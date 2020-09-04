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
	connStateWasClosed
)

const (
	wsStateEdge = iota
	wsStateContinuation = iota
	wsStateReadingHeader
	wsStateReadingMask

	connReaderStateContinuation = iota
	connReaderStateHasTail
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

	isReading      bool
	lastReaderTime time.Time
	rwMutex        sync.RWMutex
	lastCheck      bool
	readerState    int

	readerBufN int
	readerBuf  []byte

	readerBufPingOffset int
	readerBufPongOffset int
	readerBufFlag       bool
	readerBufPing       []byte
	readerBufPong       []byte

	*wsProtocol
}

func (c *conn) Init() {
}

func (c *conn) GetBuf() (prev []byte, next []byte) {
	if c.readerBufFlag {
		c.readerBufFlag = false
		return c.readerBufPing, c.readerBufPong
	} else {
		c.readerBufFlag = true
		return c.readerBufPong, c.readerBufPing
	}
}
func (c *conn) SetBufOffset() {

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
