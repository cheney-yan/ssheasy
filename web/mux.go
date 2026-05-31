package main

// Stream multiplexing over a single obfuscated websocket. Every proxied
// connection (the SSH transport AND each reverse-proxy target dial) becomes a
// yamux stream on one shared, long-lived /pm websocket instead of its own
// websocket + obfuscation handshake. This amortises the per-connection setup
// cost and, because it's one persistent encrypted stream rather than many
// short connections, makes the traffic harder to fingerprint.
//
// con() tries this first and falls back to a dedicated /p connection if the
// mux session can't be established (e.g. an older proxy without /pm).

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall/js"

	"github.com/hashicorp/yamux"
	"github.com/hullarb/dom/net/ws"
)

var (
	muxMu   sync.Mutex
	muxSess *yamux.Session
)

// dialObfWS opens an obfuscation-wrapped websocket to a proxy path (no
// per-connection header — the mux header is sent per stream instead).
func dialObfWS(path string) (net.Conn, error) {
	l := js.Global().Get("window").Get("location")
	proto := "wss://"
	if l.Get("protocol").String() == "http:" {
		proto = "ws://"
	}
	conn, err := ws.Dial(proto + l.Get("host").String() + path)
	if err != nil {
		return nil, fmt.Errorf("failed to open ws: %v", err)
	}
	if obfsClientEnabled() {
		return newObfConn(conn, false, obfsClientKey()), nil
	}
	return conn, nil
}

// muxSession returns a live multiplexed session to /pm, (re)establishing it on
// first use or after the previous one died.
func muxSession() (*yamux.Session, error) {
	muxMu.Lock()
	defer muxMu.Unlock()
	if muxSess != nil && !muxSess.IsClosed() {
		return muxSess, nil
	}
	conn, err := dialObfWS("/pm")
	if err != nil {
		return nil, err
	}
	sess, err := yamux.Client(conn, yamux.DefaultConfig())
	if err != nil {
		conn.Close()
		return nil, err
	}
	muxSess = sess
	return sess, nil
}

// dialMuxed opens a stream to host:port over the shared session, retrying once
// with a fresh session if the current one has gone away.
func dialMuxed(host string, port int) (net.Conn, error) {
	st, err := openMuxStream(host, port)
	if err != nil {
		muxMu.Lock()
		if muxSess != nil {
			muxSess.Close()
			muxSess = nil
		}
		muxMu.Unlock()
		st, err = openMuxStream(host, port)
	}
	return st, err
}

func openMuxStream(host string, port int) (net.Conn, error) {
	sess, err := muxSession()
	if err != nil {
		return nil, err
	}
	st, err := sess.Open()
	if err != nil {
		return nil, err
	}
	if err := writeMuxHeader(st, host, port); err != nil {
		st.Close()
		return nil, err
	}
	var status [1]byte
	if _, err := io.ReadFull(st, status[:]); err != nil {
		st.Close()
		return nil, err
	}
	if status[0] != 1 {
		st.Close()
		return nil, fmt.Errorf("mux: proxy failed to dial %s:%d", host, port)
	}
	return st, nil
}

// writeMuxHeader sends a length-prefixed {Host,Port} JSON so the proxy reads
// exactly the header and nothing of the relayed payload.
func writeMuxHeader(w io.Writer, host string, port int) error {
	hdr, err := json.Marshal(struct {
		Host string
		Port int
	}{host, port})
	if err != nil {
		return err
	}
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(hdr)))
	_, err = w.Write(append(lb[:], hdr...))
	return err
}
