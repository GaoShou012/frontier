package frontier

import (
	"container/list"
	"errors"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/golang/glog"
	"io"
	"io/ioutil"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
)

var _ Protocol = &ProtocolWs{}

type readerJob struct {
	conn    *conn
	done    chan bool
	message []byte
	fin     bool
	err     error
}

type ProtocolWs struct {
	DynamicParams *DynamicParams
	Handler       *Handler
	MessageCount  int
	onMessage     chan *Message
	readerJobs    []chan *conn
	EventsMask    []sync.Map

	mutex      sync.Mutex
	EventCount int

	again  list.List
	reader chan *conn
}

func (p *ProtocolWs) OnMessage() chan *Message {
	return p.onMessage
}

func (p *ProtocolWs) OnInit(params *DynamicParams, handler *Handler) {
	p.DynamicParams = params
	p.Handler = handler

	go func() {
		ticker := time.NewTicker(time.Millisecond)
		deadline := time.Millisecond * 10
		for {
			now := <-ticker.C
			var del []*list.Element
			for ele := p.again.Front(); ele != nil; ele = ele.Next() {
				conn := ele.Value.(*conn)
				if now.Sub(conn.lastReaderTime) < deadline {
					break
				}
				del = append(del, ele)
				p.reader <- conn
			}
			for _, ele := range del {
				p.again.Remove(ele)
			}
		}
	}()

	p.reader = make(chan *conn, 100000)
	p.onMessage = make(chan *Message, 100000)
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				conn := <-p.reader
				count := 0

				header, err := ws.ReadHeader(conn.netConn)
				if err != nil {
					glog.Errorln(err)
					continue
				}

				if header.Length == 0 {
					if conn.lastCheck {
						// 直接释放
						conn.rwMutex.Lock()
						conn.isReading = false
						conn.lastCheck = false
						conn.rwMutex.Unlock()
					} else {
						// 再次提交进行最后检查
						conn.lastCheck = true
						conn.lastReaderTime = time.Now()
						p.again.PushBack(conn)
					}
					continue
				}

			Loop:
				if header.Fin {
					payload := make([]byte, header.Length)
					_, err = io.ReadFull(conn.netConn, payload)
					p.onMessage <- &Message{conn: conn, payload: payload}
				} else {
					glog.Error(header)
				}

				if count < 2 {
					count++
					header, err = ws.ReadHeader(conn.netConn)
					if err != nil {
						glog.Errorln(err)
						continue
					}
					if header.Length > 0 {
						goto Loop
					}
				}
				p.reader <- conn
			}
		}()
	}
}

func (p *ProtocolWs) OnAccept(conn Conn) error {
	_, err := ws.Upgrader{
		ReadBufferSize:  p.DynamicParams.ReaderBufferSize,
		WriteBufferSize: p.DynamicParams.WriterBufferSize,
		Protocol:        nil,
		ProtocolCustom:  nil,
		Extension:       nil,
		ExtensionCustom: nil,
		Header:          nil,
		OnRequest: func(uri []byte) error {
			if p.Handler.OnRequest != nil {
				return p.Handler.OnRequest(conn, uri)
			}
			return nil
		},
		OnHost: func(host []byte) error {
			if p.Handler.OnHost != nil {
				return p.Handler.OnHost(conn, host)
			}
			return nil
		},
		OnHeader: func(key, value []byte) error {
			if p.Handler.OnHeader != nil {
				return p.Handler.OnHeader(conn, key, value)
			}
			return nil
		},
		OnBeforeUpgrade: func() (header ws.HandshakeHeader, err error) {
			if p.Handler.OnBeforeUpgrade != nil {
				return p.Handler.OnBeforeUpgrade(conn)
			}
			return
		},
	}.Upgrade(conn.NetConn())
	return err
}
func (p *ProtocolWs) OnClose(netConn net.Conn) error {
	if err := netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout)); err != nil {
		return err
	}
	w := wsutil.NewWriter(netConn, ws.StateServerSide, ws.OpClose)
	if _, err := w.Write([]byte("close")); err != nil {
		return err
	}
	return w.Flush()
}

