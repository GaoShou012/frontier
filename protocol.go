package frontier

import (
	"net"
)

type Protocol interface {
	OnInit(params *DynamicParams, handler *Handler)
	OnAccept(conn Conn) error
	OnClose(netConn net.Conn) error
	Writer(netConn net.Conn, message []byte) error
	Reader(conn *conn) (message []byte, err error)
}
