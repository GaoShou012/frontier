package main

import (
	"github.com/GaoShou012/frontier"
	"github.com/GaoShou012/tools/logger"
	"github.com/golang/glog"
	uuid "github.com/satori/go.uuid"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	id := uuid.NewV4().String()
	addr := ":1234"
	maxConnections := 1000000
	dynamicParams := &frontier.DynamicParams{
		LogLevel:         logger.LogAll,
		HeartbeatTimeout: 90,
		WriterBufferSize: 1024,
		ReaderBufferSize: 1024,
		WriterTimeout:    time.Millisecond * 40,
		ReaderTimeout:    time.Millisecond * 40,
	}
	handler := &frontier.Handler{
		OnRequest:       nil,
		OnHost:          nil,
		OnHeader:        nil,
		OnBeforeUpgrade: nil,
		OnOpen: func(conn frontier.Conn) error {
			return nil
		},
		OnMessage: func(conn frontier.Conn, message []byte) {
		},
		OnClose: func(conn frontier.Conn) {
		},
	}

	f := &frontier.Frontier{
		Id:             id,
		Addr:           addr,
		MaxConnections: maxConnections,
		DynamicParams:  dynamicParams,
		Protocol:       &frontier.ProtocolWs{},
		Handler:        handler,
	}
	if err := f.Init(); err != nil {
		panic(err)
	}
	if err := f.Start(); err != nil {
		panic(err)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT)
	for {
		switch s := <-c; s {
		case syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT:
			glog.Infof("got signal %s; stop server", s)
		case syscall.SIGHUP:
			glog.Infof("got signal %s; go to deamon", s)
			continue
		}
		if err := f.Stop(); err != nil {
			glog.Errorf("stop server error: %v", err)
		}
		break
	}
}
