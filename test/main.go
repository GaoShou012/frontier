package main

import (
	"fmt"
	"github.com/GaoShou012/frontier"
	"github.com/GaoShou012/tools/logger"
	"github.com/gobwas/ws"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	uuid "github.com/satori/go.uuid"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var MessageOnQueue prometheus.Gauge

type room struct {
	connections map[int]frontier.Conn
	broadcast   chan []byte
	joinEvent   chan frontier.Conn
	leaveEvent  chan frontier.Conn
}

func (r *room) init() {
	r.connections = make(map[int]frontier.Conn)
	r.broadcast = make(chan []byte, 100000)
	r.joinEvent = make(chan frontier.Conn, 100000)
	r.leaveEvent = make(chan frontier.Conn, 100000)

	go func() {
		for {
			select {
			case message := <-r.broadcast:
				for _, conn := range r.connections {
					conn.Sender(message)
				}
				MessageOnQueue.Dec()
			case conn := <-r.joinEvent:
				r.connections[conn.GetId()] = conn
			case conn := <-r.leaveEvent:
				delete(r.connections, conn.GetId())
			}
		}
	}()
}

func Prometheus(addr string) {
	if addr == "" {
		return
	}
	http.Handle("/metrics", promhttp.Handler())
	log.Fatalln(http.ListenAndServe(addr, nil))
}

func main() {
	r := &room{}
	r.init()

	go Prometheus(":9090")

	MessageOnQueue = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "room",
		Name:      "message_on_queue",
		Help:      "",
	})
	prometheus.MustRegister(MessageOnQueue)

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
		HeartbeatTimeout: 90,
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
			r.joinEvent <- conn
			return nil
		},
		OnMessage: func(conn frontier.Conn, message []byte) {
			MessageOnQueue.Inc()
			r.broadcast <- message
		},
		OnClose: func(conn frontier.Conn) {
			r.leaveEvent <- conn
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
