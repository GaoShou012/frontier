package frontier

import (
	"github.com/GaoShou012/frontier/netpoll"
	"net"
	"sync"
	"time"
)

const (
	connStateIsNothing = iota
	connStateIsWorking
	connStateIsClosing
	connStateWasClosed
)

type Conn interface {
	Frontier() *Frontier
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
	id           int
	state        int
	stateRWMutex sync.RWMutex
	broken       bool

	netConn        net.Conn
	context        interface{}
	connectionTime time.Time

	deadline int64
	desc     *netpoll.Desc
	frontier *Frontier
	protocol Protocol
}

func (c *conn) Init() {
}

func (c *conn) IsWorking() bool {
	c.stateRWMutex.RLock()
	defer c.stateRWMutex.RUnlock()
	if c.state == connStateIsWorking {
		return true
	} else {
		return false
	}
}
func (c *conn) SetState(state int) {
	c.stateRWMutex.Lock()
	defer c.stateRWMutex.Unlock()
	c.state = state
}

func (c *conn) Frontier() *Frontier {
	return c.frontier
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
