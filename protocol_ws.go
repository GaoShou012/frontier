package frontier

import (
	"container/list"
	"encoding/binary"
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

	delay1m   list.List
	delay10m  list.List
	delay100m list.List
}

const (
	bit0 = 0x80
	bit1 = 0x40
	bit2 = 0x20
	bit3 = 0x10
	bit4 = 0x08
	bit5 = 0x04
	bit6 = 0x02
	bit7 = 0x01

	len7  = int64(125)
	len16 = int64(^(uint16(0)))
	len64 = int64(^(uint64(0)) >> 1)
)

func ParseHeaderExtra(bts []byte) (extra int, err error) {
	if bts[1]&bit0 != 0 {
		extra += 4
	}

	length := bts[1] & 0x7f

	switch {
	case length < 126:
	case length == 126:
		extra += 2

	case length == 127:
		extra += 8

	default:
		err = ws.ErrHeaderLengthUnexpected
		return
	}
	return
}

func ParseHeader(bts []byte) (h ws.Header, err error) {
	h.Fin = bts[0]&bit0 != 0
	h.Rsv = (bts[0] & 0x70) >> 4
	h.OpCode = ws.OpCode(bts[0] & 0x0f)

	var extra int

	if bts[1]&bit0 != 0 {
		h.Masked = true
		extra += 4
	}

	length := bts[1] & 0x7f
	switch {
	case length < 126:
		h.Length = int64(length)

	case length == 126:
		extra += 2

	case length == 127:
		extra += 8

	default:
		err = ws.ErrHeaderLengthUnexpected
		return
	}

	if extra == 0 {
		return
	}

	// Increase len of bts to extra bytes need to read.
	// Overwrite first 2 bytes that was read before.
	bts = bts[2:]

	switch {
	case length == 126:
		h.Length = int64(binary.BigEndian.Uint16(bts[:2]))
		bts = bts[2:]

	case length == 127:
		if bts[0]&0x80 != 0 {
			err = ws.ErrHeaderLengthMSB
			return
		}
		h.Length = int64(binary.BigEndian.Uint64(bts[:8]))
		bts = bts[8:]
	}

	if h.Masked {
		copy(h.Mask[:], bts)
	}
	return
}

func parseHeader(bts []byte) (h ws.Header, extra int, err error) {
	h.Fin = bts[0]&bit0 != 0
	h.Rsv = (bts[0] & 0x70) >> 4
	h.OpCode = ws.OpCode(bts[0] & 0x0f)

	if bts[1]&bit0 != 0 {
		h.Masked = true
		extra += 4
	}

	length := bts[1] & 0x7f
	switch {
	case length < 126:
		h.Length = int64(length)

	case length == 126:
		extra += 2

	case length == 127:
		extra += 8

	default:
		err = ws.ErrHeaderLengthUnexpected
		return
	}

	if extra == 0 {
		return
	}

	bts = bts[2:]

	switch {
	case length == 126:
		h.Length = int64(binary.BigEndian.Uint16(bts[:2]))
		bts = bts[2:]

	case length == 127:
		if bts[0]&0x80 != 0 {
			err = ws.ErrHeaderLengthMSB
			return
		}
		h.Length = int64(binary.BigEndian.Uint64(bts[:8]))
		bts = bts[8:]
	}

	if h.Masked {
		copy(h.Mask[:], bts)
	}
	return
}
func getHeader1(bts []byte) (h ws.Header, extra int, err error) {
	h.Fin = bts[0]&bit0 != 0
	h.Rsv = (bts[0] & 0x70) >> 4
	h.OpCode = ws.OpCode(bts[0] & 0x0f)

	if bts[1]&bit0 != 0 {
		h.Masked = true
		extra += 4
	}

	length := bts[1] & 0x7f
	switch {
	case length < 126:
		h.Length = int64(length)

	case length == 126:
		extra += 2

	case length == 127:
		extra += 8

	default:
		err = ws.ErrHeaderLengthUnexpected
		return
	}

	if extra == 0 {
		return
	}

	bts = bts[2:]

	switch {
	case length == 126:
		h.Length = int64(binary.BigEndian.Uint16(bts[:2]))
		bts = bts[2:]

	case length == 127:
		if bts[0]&0x80 != 0 {
			err = ws.ErrHeaderLengthMSB
			return
		}
		h.Length = int64(binary.BigEndian.Uint64(bts[:8]))
		bts = bts[8:]
	}

	if h.Masked {
		copy(h.Mask[:], bts)
	}
	return
}

