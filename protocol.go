package frontier

import (
	"net"
)

type Protocol interface {
	OnInit(frontier *Frontier, params *DynamicParams, handler *Handler)
	OnAccept(conn Conn) error
	OnClose(conn *conn)
	Writer(netConn net.Conn, message []byte) error
	Reader(conn *conn)
}