func (p *ProtocolWs) Writer(netConn net.Conn, message []byte) error {
	if err := netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout)); err != nil {
		return err
	}
	w := wsutil.NewWriter(netConn, ws.StateServerSide, ws.OpText)
	if _, err := w.Write(message); err != nil {
		return err
	}
	return w.Flush()
}
func (p *ProtocolWs) Reader(conn *conn) (message []byte, err error) {
	var isReading bool

	conn.rwMutex.RLock()
	isReading = conn.isReading
	conn.rwMutex.RUnlock()
	if isReading {
		return
	}

	conn.rwMutex.Lock()
	isReading = conn.isReading
	conn.isReading = true
	conn.rwMutex.Unlock()
	if isReading {
		return
	}

	p.reader <- conn
	return
}

func (p *ProtocolWs) Reader4(conn *conn) (message []byte, err error) {
	//_,err = ws.ReadFrame(conn.netConn)
	//if err != nil {
	//	glog.Errorln(err)
	//	return
	//}
	//fmt.Println(r)
	//p.MessageCount++
	header, err := ws.ReadHeader(conn.netConn)
	if err != nil {
		glog.Errorln(err)
		return
		//	//handle error
	}
	if header.Fin {
		payload := make([]byte, header.Length)
		_, err = io.ReadFull(conn.netConn, payload)
		message = append(conn.temp, payload...)
		p.MessageCount++
		if string(payload) != "ping" {
			glog.Errorln(header)
			glog.Errorln(payload)
		}
		return
	} else {
		glog.Errorln(header)
		payload := make([]byte, header.Length)
		_, err = io.ReadFull(conn.netConn, payload)
		glog.Errorln(string(payload))
		os.Exit(1)
	}

	fmt.Println(header)
	return
	//Loop:

	switch header.OpCode {
	case ws.OpContinuation:
		//glog.Errorln("continuation")
		//fmt.Println(header)

		payload := make([]byte, header.Length)
		_, err = io.ReadFull(conn.netConn, payload)
		//if err != nil {
		//	return
		//}
		if header.Length == 0 {
			return
		}
		if header.Fin {
			message = append(conn.temp, payload...)
			conn.temp = nil
			p.MessageCount++
		} else {
			conn.temp = append(conn.temp, payload...)
		}
		break
	case ws.OpText:
		//glog.Errorln("optext")
		//fmt.Println(header)
		if header.Length != 4 {
			panic(header)
		}
		payload := make([]byte, header.Length)
		_, err = io.ReadFull(conn.netConn, payload)
		if err != nil {
			return
		}
		if header.Fin {
			message = append(conn.temp, payload...)
			conn.temp = nil
			p.MessageCount++
		} else {
			fmt.Println("not fin", header, payload)
			conn.temp = append(conn.temp, payload...)
		}

		//header, err = ws.ReadHeader(conn.netConn)
		////fmt.Println(header)
		//if err == nil {
		//	goto Loop
		//} else {
		//	glog.Errorln(err)
		//	err = nil
		//}
		//if err.(syscall.Errno) == unix.EWOULDBLOCK || err.(syscall.Errno) == unix.EAGAIN {
		//	glog.Errorln("yes syscall.Errno")
		//	goto Loop
		//} else {
		//	err = nil
		//}

		break
	case ws.OpBinary:
		glog.Errorln("opbinary")
		break
	case ws.OpClose:
		glog.Errorln("opclose")
		break
	case ws.OpPing:
		glog.Errorln("oping")
		break
	case ws.OpPong:
		glog.Errorln("oppong")
		break
	default:
		glog.Errorln("未处理opCode", header.OpCode, conn.netConn.RemoteAddr())
	}

	return
}

