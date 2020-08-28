package frontier

import (
	"net"
	"time"
)

type Protocol interface {
	OnInit(
		writerBufferSize int,
		readerBufferSize int,
		writerTimeout *time.Duration,
		readerTimeout *time.Duration,
		logLevel *int,
	)
	OnAccept(netConn net.Conn) error
	OnClose(netCOnn net.Conn) error
	Writer(netConn net.Conn, message []byte) error
	Reader(netConn net.Conn) (message []byte, err error)
}
