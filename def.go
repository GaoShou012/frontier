package frontier

import "time"

type DynamicParams struct {
	// 日志级别
	LogLevel int
	// 心跳超时
	HeartbeatTimeout int64
	// 写缓存大小，读缓存大小
	WriterBufferSize int
	ReaderBufferSize int
	// 写超时，读超时
	WriterTimeout time.Duration
	ReaderTimeout time.Duration
}