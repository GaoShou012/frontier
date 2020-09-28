package main

import (
	"encoding/json"
	"fmt"
	"github.com/GaoShou012/frontier"
	"github.com/GaoShou012/tools/logger"
	"github.com/gobwas/ws"
	"github.com/golang/glog"
	uuid "github.com/satori/go.uuid"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Message struct {
	Type        string
	Content     string
	ContentType string
	ClientMsgId int
}

func main() {
	messageCounter := 0
	go func() {
		ticker := time.NewTicker(time.Second)
		for {
			<-ticker.C
			fmt.Println(messageCounter)
		}
	}()

	id := uuid.NewV4().String()
	addr := ":1234"
	maxConnections := 1000000
	dynamicParams := &frontier.DynamicParams{
		LogLevel:         logger.LogAll,
		HeartbeatTimeout: 10,
		WriterBufferSize: 1024 * 4,
		ReaderBufferSize: 1024 * 4,
		WriterTimeout:    time.Millisecond * 40,
		ReaderTimeout:    time.Microsecond * 10,
	}
	handler := &frontier.Handler{
		OnRequest: func(conn frontier.Conn, uri []byte) error {
			return nil
		},
		OnHost:          nil,
		OnHeader:        nil,
		OnBeforeUpgrade: nil,
		OnOpen: func(conn frontier.Conn) error {
			return nil
		},
		OnMessage: func(conn frontier.Conn, message []byte) {
			//fmt.Println("count", len(message))
			msg := &Message{}
			err := json.Unmarshal(message, msg)
			if err != nil {
				fmt.Println(message)
				glog.Errorln(string(message))
				glog.Errorln(err)
				os.Exit(1)
			}
			messageCounter++
			conn.Sender([]byte("ok"))
		},
		OnClose: func(conn frontier.Conn) {
		},
	}

	f := &frontier.Frontier{
		Id:              id,
		Addr:            addr,
		MaxConnections:  maxConnections,
		DynamicParams:   dynamicParams,
		Protocol:        &frontier.ProtocolWs{MessageType: ws.OpText},
		Handler:         handler,
		SenderParallel:  100,
		SenderCacheSize: 10000,
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
