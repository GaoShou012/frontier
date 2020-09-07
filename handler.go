package frontier

import "github.com/gobwas/ws"

type Handler struct {
	OnRequest       func(conn Conn, uri []byte) error
	OnHost          func(conn Conn, host []byte) error
	OnHeader        func(conn Conn, key, value []byte) error
	OnBeforeUpgrade func(conn Conn) (header ws.HandshakeHeader, err error)
	OnOpen          func(conn Conn) error
	OnMessage       func(conn Conn, message []byte)
	OnClose         func(conn Conn)
}
