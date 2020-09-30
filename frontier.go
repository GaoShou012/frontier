package frontier

import (
	"container/list"
	"fmt"
	"github.com/GaoShou012/frontier/netpoll"
	"github.com/GaoShou012/tools/ider"
	"github.com/GaoShou012/tools/logger"
	"github.com/gobwas/ws"
	uuid "github.com/satori/go.uuid"
	"net"
	"runtime"
	"sync"
	"time"
)

type Frontier struct {
	Id              string
	Addr            string
	MaxConnections  int
	DynamicParams   *DynamicParams
	Handler         *Handler
	Protocol        Protocol
	SenderParallel  int
	SenderCacheSize int

	ider   *ider.IdPool
	ln     net.Listener
	desc   *netpoll.Desc
	poller netpoll.Poller
	sender *sender

	// 打开连接
	onOpenCache chan *conn
	// 连接事件处理程序，接入，更新，断开
	event struct {
		procNum     int
		timestamp   int64
		pool        sync.Pool
		anchor      []map[int]*list.Element
		connections []*list.List
		cache       []chan *connEvent
	}

	MessageBucket chan *Message
}

func (f *Frontier) Init() error {
	f.Protocol.OnInit(f, f.DynamicParams, f.Handler)

	// init ider
	f.ider = &ider.IdPool{}
	f.ider.Init(f.MaxConnections)

	// init tcp listener
	ln, err := net.Listen("tcp", f.Addr)
	//ln, err := (&net.ListenConfig{KeepAlive: time.Second * 60}).Listen(context.TODO(), "tcp", f.Addr)
	if err != nil {
		return err
	}
	f.ln = ln

	// init poller
	poller, err := netpoll.New(nil)
	if err != nil {
		return err
	}
	desc := netpoll.Must(netpoll.HandleListener(ln, netpoll.EventRead|netpoll.EventOneShot))
	f.poller = poller
	f.desc = desc

	// init sender
	f.sender = &sender{}
	f.sender.init(f.SenderParallel, f.SenderCacheSize, f.DynamicParams)

	f.eventHandler()
	f.onOpen(runtime.NumCPU()*10, 100000)
	f.onMessage()

	return nil
}

func (f *Frontier) Start() error {
	fmt.Println("**********************************************************************************************")
	fmt.Println("ID:", f.Id)
	fmt.Println("监听地址:", f.Addr)
	fmt.Println("最大连接数量:", f.MaxConnections)
	fmt.Println("**********************************************************************************************")

	return f.poller.Start(f.desc, func(event netpoll.Event) {
		var err error
		defer func() {
			if err := f.poller.Resume(f.desc); err != nil {
				logger.Println(logger.LogError, err)
			}
		}()

		netConn, err := f.ln.Accept()
		if err != nil {
			logger.Println(logger.LogError, "To accept error:", err)
			return
		}
		defer func() {
			if err != nil {
				if err := netConn.Close(); err != nil {
					logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
				}
			}
		}()
		if f.DynamicParams.LogLevel >= logger.LogInfo {
			logger.Println(logger.LogInfo, "New connection:", netConn.RemoteAddr().String())
		}

		id, err := f.ider.Get()
		if err != nil {
			logger.Println(logger.LogError, err)
			return
		}
		defer func() {
			if err != nil {
				f.ider.Put(id)
			}
		}()

		conn := &conn{
			frontier:       f,
			protocol:       f.Protocol,
			id:             id,
			uuid:           uuid.NewV4().String(),
			state:          connStateIsNothing,
			netConn:        netConn,
			context:        nil,
			connectionTime: time.Now(),
			deadline:       0,
			desc:           nil,
		}
		conn.Init()
		err = f.Protocol.OnAccept(conn)
		if err != nil {
			return
		}
		desc := netpoll.Must(netpoll.HandleRead(netConn))
		conn.desc = desc

		f.onOpenCache <- conn
	})
}
func (f *Frontier) Stop() error {
	if err := f.poller.Stop(f.desc); err != nil {
		return err
	}
	return f.ln.Close()
}

