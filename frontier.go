package frontier

import (
	"bytes"
	"container/list"
	"context"
	"fmt"
	"frontier/netpoll"
	"github.com/golang/glog"
	"net"
	"runtime"
	"sync"
	"time"
)

const (
	LogLevelClose = iota
	LogLevelError
	LogLevelWarning
	LogLevelInfo
	LogLevelAll
)

type Frontier struct {
	Id               string
	HeartbeatTimeout int64
	Protocol         Protocol
	Handler          Handler

	// 写缓存大小，读缓存大小
	WriterBufferSize int
	ReaderBufferSize int

	// 支持在线实时变更的参数
	// 写超时，读超时，日志级别
	WriterTimeout *time.Duration
	ReaderTimeout *time.Duration
	LogLevel      *int

	ider *ider
	ln   net.Listener

	// poller
	desc   *netpoll.Desc
	poller netpoll.Poller

	sender *sender

	// conn event's handler
	// insert,update,delete
	event struct {
		procNum     int
		timestamp   int64
		pool        sync.Pool
		anchor      map[int]*list.Element
		connections []*list.List
		cache       []chan *connEvent
	}

	// 打开连接
	onOpenCache chan *conn
	// 接受数据
	onRecvCache chan *conn
	// 消息处理
	onMessageHandle struct {
		pool  sync.Pool
		cache chan *Message
	}
}

func (f *Frontier) Init(ctx context.Context, addr string, maxConnections int) error {
	f.Protocol.OnInit(
		f.WriterBufferSize,
		f.ReaderBufferSize,
		f.WriterTimeout,
		f.ReaderTimeout,
		f.LogLevel,
	)
	f.ider.init(maxConnections)

	// init tcp listener
	ln, err := (&net.ListenConfig{KeepAlive: time.Second * 60}).Listen(ctx, "tcp", addr)
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
	f.sender.init(4, 100000, f.LogLevel)

	// 事件处理者
	// 事件会存在异步情况
	f.eventHandler()

	f.onOpen(runtime.NumCPU()*10, 100000)
	f.onRecv(100000)
	f.onHandle(100000)

	return nil
}

func (f *Frontier) Start() error {
	return f.poller.Start(f.desc, func(event netpoll.Event) {
		var err error
		defer func() {
			if err := f.poller.Resume(f.desc); err != nil {
				glog.Errorln(err)
			}
		}()

		netConn, err := f.ln.Accept()
		if err != nil {
			glog.Errorln("accept error:", err)
			return
		}
		defer func() {
			if err != nil {
				netConn.Close()
			}
		}()

		id, err := f.ider.get()
		if err != nil {
			glog.Errorln(err)
			return
		}
		defer func() {
			if err != nil {
				f.ider.put(id)
			}
		}()

		err = f.Protocol.OnAccept(netConn)
		if err != nil {
			return
		}

		desc := netpoll.Must(netpoll.HandleRead(netConn))
		conn := &conn{
			frontier:       f,
			protocol:       f.Protocol,
			id:             id,
			state:          0,
			netConn:        netConn,
			context:        nil,
			connectionTime: time.Now(),
			deadline:       0,
			desc:           desc,
		}

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

				if f.Handler.OnOpen(conn) == false {
					if err := conn.NetConn().Close(); err != nil && *f.LogLevel >= LogLevelWarning {
						glog.Warningln(err)
					}
					f.ider.put(conn.id)
					continue
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
				f.poller.Start(conn.desc, func(event netpoll.Event) {
					go func() {
						data, err := conn.protocol.Reader(conn.netConn)
						if err != nil {
							if *f.LogLevel >= LogLevelWarning {
								glog.Infoln(err)
							}
							f.Handler.OnClose(conn)
							if err := f.Protocol.OnClose(conn.netConn); err != nil && *f.LogLevel >= LogLevelWarning {
								glog.Infoln(err)
							}
							if err := conn.netConn.Close(); err != nil && *f.LogLevel >= LogLevelWarning {
								glog.Infoln(err)
							}
							if err := f.poller.Stop(conn.desc); err != nil && *f.LogLevel >= LogLevelWarning {
								glog.Infoln(err)
							}
							f.ider.put(conn.id)
							f.eventPush(ConnEventTypeDelete, conn)
						}
						if bytes.Equal(data, []byte("ping")) {
							f.eventPush(ConnEventTypeUpdate, conn)
							return
						}
						message := f.onMessageHandle.pool.Get().(*Message)
						message.conn, message.data = conn, data
						f.onMessageHandle.cache <- message
						f.eventPush(ConnEventTypeUpdate, conn)
					}()
				})
			}
		}()
	}
}

