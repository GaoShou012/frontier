package frontier

type Handler interface {
	OnOpen(conn Conn) bool
	OnMessage(conn Conn, message []byte)
	OnClose(conn Conn)
}