func (f *Frontier) onOpen(procNum int, size int) {
	f.onOpenCache = make(chan *conn, size)
	for i := 0; i < procNum; i++ {
		go func() {
			for {
				conn := <-f.onOpenCache

				if f.Handler.OnOpen != nil {
					if err := f.Handler.OnOpen(conn); err != nil {
						logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
						if err := conn.NetConn().Close(); err != nil {
							logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
						}
						continue
					}
				}

				f.eventPush(ConnEventTypeInsert, conn)

				// Here we can read some new message from connection.
				// We can not read it right here in callback, because then we will
				// block the poller's inner loop.
				// We do not want to spawn a new goroutine to read single message.
				// But we want to reuse previously spawned goroutine.
				//f.poller.StartReader(conn.desc, conn)
				err := f.poller.Start(conn.desc, func(event netpoll.Event) {
					if event == (netpoll.EventRead | netpoll.EventReadHup) {
						f.onClose(conn)
						return
					}

					if !conn.IsWorking() {
						return
					}

					f.eventPush(ConnEventTypeUpdate, conn)
					f.Protocol.Reader(conn)
				})
				if err != nil {
					logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
				}
			}
		}()
	}
}
func (f *Frontier) onMessage() {
	f.MessageBucket = make(chan *Message, f.MaxConnections)
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				message := <-f.MessageBucket
				conn, opCode, payload := message.Conn, message.OpCode, message.Payload
				switch opCode {
				case ws.OpPing:
					continue
				case ws.OpPong:
					continue
				case ws.OpClose:
					f.onClose(conn)
					continue
				}
				if f.Handler.OnMessage != nil {
					f.Handler.OnMessage(conn, payload)
				} else {
					logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, "The handler has no OnMessage program")
				}
			}
		}()
	}
}
func (f *Frontier) onClose(conn *conn) {
	conn.stateRWMutex.Lock()
	defer conn.stateRWMutex.Unlock()
	if conn.state != connStateIsWorking {
		return
	} else {
		conn.state = connStateIsClosing
	}

	// 业务层关闭
	if f.Handler.OnClose != nil {
		f.Handler.OnClose(conn)
	} else {
		logger.Compare(logger.LogInfo, f.DynamicParams.LogLevel, "The handler has no OnClose program")
	}

	// 安全删除协议层的连接信息
	f.Protocol.OnClose(conn)

	// TCP协议层关闭
	if err := conn.netConn.Close(); err != nil {
		logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
	}

	// 停止epoll监听
	if err := f.poller.Stop(conn.desc); err != nil {
		logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
	}

	// 关闭desc
	if err := conn.desc.Close(); err != nil {
		logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
	}

	// 关闭心跳检查，并释放conn id
	f.eventPush(ConnEventTypeDelete, conn)
}

func (f *Frontier) eventPush(evt int, conn *conn) {
	if evt == ConnEventTypeInsert || evt == ConnEventTypeUpdate {
		conn.deadline = f.event.timestamp + f.DynamicParams.HeartbeatTimeout
	}

	event := f.event.pool.Get().(*connEvent)
	event.Type, event.Conn = evt, conn
	blockNum := conn.id % f.event.procNum
	f.event.cache[blockNum] <- event
}
func (f *Frontier) eventHandler() {
	f.event.procNum = runtime.NumCPU()
	procNum := f.event.procNum

	go func() {
		ticker := time.Tick(time.Second)
		f.event.timestamp = time.Now().Unix()
		for {
			now := <-ticker
			f.event.timestamp = now.Unix()
		}
	}()
	f.event.pool.New = func() interface{} {
		return new(connEvent)
	}
	f.event.connections = make([]*list.List, procNum)
	f.event.anchor = make([]map[int]*list.Element, procNum)
	f.event.cache = make([]chan *connEvent, procNum)
	for i := 0; i < procNum; i++ {
		f.event.connections[i] = list.New()
		f.event.anchor[i] = make(map[int]*list.Element, 100000)
		f.event.cache[i] = make(chan *connEvent, 100000)

		anchors := f.event.anchor[i]
		connections := f.event.connections[i]
		events := f.event.cache[i]
		go func(connections *list.List, events chan *connEvent) {
			defer func() { panic("exception error") }()
			tick := time.NewTicker(time.Second)
			for {
				select {
				case now := <-tick.C:
					if f.DynamicParams.HeartbeatTimeout <= 0 {
						break
					}
					deadline := now.Unix()
					for {
						ele := connections.Front()
						if ele == nil {
							break
						}
						conn := ele.Value.(*conn)
						if conn.deadline > deadline {
							break
						}
						connections.Remove(ele)

						f.onClose(conn)
						if f.DynamicParams.LogLevel >= logger.LogInfo {
							logger.Println(logger.LogInfo, "Conn Deadline", conn)
						}
					}
					break
				case event, ok := <-events:
					conn := event.Conn
					connId := conn.id

					switch event.Type {
					case ConnEventTypeInsert:
						if f.DynamicParams.LogLevel >= logger.LogInfo {
							logger.Println(logger.LogInfo, "ConnEventTypeInsert", conn)
						}
						if conn.state == connStateIsNothing {
							conn.state = connStateIsWorking
						} else {
							break
						}
						anchor := connections.PushBack(conn)
						anchors[connId] = anchor
						Connections.Inc()
						break
					case ConnEventTypeDelete:
						if f.DynamicParams.LogLevel >= logger.LogInfo {
							logger.Println(logger.LogInfo, "ConnEventTypeDelete", conn)
						}
						if conn.state == connStateIsClosing {
							conn.state = connStateWasClosed
						} else {
							break
						}
						anchor, ok := anchors[connId]

						if !ok {
							panic(fmt.Sprintf("To delete the conn id is not exists %d\n", connId))
						}
						delete(anchors, connId)
						connections.Remove(anchor)

						f.ider.Put(connId)

						close(conn.sender.cache)
						Connections.Dec()
						break
					case ConnEventTypeUpdate:
						if conn.state != connStateIsWorking {
							break
						}
						anchor, ok := anchors[connId]
						if !ok {
							panic(fmt.Sprintf("To update the conn id is not exists %d\n", connId))
						}
						connections.Remove(anchor)
						anchor = connections.PushBack(conn)
						anchors[connId] = anchor
						break
					}

					if ok {
						f.event.pool.Put(event)
					}
					break
				}
			}
		}(connections, events)
	}
}
