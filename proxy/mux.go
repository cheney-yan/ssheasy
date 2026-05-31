package main

// Server side of the stream multiplexer (see web/mux.go). A /pm websocket
// carries a yamux session; every stream is one proxied connection, with a
// length-prefixed {Host,Port} header followed by raw relayed bytes.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/net/websocket"
)

func handleMuxWss(wsconn *websocket.Conn) {
	wsconn.PayloadType = websocket.BinaryFrame
	blocked, ips := getIPAdress(wsconn)
	if blocked {
		log.Printf("mux: blocking ip %v", ips)
		return
	}
	src := ips[0]

	var tr net.Conn = wsconn
	if obfsEnabled() {
		tr = newObfConn(wsconn, true, obfsPSK())
	}
	sess, err := yamux.Server(tr, yamux.DefaultConfig())
	if err != nil {
		log.Printf("mux: server session failed: %v", err)
		return
	}
	defer sess.Close()
	for {
		st, err := sess.Accept()
		if err != nil {
			return // session closed
		}
		go handleMuxStream(st, src)
	}
}

func handleMuxStream(st net.Conn, src string) {
	defer st.Close()
	host, port, err := readMuxHeader(st)
	if err != nil {
		log.Printf("mux: bad header from %s: %v", src, err)
		return
	}
	if !isAllowedTarger(host) {
		log.Printf("mux: target %s not allowed", host)
		st.Write([]byte{0})
		return
	}
	target, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 30*time.Second)
	if err != nil {
		log.Printf("mux: dial %s:%d failed: %v", host, port, err)
		writeAuditLog(src, host, port, "mux dial failed")
		st.Write([]byte{0})
		return
	}
	defer target.Close()
	if _, err := st.Write([]byte{1}); err != nil {
		return
	}
	writeAuditLog(src, host, port, "mux stream established")

	done := make(chan struct{}, 2)
	go func() { io.Copy(target, st); done <- struct{}{} }()
	go func() { io.Copy(st, target); done <- struct{}{} }()
	<-done // tear down once either direction closes
}

func readMuxHeader(r io.Reader) (string, int, error) {
	var lb [2]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return "", 0, err
	}
	n := binary.BigEndian.Uint16(lb[:])
	if n == 0 || n > 4096 {
		return "", 0, fmt.Errorf("mux: implausible header length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", 0, err
	}
	var h struct {
		Host string
		Port int
	}
	if err := json.Unmarshal(buf, &h); err != nil {
		return "", 0, err
	}
	return h.Host, h.Port, nil
}