func getHeader(r io.Reader, bts []byte) (h ws.Header, err error) {
	h.Fin = bts[0]&bit0 != 0
	h.Rsv = (bts[0] & 0x70) >> 4
	h.OpCode = ws.OpCode(bts[0] & 0x0f)

	var extra int

	if bts[1]&bit0 != 0 {
		h.Masked = true
		extra += 4
	}

	length := bts[1] & 0x7f
	switch {
	case length < 126:
		h.Length = int64(length)

	case length == 126:
		extra += 2

	case length == 127:
		extra += 8

	default:
		err = ws.ErrHeaderLengthUnexpected
		return
	}

	if extra == 0 {
		return
	}

	// Increase len of bts to extra bytes need to read.
	// Overwrite first 2 bytes that was read before.
	bts = bts[:extra]
	_, err = io.ReadFull(r, bts)
	if err != nil {
		return
	}

	switch {
	case length == 126:
		h.Length = int64(binary.BigEndian.Uint16(bts[:2]))
		bts = bts[2:]

	case length == 127:
		if bts[0]&0x80 != 0 {
			err = ws.ErrHeaderLengthMSB
			return
		}
		h.Length = int64(binary.BigEndian.Uint64(bts[:8]))
		bts = bts[8:]
	}

	if h.Masked {
		copy(h.Mask[:], bts)
	}
	return
}

func asyncRead(conn *conn) (payload []byte, err error) {

	switch conn.readerState {
	case wsStateEdge:
		if err := conn.netConn.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
			return
		}
		n, err := conn.netConn.Read(conn.readerBuf)
		if err != nil {
			return
		}
		if n == 0 {
			return
		}
		if n < 2 {
			conn.readerState = wsStateReadingHeader
			return
		}

		break
	}

	n, err := conn.netConn.Read(conn.readerBuf)
	if n == 0 {
		if conn.lastCheck {
			conn.rwMutex.Lock()
			conn.isReading = false
			conn.lastCheck = false
			conn.rwMutex.Unlock()
		} else {
			conn.lastCheck = true
			conn.lastReaderTime = time.Now()
			p.again.PushBack(conn)
		}
		continue
	}
	if n == 1 {
		conn.readerState = wsStateReadingHeader
		conn.readerBuf = buf
		conn.readerBufN = n
		p.again.PushBack(conn)
		continue
	}

	header, extra, err := ParseHeader(buf)
	if err != nil {
		glog.Errorln(err)
		continue
	}

	// 检查是否已经读取mask数据
	if n < extra+2 {
		conn.readerState = wsStateReadingMask
		glog.Errorln("WS Reading Mask")
		continue
	}
}

func (p *ProtocolWs) OnMessage() chan *Message {
	return p.onMessage
}

