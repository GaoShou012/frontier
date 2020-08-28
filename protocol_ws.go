package frontier

import (
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/golang/glog"
	"io/ioutil"
	"net"
	"time"
)

var _ Protocol = &ProtocolWs{}

type ProtocolWs struct {
	writerBufferSize     int
	readerBufferSize     int
	writerMessageTimeout *time.Duration
	readerMessageTimeout *time.Duration
	logLevel             *int
}

func (p *ProtocolWs) OnInit(writerBufferSize int, readerBufferSize int, writerTimeout *time.Duration, readerTimeout *time.Duration, logLevel *int) {
	p.writerBufferSize = writerBufferSize
	p.readerBufferSize = readerBufferSize
	p.writerMessageTimeout = writerTimeout
	p.readerMessageTimeout = readerTimeout
	p.logLevel = logLevel
}

func (p *ProtocolWs) OnAccept(netConn net.Conn) error {
	var err error
	if *p.logLevel >= LogLevelInfo {
		_, err = ws.Upgrader{
			ReadBufferSize:  p.readerBufferSize,
			WriteBufferSize: p.writerBufferSize,
			Protocol:        nil,
			ProtocolCustom:  nil,
			Extension:       nil,
			ExtensionCustom: nil,
			Header:          nil,
			OnRequest: func(uri []byte) error {
				glog.Infoln("conn uri:", string(uri))
				return nil
			},
			OnHost: func(host []byte) error {
				glog.Infoln("conn host:", string(host))
				return nil
			},
			OnHeader: func(key, value []byte) error {
				glog.Infoln("conn on header:", key, string(value))
				return nil
			},
			OnBeforeUpgrade: func() (header ws.HandshakeHeader, err error) {
				glog.Infoln("conn before upgrade header:", header)
				return
			},
		}.Upgrade(netConn)
	} else {
		_, err = ws.Upgrader{
			ReadBufferSize:  0,
			WriteBufferSize: 0,
			Protocol:        nil,
			ProtocolCustom:  nil,
			Extension:       nil,
			ExtensionCustom: nil,
			Header:          nil,
			OnRequest:       nil,
			OnHost:          nil,
			OnHeader:        nil,
			OnBeforeUpgrade: nil,
		}.Upgrade(netConn)
	}
	return err
}
func (p *ProtocolWs) OnClose(netConn net.Conn) error {
	w := wsutil.NewWriter(netConn, ws.StateServerSide, ws.OpClose)
	if _, err := w.Write([]byte("close")); err != nil {
		return err
	}
	return w.Flush()
}

func (p *ProtocolWs) Writer(netConn net.Conn, message []byte) error {
	if err := netConn.SetWriteDeadline(time.Now().Add(*p.writerMessageTimeout)); err != nil {
		return err
	}
	w := wsutil.NewWriter(netConn, ws.StateServerSide, ws.OpText)
	if _, err := w.Write(message); err != nil {
		return err
	}
	return w.Flush()
}

func (p *ProtocolWs) Reader(netConn net.Conn) (message []byte, err error) {
	h, r, err := wsutil.NextReader(netConn, ws.StateServerSide)
	if err != nil {
		return nil, err
	}

	if h.OpCode.IsControl() {
		if h.OpCode == ws.OpPing {
			err := netConn.SetWriteDeadline(time.Now().Add(*p.readerMessageTimeout))
			if err != nil {
				return nil, err
			}
			w := wsutil.NewControlWriter(netConn, ws.StateServerSide, ws.OpPong)
			if _, e := w.Write(nil); e != nil {
				err = e
			}
			if e := w.Flush(); e != nil {
				err = e
			}
			return nil, nil
		}
		return nil, wsutil.ControlFrameHandler(netConn, ws.StateServerSide)(h, r)
	}

	data, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return data, nil
}
