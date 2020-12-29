package main

import (
	"github.com/zhyoulun/kcp-go/src/listener"
	"github.com/zhyoulun/kcp-go/src/session"
	"io"
	"log"
	"time"
)

func main() {
	//key := pbkdf2.Key([]byte("demo pass"), []byte("demo salt"), 1024, 32, sha1.New)
	//block, _ := kcp.NewAESBlockCrypt(key)
	//if listener, err := kcp.ListenWithOptions("127.0.0.1:12345", block, 10, 3); err == nil {
	if l, err := listener.ListenWithOptions("127.0.0.1:12345"); err == nil {
		// spin-up the client
		go client()
		for {
			s, err := l.AcceptKCP()
			if err != nil {
				log.Fatal(err)
			}
			go handleEcho(s)
		}
	} else {
		log.Fatal(err)
	}
}

// handleEcho send back everything it received
func handleEcho(conn *session.UDPSession) {
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			log.Println(err)
			return
		}

		n, err = conn.Write(buf[:n])
		if err != nil {
			log.Println(err)
			return
		}
	}
}

func client() {
	//key := pbkdf2.Key([]byte("demo pass"), []byte("demo salt"), 1024, 32, sha1.New)
	//block, _ := kcp.NewAESBlockCrypt(key)

	// wait for server to become ready
	time.Sleep(time.Second)

	// dial to the echo server
	//if sess, err := kcp.DialWithOptions("127.0.0.1:12345", block, 10, 3); err == nil {
	var sess *session.UDPSession
	var err error
	if sess, err = session.DialWithOptions("127.0.0.1:12345"); err != nil {
		log.Fatal(err)
	}

	for {
		data := time.Now().String()
		buf := make([]byte, len(data))
		log.Println("sent:", data)
		if _, err := sess.Write([]byte(data)); err != nil {
			log.Fatal(err)
		}
		// read back the data
		if _, err := io.ReadFull(sess, buf); err != nil {
			log.Fatal(err)
		}
		log.Println("recv:", string(buf))
		time.Sleep(time.Second)
	}
}
