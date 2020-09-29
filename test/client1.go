package main

import (
	"fmt"
	"github.com/golang/glog"
	"github.com/gorilla/websocket"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	conn, _, err := websocket.DefaultDialer.Dial("ws://192.168.56.101:1234?mid=1598252141521-0&token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJyb29tQ29kZSI6Ijg4OWQiLCJ0ZW5hbnRDb2RlIjoieGdjcCIsInVzZXJJZCI6MzIxLCJ1c2VyTmFtZSI6ImFiY2NjIiwidXNlclRodW1iIjoiZGRkIiwidXNlclR5cGUiOiJtYW5hZ2VyIn0.QNSZ3_9sn1VK4ZANAMRoOQMvVnCZLfb2_quF9-dcO7E", nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer conn.Close()

	done := make(chan struct{})
	messageCount := 0

	go func() {
		defer close(done)
		defer func() {
			fmt.Println("messageCount", messageCount)
		}()
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				glog.Errorln("read:", err)
				return
			}
			fmt.Println(message)
		}
	}()

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("123")); err != nil {
		glog.Errorln(err)
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
		break
	}
}