type wsProtocol struct {
	header        ws.Header
	readerState   int
	readerBufferN int
	readerBuffer  []byte
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

	go func() {
		ticker := time.NewTicker(time.Millisecond)
		deadline := time.Millisecond
		for {
			now := <-ticker.C
			var del []*list.Element
			for ele := p.delay100m.Front(); ele != nil; ele = ele.Next() {
				conn := ele.Value.(*conn)
				if now.Sub(conn.lastReaderTime) < deadline {
					break
				}
				del = append(del, ele)
				p.reader <- conn
			}
			for _, ele := range del {
				p.delay100m.Remove(ele)
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Millisecond)
		deadline := time.Millisecond * 10
		for {
			now := <-ticker.C
			var del []*list.Element
			for ele := p.delay10m.Front(); ele != nil; ele = ele.Next() {
				conn := ele.Value.(*conn)
				if now.Sub(conn.lastReaderTime) < deadline {
					break
				}
				del = append(del, ele)
				p.reader <- conn
			}
			for _, ele := range del {
				p.delay10m.Remove(ele)
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Millisecond)
		deadline := time.Millisecond
		for {
			now := <-ticker.C
			var del []*list.Element
			for ele := p.delay1m.Front(); ele != nil; ele = ele.Next() {
				conn := ele.Value.(*conn)
				if now.Sub(conn.lastReaderTime) < deadline {
					break
				}
				del = append(del, ele)
				p.reader <- conn
			}
			for _, ele := range del {
				p.delay1m.Remove(ele)
			}
		}
	}()

	p.reader = make(chan *conn, 100000)
	p.onMessage = make(chan *Message, 100000)

	//for i := 0; i < runtime.NumCPU(); i++ {
	//	go func() {
	//		for {
	//			conn := <-p.reader
	//			//fmt.Println("pop", conn)
	//			err := conn.netConn.SetReadDeadline(time.Now().Add(time.Millisecond))
	//			if err != nil {
	//				glog.Errorln(err)
	//				continue
	//			}
	//			bts := make([]byte, 2, ws.MaxHeaderSize-2)
	//			n, err := conn.netConn.Read(bts)
	//			//fmt.Println("n:", n, bts)
	//			//fmt.Println(bts)
	//			//p.reader <- conn
	//			//continue
	//			//if n != ws.MaxHeaderSize-2 {
	//			if n != 2 {
	//				if conn.lastCheck {
	//					// 直接释放
	//					conn.rwMutex.Lock()
	//					conn.isReading = false
	//					conn.lastCheck = false
	//					conn.rwMutex.Unlock()
	//					//fmt.Println("release", conn)
	//				} else {
	//					// 再次提交进行最后检查
	//					conn.lastCheck = true
	//					conn.lastReaderTime = time.Now()
	//					//fmt.Println("lastcheck", conn)
	//					p.again.PushBack(conn)
	//				}
	//				continue
	//			}
	//			header, err := getHeader(conn.netConn, bts)
	//			//header, err := ws.ReadHeader(conn.netConn)
	//			if err != nil {
	//				glog.Errorln(err)
	//			}
	//			//fmt.Println(header)
	//			if header.Length != 5 {
	//				glog.Errorln(header)
	//			}
	//			if header.Fin {
	//				payload := make([]byte, header.Length)
	//				_, err = io.ReadFull(conn.netConn, payload)
	//				if header.Masked {
	//					ws.Cipher(payload, header.Mask, 0)
	//				}
	//				p.onMessage <- &Message{conn: conn, payload: payload}
	//				//fmt.Println(payload)
	//				//fmt.Println(string(payload))
	//			} else {
	//				glog.Error(header)
	//			}
	//
	//			//fmt.Println("push", conn)
	//			//conn.lastCheck = false
	//			//p.reader <- conn
	//			p.again.PushBack(conn)
	//		}
	//	}()
	//}

	//parallel := &tools.Parallel{}
	//parallel.Init(runtime.NumCPU())

	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				conn := <-p.reader
				//if err := conn.netConn.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
				//	glog.Errorln(err)
				//	continue
				//}
				//buf := make([]byte, 1024)
				//n, err := conn.netConn.Read(buf)
				//if err != nil {
				//	if err == io.EOF {
				//		// 投放关闭事件
				//		glog.Errorln(io.EOF)
				//		continue
				//	}
				//}

				switch conn.readerState {
				case wsStateEdge:
					if err := conn.netConn.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
						glog.Errorln(err)
					}
					n, err := conn.netConn.Read(conn.wsProtocol.readerBuffer)
					if err != nil {
						glog.Errorln(err)
						continue
					}
					if n == 0 {
						if conn.lastCheck {
							conn.rwMutex.Lock()
							conn.isReading = false
							conn.lastCheck = false
							conn.rwMutex.Unlock()
						} else {
							conn.lastCheck = true
							conn.lastReaderTime = time.Now()
							p.again.PushBack(conn)
						}
						continue
					}
					if n == 1 {
						conn.readerState = wsStateReadingHeader
						p.again.PushBack(conn)
						continue
					}

					// 解析Header
					extra, err := ParseHeaderExtra(conn.wsProtocol.readerBuffer)
					if err != nil {
						glog.Errorln(err)
						continue
					}

					// 检查是否已经读取mask数据
					if n < extra+2 {
						glog.Errorln("WS Reading Mask")
						conn.wsProtocol.readerState = wsStateReadingMask
						conn.wsProtocol.readerBufferN = n
						p.again.PushBack(conn)
						continue
					}

					break
				case wsStateReadingHeader:
					if n == 0 {
						p.again.PushBack(conn)
						continue
					}
					tail := conn.readerBuf[:conn.readerBufN]
					buf = append(tail, buf...)

					break
				case wsStateReadingMask:

					break
				}

				if n == 0 {
					if conn.lastCheck {
						conn.rwMutex.Lock()
						conn.isReading = false
						conn.lastCheck = false
						conn.rwMutex.Unlock()
						//fmt.Println("release")
					} else {
						conn.lastCheck = true
						conn.lastReaderTime = time.Now()
						p.again.PushBack(conn)
						//fmt.Println("again")
					}
					continue
				}

				if conn.readerState == connReaderStateHasTail {
					buf = conn.readerBuf[:conn.readerBufOffset+n]
				} else {
					buf = buf[:n]
				}

				//Again:
				if n < 6 {
					// 复制到conn cache
					glog.Errorln("n < 6", buf)
					os.Exit(1)
				}

			Again:
				// 检查buf长度

				header, extra, err := parseHeader(buf)
				if err != nil {
					glog.Errorln(err)
					continue
				}

				payload := buf[2+extra:]
				payloadLength := int64(len(payload))
				if payloadLength == header.Length {
					conn.readerState = connReaderStateEdge
					conn.lastReaderTime = time.Now()
					p.delay10m.PushBack(conn)

					message := &Message{conn: conn, payload: payload}
					if header.Masked {
						ws.Cipher(message.payload, header.Mask, 0)
					}
					p.onMessage <- message
					continue
				}
				if payloadLength > header.Length {
					// 多包，立即返回排队操作
					//glog.Errorln("多包")

					message := &Message{conn: conn, payload: payload[:header.Length]}
					if header.Masked {
						ws.Cipher(message.payload, header.Mask, 0)
					}
					p.onMessage <- message

					// 尾包，再次检查
					buf = payload[header.Length:]
					if len(buf) < 2 {
						conn.readerBuf = make([]byte, 1024)
						copy(conn.readerBuf, buf)
						conn.readerState = connReaderStateHasTail
						conn.readerBufOffset = len(buf)
						p.reader <- conn
						continue
					}
					goto Again
					//tail := payload[header.Length:]
					//fmt.Println("tail length", len(tail))
					//conn.readerBuf = make([]byte, 1024)
					//copy(conn.readerBuf, tail)
					//conn.readerBufOffset = len(tail)
					//p.reader <- conn
					//continue
				}
				if payloadLength < header.Length {
					// 半包
					// 投放到快查询，等待后半包
					glog.Errorln("半包")

					conn.readerState = 1
					conn.lastReaderTime = time.Now()
					p.delay1m.PushBack(conn)
					continue
				}

				p.reader <- conn
				//conn := <-p.reader
				//go func() {
				//	for {
				//n, err := conn.netConn.Read(conn.readerBuf)
				//if err != nil {
				//	glog.Errorln(err)
				//	return
				//}
				//if conn.readerState == 0 {
				//	// 刚好在包边缘
				//	bts := buf[:2]
				//	header, err := getHeader(conn.netConn, bts)
				//	if err != nil {
				//		glog.Errorln(err)
				//		return
				//	}
				//	end := header.Length + 2
				//	payload := buf[2:end]
				//}
				//}
				//}()
			}
		}()
	}

	//return
	//for i := 0; i < runtime.NumCPU(); i++ {
	//	go func() {
	//		for {
	//			conn := <-p.reader
	//			fmt.Println("pop", conn)
	//			header, err := ws.ReadHeader(conn.netConn)
	//			if err != nil {
	//				glog.Errorln(err)
	//				continue
	//			}
	//		Loop:
	//			fmt.Println("header", header)
	//			if header.Length == 0 {
	//				fmt.Println("header", header)
	//				if conn.lastCheck {
	//					// 直接释放
	//					conn.rwMutex.Lock()
	//					conn.isReading = false
	//					conn.lastCheck = false
	//					conn.rwMutex.Unlock()
	//					fmt.Println("release")
	//				} else {
	//					// 再次提交进行最后检查
	//					conn.lastCheck = true
	//					conn.lastReaderTime = time.Now()
	//					fmt.Println("lastcheck")
	//					p.again.PushBack(conn)
	//				}
	//				continue
	//			}
	//
	//			count++
	//			fmt.Println("count", count)
	//			if header.Fin {
	//				payload := make([]byte, header.Length)
	//				_, err = io.ReadFull(conn.netConn, payload)
	//				p.onMessage <- &Message{conn: conn, payload: payload}
	//			} else {
	//				glog.Error(header)
	//			}
	//
	//			err = conn.netConn.SetReadDeadline(time.Now().Add(time.Millisecond))
	//			if err != nil {
	//				glog.Errorln(err)
	//			}
	//			bts := make([]byte, 2, ws.MaxHeaderSize-2)
	//			n, err := conn.netConn.Read(bts)
	//			if n == 0 {
	//				header.Length = 0
	//				goto Loop
	//			}
	//			//if n != len(bts) {
	//			//	conn.rwMutex.Lock()
	//			//	conn.isReading = false
	//			//	conn.lastCheck = false
	//			//	conn.rwMutex.Unlock()
	//			//	fmt.Println("release")
	//			//	continue
	//			//}
	//			header, err = getHeader(conn.netConn, bts)
	//			fmt.Println("header2", header)
	//			goto Loop
	//
	//			//n, err := conn.netConn.Read(bts)
	//			//io.ReadFull(conn.netConn,bts)
	//			//header, err = ws.ReadHeader(conn.netConn)
	//			//fmt.Println("n", n)
	//			//fmt.Println("bts", bts)
	//
	//			//if count < 0 {
	//			//	count++
	//			//	header, err = ws.ReadHeader(conn.netConn)
	//			//	if err != nil {
	//			//		glog.Errorln(err)
	//			//		continue
	//			//	}
	//			//	if header.Length > 0 {
	//			//		goto Loop
	//			//	}
	//			//}
	//			p.reader <- conn
	//			fmt.Println("push", conn)
	//			//p.again.PushBack(conn)
	//		}
	//	}()
	//}
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
