package frontier

import (
	"container/list"
	"fmt"
	"github.com/GaoShou012/frontier/netpoll"
	"github.com/GaoShou012/tools/ider"
	"github.com/GaoShou012/tools/logger"
	"net"
	"runtime"
	"sync"
	"time"
)

type DynamicParams struct {
	// 日志级别
	LogLevel int
	// 心跳超时
	HeartbeatTimeout int64
	// 写缓存大小，读缓存大小
	WriterBufferSize int
	ReaderBufferSize int
	// 写超时，读超时
	WriterTimeout time.Duration
	ReaderTimeout time.Duration
}

type ReaderEvent struct {
	conn  *conn
	event netpoll.Event
}

type Frontier struct {
	Id             string
	Addr           string
	MaxConnections int
	DynamicParams  *DynamicParams
	Handler        *Handler
	Protocol       Protocol

	ider   *ider.IdPool
	ln     net.Listener
	desc   *netpoll.Desc
	poller netpoll.Poller
	sender *sender

	// 打开连接
	onOpenCache chan *conn
	// 接受数据
	onRecvCache chan *conn
	// 消息处理
	onMessageHandle struct {
		pool  sync.Pool
		cache chan *Message
	}
	// 连接事件处理程序，接入，更新，断开
	event struct {
		procNum     int
		timestamp   int64
		pool        sync.Pool
		anchor      []map[int]*list.Element
		connections []*list.List
		cache       []chan *connEvent
	}

	readerEvent chan *ReaderEvent
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
	f.sender.init(2, 100000, f.DynamicParams)

	f.readerEvent = make(chan *ReaderEvent, 200000)

	f.eventHandler()
	f.onOpen(runtime.NumCPU()*10, 100000)
	f.onRecv(100000)
	f.onHandle()
	f.onEvent()

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

		fmt.Println("f.desc", event)

		netConn, err := f.ln.Accept()
		if err != nil {
			logger.Println(logger.LogError, "To accept error:", err)
			return
		}
		defer func() {
			if err != nil {
				netConn.Close()
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
			state:          0,
			netConn:        netConn,
			context:        nil,
			connectionTime: time.Now(),
			deadline:       0,
			desc:           nil,
			wsReader:       &wsReader{bufferSiz: 512},
		}
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

						if err := conn.protocol.OnClose(conn.netConn); err != nil {
							logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
						}
						if err := conn.NetConn().Close(); err != nil {
							logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
						}
						f.ider.Put(conn.id)
						continue
					}
				} else {
					logger.Compare(logger.LogInfo, f.DynamicParams.LogLevel, "The handler has no OnOpen program")
				}

				f.onRecvCache <- conn
			}
		}()
	}
}

func (f *Frontier) onRecv(size int) {
	f.onRecvCache = make(chan *conn, size)
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				conn := <-f.onRecvCache
				f.eventPush(ConnEventTypeInsert, conn)

				// Here we can read some new message from connection.
				// We can not read it right here in callback, because then we will
				// block the poller's inner loop.
				// We do not want to spawn a new goroutine to read single message.
				// But we want to reuse previously spawned goroutine.
				f.poller.StartReader(conn.desc, conn)
			}
		}()
	}
}

func (f *Frontier) onEvent() {
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				event := <-f.poller.OnEvent()
				conn := event.Ctx.(*conn)
				f.Protocol.Reader(conn)
			}
		}()
	}
}

func (f *Frontier) onHandle() {
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				message := <-f.Protocol.OnMessage()
				if f.Handler.OnMessage != nil {
					f.Handler.OnMessage(message)
				} else {
					logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, "The handler has no OnMessage program")
				}
			}
		}()
	}
}

func (f *Frontier) eventPush(evt int, conn *conn) {
	return
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
						if f.DynamicParams.LogLevel >= logger.LogInfo {
							logger.Println(logger.LogInfo, "To close timeout connection:", conn.id, conn.netConn.RemoteAddr().String())
						}
						if f.Handler.OnClose != nil {
							f.Handler.OnClose(conn)
						} else {
							logger.Compare(logger.LogInfo, f.DynamicParams.LogLevel, "The handler has no OnClose program")
						}
						if err := f.Protocol.OnClose(conn.netConn); err != nil {
							logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
						}
						if err := f.poller.Stop(conn.desc); err != nil {
							logger.Compare(logger.LogWarning, f.DynamicParams.LogLevel, err)
						}
						f.ider.Put(conn.id)

						connId := conn.id
						conn.state = connStateWasClosed

						anchor, ok := anchors[connId]
						if !ok {
							panic(fmt.Sprintf("The conn id is not exists. %d\n", connId))
						}
						delete(anchors, connId)
						connections.Remove(anchor)
					}
					break
				case event, ok := <-events:
					conn := event.Conn
					connId := conn.id

					switch event.Type {
					case ConnEventTypeInsert:
						if conn.state == connStateIsNothing {
							conn.state = connStateIsWorking
						} else {
							break
						}
						anchor := connections.PushBack(conn)
						anchors[connId] = anchor
						break
					case ConnEventTypeDelete:
						if conn.state == connStateIsWorking {
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
