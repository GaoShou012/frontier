package frontier

import "github.com/gobwas/ws"

type Message struct {
	Conn    *conn
	OpCode  ws.OpCode
	Payload []byte
}