func (p *ProtocolWs) Reader1(netConn net.Conn) (message []byte, err error) {
	header, err := ws.ReadHeader(netConn)
	if err != nil {
		glog.Errorln(err)
		return
	}

	// Reset the Masked flag, server frames must not be masked as
	// RFC6455 says.
	header.Masked = false

	switch header.OpCode {
	case ws.OpContinuation:
		glog.Errorln("continuation", netConn.RemoteAddr())
		break
	case ws.OpText:
		message = make([]byte, header.Length)
		_, err = io.ReadFull(netConn, message)
		if err != nil {
			glog.Errorln(err)
			// handle error
			return
		}
		break
	case ws.OpBinary:
		break
	case ws.OpClose:
		glog.Errorln("close", netConn.RemoteAddr())
		err = errors.New("closed")
		//err = netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout))
		//if err != nil {
		//	return
		//}
		//err = ws.WriteHeader(netConn, header)
		//if err != nil {
		//	glog.Errorln(err)
		//	return
		//}
		//_, err = netConn.Write(ws.CompiledClose)
		//if err != nil {
		//	glog.Errorln(err)
		//	return
		//}
		break
	case ws.OpPing:
		glog.Errorln("ping", netConn.RemoteAddr())
		err = netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout))
		if err != nil {
			return
		}
		err = ws.WriteHeader(netConn, header)
		if err != nil {
			glog.Errorln(err)
			return
		}
		_, err = netConn.Write(ws.CompiledPong)
		if err != nil {
			glog.Errorln(err)
			return
		}
		break
	case ws.OpPong:
		glog.Errorln("pong", netConn.RemoteAddr())
		err = netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout))
		if err != nil {
			return
		}
		err = ws.WriteHeader(netConn, header)
		if err != nil {
			glog.Errorln(err)
			return
		}
		_, err = netConn.Write(ws.CompiledPing)
		if err != nil {
			glog.Errorln(err)
			return
		}
		break
	default:
		glog.Errorln("未处理opCode", header.OpCode, netConn.RemoteAddr())
	}
	return
}
func (p *ProtocolWs) Reader2(netConn net.Conn) (message []byte, err error) {
	message, op, err := wsutil.ReadClientData(netConn)
	if err != nil {
		return
	}
	switch op {
	case ws.OpContinuation:
		glog.Errorln("continuation", netConn.RemoteAddr())
		break
	case ws.OpText:
		break
	case ws.OpBinary:
		break
	case ws.OpClose:
		glog.Errorln("close", netConn.RemoteAddr())
		err = errors.New("closed")
		//err = netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout))
		//if err != nil {
		//	return
		//}
		//err = ws.WriteHeader(netConn, header)
		//if err != nil {
		//	glog.Errorln(err)
		//	return
		//}
		//_, err = netConn.Write(ws.CompiledClose)
		//if err != nil {
		//	glog.Errorln(err)
		//	return
		//}
		break
	case ws.OpPing:
		glog.Errorln("ping", netConn.RemoteAddr())
		err = netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout))
		if err != nil {
			return
		}
		w := wsutil.NewControlWriter(netConn, ws.StateServerSide, ws.OpPong)
		if _, e := w.Write(nil); e != nil {
			err = e
		}
		if e := w.Flush(); e != nil {
			err = e
		}
		break
	case ws.OpPong:
		glog.Errorln("pong", netConn.RemoteAddr())
		err = netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout))
		if err != nil {
			return
		}

		w := wsutil.NewControlWriter(netConn, ws.StateServerSide, ws.OpPing)
		if _, e := w.Write(nil); e != nil {
			err = e
		}
		if e := w.Flush(); e != nil {
			err = e
		}
		break
	default:
		glog.Errorln("未处理opCode", op, netConn.RemoteAddr())
	}
	return
}

func (p *ProtocolWs) ReaderOld(netConn net.Conn) (message []byte, err error) {
	h, r, err := wsutil.NextReader(netConn, ws.StateServerSide)
	if err != nil {
		return
	}

	if h.OpCode.IsControl() {
		if h.OpCode == ws.OpPing {
			err = netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.WriterTimeout))
			if err != nil {
				return
			}
			w := wsutil.NewControlWriter(netConn, ws.StateServerSide, ws.OpPong)
			if _, e := w.Write(nil); e != nil {
				err = e
			}
			if e := w.Flush(); e != nil {
				err = e
			}
			return
		}
		err = wsutil.ControlFrameHandler(netConn, ws.StateServerSide)(h, r)
		return
	}

	message, err = ioutil.ReadAll(r)
	return
}
