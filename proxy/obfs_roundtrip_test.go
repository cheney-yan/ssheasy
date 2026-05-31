package main

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func roundtrip(t *testing.T, psk string) {
	c, s := net.Pipe()
	defer c.Close()
	defer s.Close()
	client := newObfConn(c, false, psk)
	server := newObfConn(s, true, psk)

	msg := []byte(`{"Host":"example.com","Port":22}SSH-2.0-OpenSSH_9.6` + "\r\n")
	errc := make(chan error, 1)
	go func() {
		_, err := client.Write(msg)
		errc <- err
	}()

	got := make([]byte, len(msg))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("client write: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("payload mismatch: got %q want %q", got, msg)
	}

	// reply direction
	reply := []byte(`{"status":"ok"}`)
	go func() { server.Write(reply) }()
	rb := make([]byte, len(reply))
	if _, err := io.ReadFull(client, rb); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if !bytes.Equal(rb, reply) {
		t.Fatalf("reply mismatch: got %q want %q", rb, reply)
	}
}

func TestObfRoundtripPSK(t *testing.T)  { withTimeout(t, func() { roundtrip(t, "secret-key") }) }
func TestObfRoundtripECDH(t *testing.T) { withTimeout(t, func() { roundtrip(t, "") }) }

func withTimeout(t *testing.T, f func()) {
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}
