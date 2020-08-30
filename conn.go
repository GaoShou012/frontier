package frontier

import (
	"github.com/GaoShou012/frontier/netpoll"
	"net"
	"time"
)

const (
	connStateIsNothing = iota
	connStateIsWorking
	connStateWasClosed
)

type Conn interface {
	GetId() int
	GetState() int
	GetConnectionTime() time.Time
	SetContext(ctx interface{})
	GetContext() interface{}
	NetConn() net.Conn
	Sender(message []byte)
}

var _ Conn = &conn{}

type conn struct {
	frontier *Frontier
	protocol Protocol

	id    int
	state int

	netConn        net.Conn
	context        interface{}
	connectionTime time.Time

	deadline int64
	desc     *netpoll.Desc
}

func (c *conn) GetId() int {
	return c.id
}
func (c *conn) GetState() int {
	return c.state
}
func (c *conn) GetConnectionTime() time.Time {
	return c.connectionTime
}
func (c *conn) SetContext(ctx interface{}) {
	c.context = ctx
}
func (c *conn) GetContext() interface{} {
	return c.context
}
func (c *conn) NetConn() net.Conn {
	return c.netConn
}
func (c *conn) Sender(message []byte) {
	c.frontier.sender.push(c, message)
}
