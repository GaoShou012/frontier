// Copyright 2015 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build ignore

package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/websocket"
)

var addr = flag.String("addr", "192.168.56.102:1234", "http service address")
var times = flag.Int("times", 1, "loop times")

func main() {
	flag.Parse()
	log.SetFlags(0)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	u := url.URL{Scheme: "ws", Host: *addr, Path: "/"}
	log.Printf("connecting to %s", u.String())

	fmt.Println(u.String())
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	//defer c.Close()

	done := make(chan struct{})
	messageCount := 0

	go func() {
		defer close(done)
		defer func() {
			fmt.Println("messageCount", messageCount)
		}()
		c.SetPongHandler(func(appData string) error {
			fmt.Println("pong", appData)
			return nil
		})
		for {
			op, message, err := c.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				return
			}
			log.Printf("op: %v\n", op)
			log.Printf("recv: %s\n", message)
			fmt.Println(messageCount)
		}
	}()
	time.Sleep(time.Second)
	fmt.Println("write ping message")
	//c.WriteMessage(websocket.TextMessage, []byte("123123"))
	//c.WriteMessage(websocket.PingMessage, nil)
	c.WriteControl(websocket.PingMessage, []byte("333"), time.Now().Add(time.Millisecond*10))
	time.Sleep(time.Second * 10)
	return
	ticker := time.NewTicker(time.Millisecond * 1)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case t := <-ticker.C:
			//err := c.WriteMessage(websocket.TextMessage, []byte(t.String()))
			err := c.WriteMessage(websocket.TextMessage, []byte("ping1"))
			if err != nil {
				log.Println("write:", err)
				return
			}
			//time.Sleep(time.Second)
			//panic("exit")
			messageCount++
			if messageCount >= *times {
				fmt.Println("waiting")
				fmt.Println(t)
				time.Sleep(time.Second * 10)
				return
			}
		case <-interrupt:
			log.Println("interrupt")
			fmt.Println(messageCount)
			// Cleanly close the connection by sending a close message and then
			// waiting (with timeout) for the server to close the connection.
			err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("write close:", err)
				return
			}
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return
		}
	}
}
