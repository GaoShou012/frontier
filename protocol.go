package frontier

import (
	"net"
)

type Protocol interface {
	OnInit(frontier *Frontier, params *DynamicParams, handler *Handler)
	OnAccept(conn Conn) error
	OnMessage() chan *Message
	OnClose(conn *conn)
	Writer(netConn net.Conn, message []byte) error
	Reader(conn *conn)
}