func (f *Frontier) onHandle(size int) {
	f.onMessageHandle.cache = make(chan *Message, size)
	f.onMessageHandle.pool.New = func() interface{} {
		return new(Message)
	}
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				message := <-f.onMessageHandle.cache
				conn, data := message.conn, message.data
				f.Handler.OnMessage(conn, data)
				f.onMessageHandle.pool.Put(message)
			}
		}()
	}
}

func (f *Frontier) eventPush(evt int, conn *conn) {
	if evt == ConnEventTypeInsert {
		conn.deadline = f.event.timestamp + f.HeartbeatTimeout
	} else if evt == ConnEventTypeUpdate {
		if (conn.deadline - f.event.timestamp) < 5 {
			return
		}
		conn.deadline = f.event.timestamp + f.HeartbeatTimeout
	}

	event := f.event.pool.Get().(*connEvent)
	event.Type, event.Conn = evt, conn
	blockNum := conn.id & f.event.procNum
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
	f.event.anchor = make(map[int]*list.Element)
	f.event.connections = make([]*list.List, procNum)
	f.event.cache = make([]chan *connEvent, procNum)
	for i := 0; i < procNum; i++ {
		f.event.connections[i] = list.New()
		f.event.cache[i] = make(chan *connEvent, 50000)

		connections := f.event.connections[i]
		events := f.event.cache[i]
		go func(connections *list.List, events chan *connEvent) {
			defer func() { panic("exception error") }()
			tick := time.NewTicker(time.Second)
			for {
				select {
				case now := <-tick.C:
					if f.HeartbeatTimeout <= 0 {
						return
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
						if *f.LogLevel >= LogLevelInfo {
							glog.Infof("服务端关闭超时连接 %d %s\n", conn.id, conn.netConn.RemoteAddr().String())
						}
						f.Handler.OnClose(conn)
						if err := f.Protocol.OnClose(conn.netConn); err != nil && *f.LogLevel >= LogLevelWarning {
							glog.Warningln(err)
						}
						if err := f.poller.Stop(conn.desc); err != nil && *f.LogLevel >= LogLevelWarning {
							glog.Warningln(err)
						}
						f.ider.put(conn.id)

						connId := conn.id
						conn.state = connStateWasClosed

						anchor, ok := f.event.anchor[connId]
						if !ok {
							panic(fmt.Sprintf("The conn id is not exists. %d\n", connId))
						}
						delete(f.event.anchor, connId)
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
						f.event.anchor[connId] = anchor
						break
					case ConnEventTypeDelete:
						if conn.state == connStateIsWorking {
							conn.state = connStateWasClosed
						} else {
							break
						}
						anchor, ok := f.event.anchor[connId]

						if !ok {
							panic(fmt.Sprintf("delete the conn id is not exists %d\n", connId))
						}
						delete(f.event.anchor, connId)
						connections.Remove(anchor)
						break
					case ConnEventTypeUpdate:
						if conn.state != connStateIsWorking {
							break
						}
						anchor, ok := f.event.anchor[connId]
						if !ok {
							panic(fmt.Sprintf("update the conn id is not exists %d\n", connId))
						}
						connections.Remove(anchor)
						anchor = connections.PushBack(conn)
						f.event.anchor[connId] = anchor
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
