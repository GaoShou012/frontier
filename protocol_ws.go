package frontier

import (
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"io/ioutil"
	"net"
	"time"
)

var _ Protocol = &ProtocolWs{}

type ProtocolWs struct {
	DynamicParams *DynamicParams
	Handler       *Handler
}

func (p *ProtocolWs) OnInit(params *DynamicParams, handler *Handler) {
	p.DynamicParams = params
	p.Handler = handler
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

func (p *ProtocolWs) Reader(netConn net.Conn) (message []byte, err error) {
	h, r, err := wsutil.NextReader(netConn, ws.StateServerSide)
	if err != nil {
		return
	}

	if h.OpCode.IsControl() {
		if h.OpCode == ws.OpPing {
			err = netConn.SetWriteDeadline(time.Now().Add(p.DynamicParams.ReaderTimeout))
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
