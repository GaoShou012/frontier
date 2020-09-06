package frontier

import (
	"github.com/GaoShou012/frontier/netpoll"
	"github.com/gobwas/ws"
	"net"
	"sync"
	"time"
)

const (
	connStateIsNothing = iota
	connStateIsWorking
	connStateWasClosed
)



type wsReader struct {
	state             int
	isClose           bool
	isReading         bool
	rwMutex           sync.RWMutex
	bufferSiz         int64
	buffer            []byte
	bufferN           int64
	header            *ws.Header
	headerExtraLength int64
	lastCheck         bool
	checkTimes        int
	lastReadTime      time.Time
}

func (r *wsReader) Release() {
	r.rwMutex.Lock()
	defer r.rwMutex.Unlock()
	r.isReading = false
}

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
	id     int
	state  int
	broken bool

	netConn        net.Conn
	context        interface{}
	connectionTime time.Time

	deadline int64
	desc     *netpoll.Desc
	frontier *Frontier
	protocol Protocol

	temp []byte

	wsReader *wsReader
}

func (c *conn) Init() {
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
