package frontier

import (
	"encoding/binary"
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

const (
	wsConnStateIsFreeing = iota
	wsConnStateIsBusying
	wsConnStateWasClosed
)

const (
	wsReaderStateInit = iota
	wsReaderStateToReadHeader
	wsReaderStateToParseHeaderExtraLength
	wsReaderStateToReadHeaderExtra
	wsReaderStateToParseHeader
	wsReaderStateToParsePayload
	wsReaderStateToReadPayload
)

type WsConn struct {
	conn *conn

	state      int8
	stateMutex sync.Mutex

	header *ws.Header

	readerState        int8
	readerBuffer       []byte
	readerBufferN      int64
	readerProcessCount int64
	readerProcessTime  time.Time

	frameLength        int64
	frameHeaderLength  int64
	framePayloadLength int64

	// 数据包
	dataPack []byte
}

func (c *WsConn) readerRelease() {
	c.stateMutex.Lock()
	defer c.stateMutex.Unlock()
	if c.state == wsConnStateIsBusying {
		c.state = wsConnStateIsFreeing
	}
}

type ProtocolWs struct {
	Frontier      *Frontier
	DynamicParams *DynamicParams
	Handler       *Handler

	connectionsMutex sync.Mutex
	connections      map[int]*WsConn
	reader           chan *WsConn
	delay1m          chan *WsConn

	MessageCount int
	onMessage    chan *Message
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

func ParseHeaderExtra(bts []byte) (extra int64, err error) {
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

func (p *ProtocolWs) OnMessage() chan *Message {
	return p.onMessage
}

func (p *ProtocolWs) close(wsConn *WsConn) {
	wsConn.state = wsConnStateWasClosed
	p.Frontier.eventPush(ConnEventTypeDelete, wsConn.conn)
}

func (p *ProtocolWs) newBuffer() []byte {
	size := p.DynamicParams.ReaderBufferSize
	buffer := make([]byte, size)
	return buffer
}

func (p *ProtocolWs) OnInit(frontier *Frontier, params *DynamicParams, handler *Handler) {
	p.Frontier = frontier
	p.DynamicParams = params
	p.Handler = handler
	p.connections = make(map[int]*WsConn)

	go func() {
		p.delay1m = make(chan *WsConn, 1000000)
		for {
			wsConn := <-p.delay1m
			now := time.Now()
			sub := now.Sub(wsConn.readerProcessTime)
			if sub < time.Millisecond {
				time.Sleep(sub)
			}
			p.reader <- wsConn
		}
	}()

	p.reader = make(chan *WsConn, p.Frontier.MaxConnections+10000)
	p.onMessage = make(chan *Message, 100000)

	readDeadLine := time.Microsecond * 10

	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				wsConn := <-p.reader
				netConn := wsConn.conn.netConn
			Loop:
				switch wsConn.readerState {
				case wsReaderStateInit:
					if len(wsConn.readerBuffer) < 6 {
						buf := p.newBuffer()
						if wsConn.readerBufferN > 0 {
							copy(buf, wsConn.readerBuffer)
						}
						wsConn.readerBuffer = buf
					}

					wsConn.readerState = wsReaderStateToReadHeader
					wsConn.readerProcessCount = 0
					goto Loop
				case wsReaderStateToReadHeader:
					if err := netConn.SetReadDeadline(time.Now().Add(readDeadLine)); err != nil {
						glog.Errorln(err)
						continue
					}
					buf := wsConn.readerBuffer[wsConn.readerBufferN:]
					n, err := netConn.Read(buf)
					if err != nil {
						if err == io.EOF {
							p.close(wsConn)
							continue
						}
					}
					wsConn.readerBufferN += int64(n)

					if wsConn.readerBufferN == 0 {
						if wsConn.readerProcessCount > 300 {
							wsConn.readerProcessCount = 0
							wsConn.readerRelease()
						} else {
							wsConn.readerProcessCount++
							wsConn.readerProcessTime = time.Now()
							p.delay1m <- wsConn
						}
						continue
					}

					// To parse header extra
					if wsConn.readerBufferN >= 2 {
						wsConn.readerState = wsReaderStateToParseHeaderExtraLength
						goto Loop
					}

					p.reader <- wsConn
					break
				case wsReaderStateToParseHeaderExtraLength:
					extra, err := ParseHeaderExtra(wsConn.readerBuffer)
					if err != nil {
						glog.Errorln(err)
						os.Exit(1)
					}
					wsConn.frameHeaderLength = 2 + extra

					// To parse header
					if wsConn.readerBufferN >= wsConn.frameHeaderLength {
						wsConn.readerState = wsReaderStateToParseHeader
						goto Loop
					}

					// To check the space that is there enough for frame's header
					if int64(len(wsConn.readerBuffer)) < wsConn.frameHeaderLength {
						buf := p.newBuffer()
						copy(buf, wsConn.readerBuffer)
						wsConn.readerBuffer = buf
					}

					// To read header extra data
					wsConn.readerState = wsReaderStateToReadHeaderExtra
					goto Loop
				case wsReaderStateToReadHeaderExtra:
					if err := netConn.SetReadDeadline(time.Now().Add(readDeadLine)); err != nil {
						glog.Errorln(err)
						continue
					}
					buf := wsConn.readerBuffer[wsConn.readerBufferN:]
					n, err := netConn.Read(buf)
					if err != nil {
						if err == io.EOF {
							p.close(wsConn)
							continue
						}
					}
					wsConn.readerBufferN += int64(n)

					// To parse header
					if wsConn.readerBufferN >= wsConn.frameHeaderLength {
						wsConn.readerState = wsReaderStateToParseHeader
						goto Loop
					}

					p.reader <- wsConn
					break
				case wsReaderStateToParseHeader:
					header, err := ParseHeader(wsConn.readerBuffer)
					if err != nil {
						glog.Errorln(err)
						os.Exit(1)
					}
					wsConn.header = &header
					wsConn.framePayloadLength = header.Length
					wsConn.frameLength = wsConn.frameHeaderLength + wsConn.framePayloadLength

					wsConn.readerBuffer = wsConn.readerBuffer[wsConn.frameHeaderLength:]
					wsConn.readerBufferN -= wsConn.frameHeaderLength

					// To parse payload
					if wsConn.readerBufferN >= wsConn.framePayloadLength {
						wsConn.readerState = wsReaderStateToParsePayload
						goto Loop
					}

					// To check the space is there enough for payload data
					if int64(len(wsConn.readerBuffer)) < wsConn.framePayloadLength {
						buf := p.newBuffer()
						if int64(len(buf)) < wsConn.framePayloadLength {
							panic("buf size error")
						}
						wsConn.readerBuffer = buf
					}

					// To read payload
					wsConn.readerState = wsReaderStateToReadPayload
					p.reader <- wsConn
					break
				case wsReaderStateToReadPayload:
					if err := netConn.SetReadDeadline(time.Now().Add(readDeadLine)); err != nil {
						glog.Errorln(err)
						continue
					}
					buf := wsConn.readerBuffer[wsConn.readerBufferN:]
					n, err := netConn.Read(buf)
					if err != nil {
						if err == io.EOF {
							p.close(wsConn)
							continue
						}
					}
					wsConn.readerBufferN += int64(n)

					if wsConn.readerBufferN >= wsConn.framePayloadLength {
						wsConn.readerState = wsReaderStateToParsePayload
						goto Loop
					}
					p.reader <- wsConn
					break
				case wsReaderStateToParsePayload:
					header := wsConn.header
					if wsConn.framePayloadLength > 0 {
						payload := wsConn.readerBuffer[:wsConn.framePayloadLength]
						if header.Masked {
							ws.Cipher(payload, header.Mask, 0)
						}
						wsConn.dataPack = append(wsConn.dataPack, payload...)
					}
					if header.Fin {
						message := &Message{
							Conn:    wsConn.conn,
							OpCode:  header.OpCode,
							Payload: wsConn.dataPack,
						}
						p.onMessage <- message
						wsConn.dataPack = nil
					}

					wsConn.readerBuffer = wsConn.readerBuffer[wsConn.framePayloadLength:]
					wsConn.readerBufferN -= wsConn.framePayloadLength

					switch {
					case wsConn.readerBufferN == 0:
						wsConn.readerState = wsReaderStateInit
						p.delay1m <- wsConn
						break
					case wsConn.readerBufferN == 1:
						wsConn.readerState = wsReaderStateInit
						goto Loop
					case wsConn.readerBufferN >= 2:
						wsConn.readerState = wsReaderStateToParseHeaderExtraLength
						goto Loop
					default:
						panic("Unknown Reader Buffer N" + fmt.Sprintf("%d", wsConn.readerBufferN))
					}
					break
				}
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
func (p *ProtocolWs) Reader(conn *conn) {
	p.connectionsMutex.Lock()
	wsConn, ok := p.connections[conn.GetId()]
	if !ok {
		wsConn = &WsConn{conn: conn}
		p.connections[conn.GetId()] = wsConn
	}
	p.connectionsMutex.Unlock()

	if wsConn.state != wsConnStateIsFreeing {
		return
	}

	ok = false
	wsConn.stateMutex.Lock()
	if wsConn.state == wsConnStateIsFreeing {
		wsConn.state = wsConnStateIsBusying
		ok = true
	}
	wsConn.stateMutex.Unlock()
	if !ok {
		return
	}

	p.reader <- wsConn
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
