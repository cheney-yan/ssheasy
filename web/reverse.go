package main

// Reverse proxy (SSH remote port forwarding, like `ssh -R`): open a listening
// port on the remote server and tunnel every connection it accepts back through
// the SSH connection to a target reachable from here (the browser's host),
// which we reach via the same websocket proxy used for outbound connections.
//
// NOTE on "Stop": we deliberately never call the ssh listener's Close(). In
// x/crypto/ssh that issues a cancel-tcpip-forward global request over a shared
// request/response channel, and on this transport it can panic and desync the
// channel — after which the next tcpip-forward (a re-add) blocks forever. So
// Stop just gates the forward (drops new connections) and keeps the remote
// listener open; re-adding the same port re-enables it instead of re-listening.
// The remote port is released when the SSH session ends.

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"syscall/js"
)

type revFwd struct {
	l       net.Listener
	mu      sync.Mutex
	host    string
	port    int
	stopped bool
}

var (
	revMu   sync.Mutex
	revFwds = map[string]*revFwd{}
)

// reverseForward(remotePort int, bindAll bool, targetHost string, targetPort int, cb func)
// opens remotePort on the SSH server (loopback, or all interfaces if bindAll —
// the server must permit it via GatewayPorts) and forwards to targetHost:targetPort.
// cb is called once with {ok, key, info} or {ok:false, error}.
func reverseForward(this js.Value, args []js.Value) interface{} {
	if len(args) < 5 {
		log.Print("reverse: too few arguments")
		return nil
	}
	remotePort := args[0].Int()
	bindAll := args[1].Bool()
	targetHost := args[2].String()
	targetPort := args[3].Int()
	cb := args[4]

	if sshClient == nil {
		log.Print("reverse: no ssh connection")
		invokeCB(cb, map[string]interface{}{"ok": false, "error": "no active session"})
		return nil
	}

	// Use an IP literal, not "localhost": ssh.Client.Listen resolves the bind
	// address, and a DNS lookup isn't available inside the WASM runtime.
	bindHost := "127.0.0.1"
	if bindAll {
		bindHost = "0.0.0.0"
	}
	key := fmt.Sprintf("%s:%d", bindHost, remotePort)
	info := fmt.Sprintf("remote %s → %s:%d", key, targetHost, targetPort)

	// Already listening on this remote port: just retarget / re-enable it,
	// avoiding a second tcpip-forward (which would hang after a prior Stop).
	revMu.Lock()
	rf := revFwds[key]
	revMu.Unlock()
	if rf != nil {
		rf.mu.Lock()
		rf.host, rf.port, rf.stopped = targetHost, targetPort, false
		rf.mu.Unlock()
		log.Printf("reverse: re-enabled %s", info)
		invokeCB(cb, map[string]interface{}{"ok": true, "key": key, "info": info})
		return nil
	}

	go func() {
		defer recoverLog("reverse listener " + key)
		l, err := sshClient.Listen("tcp", key)
		if err != nil {
			log.Printf("reverse: listen on remote %s failed: %v", key, err)
			invokeCB(cb, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		rf := &revFwd{l: l, host: targetHost, port: targetPort}
		revMu.Lock()
		revFwds[key] = rf
		revMu.Unlock()

		log.Printf("reverse: %s", info)
		invokeCB(cb, map[string]interface{}{"ok": true, "key": key, "info": info})

		for {
			rc, err := l.Accept()
			if err != nil {
				log.Printf("reverse: listener %s stopped: %v", key, err)
				return
			}
			rf.mu.Lock()
			stopped, host, port := rf.stopped, rf.host, rf.port
			rf.mu.Unlock()
			if stopped {
				rc.Close() // gated: drop connections without forwarding
				continue
			}
			go handleReverseConn(rc, host, port)
		}
	}()
	return nil
}

// stopReverseForward(key string) gates a forward: the remote listener stays
// open but incoming connections are dropped. It does NOT close the ssh
// listener (see the note at the top of the file).
func stopReverseForward(this js.Value, args []js.Value) interface{} {
	if len(args) < 1 {
		return nil
	}
	key := args[0].String()
	revMu.Lock()
	rf := revFwds[key]
	revMu.Unlock()
	if rf != nil {
		rf.mu.Lock()
		rf.stopped = true
		rf.mu.Unlock()
		log.Printf("reverse: gated %s", key)
	}
	return nil
}

func recoverLog(what string) {
	if r := recover(); r != nil {
		log.Printf("reverse: recovered from panic in %s: %v", what, r)
	}
}

func handleReverseConn(rc net.Conn, host string, port int) {
	defer recoverLog("reverse conn")
	defer rc.Close()
	tc, err := con(host, port, false) // dial the target through the websocket proxy
	if err != nil {
		log.Printf("reverse: dial target %s:%d failed: %v", host, port, err)
		return
	}
	defer tc.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(tc, rc); done <- struct{}{} }()
	go func() { io.Copy(rc, tc); done <- struct{}{} }()
	<-done // tear down once either direction closes
}

func invokeCB(cb js.Value, v interface{}) {
	if cb.Type() == js.TypeFunction {
		cb.Invoke(v)
	}
}
