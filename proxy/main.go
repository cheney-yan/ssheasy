package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/websocket"
	"golang.org/x/time/rate"
)

func main() {
	publicAddr := flag.String("pub", ":5555", "listener address")
	adminAddr := flag.String("priv", ":6666", "admin listener address")
	adminKey := flag.String("adm-key", "qtIazZDhzrYERShXuYpqRx", "admin api key")
	auditLogFile := flag.String("al", "connections.log", "log file to store connection request, if empty connection logging is disabled")
	flag.Parse()
	if *publicAddr == "" || *adminAddr == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	if auditLogFile != nil {
		var err error
		auditLog, err = os.OpenFile(*auditLogFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
		if err != nil {
			log.Fatalf("failed to open connection log file %s: %v", *auditLogFile, err)
		}
	}

	wmux := websocket.Server{
		Handshake: bootHandshake,
		Handler:   handleWss,
	}

	startAdmin(*adminAddr, *adminKey)
	initAuth()

	r := mux.NewRouter()
	// TOTP login gate (no-op unless TOTP_SEED + SESSION_SECRET are set).
	r.HandleFunc("/auth/login", handleLogin)
	r.HandleFunc("/auth/logout", handleLogout)
	// Websocket proxy. /p is the path the WASM client dials (see web/main.go);
	// /ws is kept for backwards compatibility.
	r.Handle("/p", wmux)
	r.Handle("/ws", wmux)
	// /pm carries a yamux-multiplexed session: many connections share one
	// obfuscated websocket (default path; /p is the per-connection fallback).
	r.Handle("/pm", websocket.Server{Handshake: bootHandshake, Handler: handleMuxWss})
	// Static site, with SPA fallback to index.html for unknown paths.
	r.PathPrefix("/").Handler(spaFileServer(Dir("./html")))

	srv := http.Server{
		Addr:    *publicAddr,
		Handler: authMiddleware(r),
	}
	idleConnsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)
		<-sigint

		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("HTTP server Shutdown: %v", err)
		}
		close(idleConnsClosed)
	}()

	log.Printf("server starts on: %s", *publicAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("HTTP server ListenAndServe: %v", err)
	}

	<-idleConnsClosed
	wc := 0
	for {
		a := atomic.LoadInt64(&activeWebsocks)
		if a <= 0 {
			log.Printf("%d active websockets, terminating", a)
			break
		}
		time.Sleep(300 * time.Millisecond)
		wc++
		if wc%100 == 0 {
			log.Printf("%d websockets are active, waiting", a)
		}
	}
}

var activeWebsocks int64

func handleWss(wsconn *websocket.Conn) {
	var ac prometheus.Gauge
	defer func() {
		atomic.AddInt64(&activeWebsocks, -1)
		wsconn.Close()
		if ac != nil {
			ac.Dec()
		}
	}()
	atomic.AddInt64(&activeWebsocks, 1)
	id := wsconn.Config().Header.Get(reqIDHdr)
	l := logFromID(id)
	l.logf("request headers: %v", wsconn.Request().Header)
	blocked, ips := getIPAdress(wsconn)
	if blocked {
		l.logf("blocking ip: %v", ips)
		return
	}
	l.logf("handlewss from %v", ips)
	totalConnectionRequests.WithLabelValues(svcHost).Inc()
	// Wrap the websocket in the obfuscation layer (when enabled) before any
	// payload is read or written, so the connection header and the SSH banner
	// never appear in plaintext on the wire. Binary frames must be set before
	// the first write. tr is the transport used for all payload from here on;
	// control frames (ping) keep using wsconn directly.
	wsconn.PayloadType = websocket.BinaryFrame
	var tr net.Conn = wsconn
	if obfsEnabled() {
		tr = newObfConn(wsconn, true, obfsPSK())
	}
	err := wsconn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		log.Printf("failed to set red deadline: %v", err)
		return
	}
	buf := make([]byte, 2048)
	_, err = tr.Read(buf)
	if err != nil {
		l.logf("failed to read connection msg: %v", err)
		return
	}
	var cr struct {
		Host string
		Port int
	}
	err = json.NewDecoder(bytes.NewBuffer(buf)).Decode(&cr)
	if err != nil {
		l.logf("failed to decode connection request [%s]: %v", buf, err)
		return
	}
	err = wsconn.SetReadDeadline(time.Time{})
	if err != nil {
		l.logf("failed to reset connection deadline: %v", err)
		return
	}
	l.logf("connecting to %s on port %d", cr.Host, cr.Port)
	writeAuditLog(ips[0], cr.Host, cr.Port, "connection request")
	if !isAllowedTarger(cr.Host) {
		l.logf("WARNING: connecting to %s is not allowed", cr.Host)
		return
	}
	var resp struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", cr.Host, cr.Port), 30*time.Second)
	if err != nil {
		l.logf("failed to connect: %v", err)
		writeAuditLog(ips[0], cr.Host, cr.Port, "connection failed")
		resp.Status = "failed"
		resp.Error = err.Error()
		if r, err := json.Marshal(resp); err != nil {
			l.logf("failed to marshall: %v", err)
		} else {
			if _, err := tr.Write(r); err != nil {
				l.logf("failed to write status: %v", err)
			}
		}
		return
	}
	defer conn.Close()
	resp.Status = "ok"
	if r, err := json.Marshal(resp); err != nil {
		l.logf("failed to marshall: %v", err)
	} else {
		if _, err := tr.Write(r); err != nil {
			l.logf("failed to write status: %v", err)
		}
	}
	totalConnections.WithLabelValues(svcHost).Inc()
	ac = activeConnections.WithLabelValues(svcHost)
	ac.Inc()
	writeAuditLog(ips[0], cr.Host, cr.Port, "connection established")

	cw, wsw := newLimters(conn, tr, l)

	done := make(chan struct{})

	go ping(l, wsconn, done)

	type conStat struct {
		dir   string
		err   error
		bytes int64
	}

	stats := make(chan conStat)

	go func() {
		n, err := io.Copy(&meteredWriter{
			w: cw,
			c: totalBytes.WithLabelValues(svcHost, "up"),
		}, tr)
		conn.Close()
		stats <- conStat{"up", err, n}
	}()
	go func() {
		n, err := io.Copy(&meteredWriter{
			w: wsw,
			c: totalBytes.WithLabelValues(svcHost, "down"),
		}, conn)
		wsconn.Close()
		stats <- conStat{"down", err, n}
	}()

	s1 := <-stats
	s2 := <-stats
	if s1.dir == "up" {
		l.logf("proxy finished copied (%d/%d)bytes anyerrors (%v,%v)", s1.bytes, s2.bytes, s1.err, s2.err)
		writeAuditLog(ips[0], cr.Host, cr.Port, fmt.Sprintf("proxy finished copied (%d/%d)bytes anyerrors (%v,%v)", s1.bytes, s2.bytes, s1.err, s2.err))
	} else {
		l.logf("proxy finished copied (%d/%d)bytes anyerrors (%v,%v)", s2.bytes, s1.bytes, s2.err, s1.err)
		writeAuditLog(ips[0], cr.Host, cr.Port, fmt.Sprintf("proxy finished copied (%d/%d)bytes anyerrors (%v,%v)", s2.bytes, s1.bytes, s2.err, s1.err))
	}
	close(done)
}

var auditLog io.Writer

func writeAuditLog(srcIP, dstIP string, dstPort int, msg string) {
	if auditLog == nil {
		return
	}
	_, err := auditLog.Write([]byte(fmt.Sprintf("%s,%s,%s,%d,%s\n", time.Now().UTC().Format(time.RFC3339Nano), srcIP, dstIP, dstPort, msg)))
	if err != nil {
		log.Printf("failed to write into connection log: %v", err)
	}
}

func ping(l logger, ws *websocket.Conn, done chan struct{}) {
	w, err := ws.NewFrameWriter(websocket.PingFrame)
	if err != nil {
		l.logf("failed to create pingwriter: %v", err)
		return
	}
	ticker := time.Tick(20 * time.Second)
	for {
		select {
		case <-ticker:
			_, err = w.Write(nil)
			if err != nil {
				l.logf("failed to write ping msg: %v", err)
				return
			}
		case <-done:
			return
		}
	}
}

type rCtx struct {
	headers http.Header
}

const reqIDHdr = "X-Request-ID"

func bootHandshake(config *websocket.Config, r *http.Request) error {
	// config.Protocol = []string{"binary"}
	u, err := uuid.NewRandom()
	id := "not-uuid"
	if err == nil {
		id = u.String()
	}
	config.Header = make(http.Header)
	config.Header.Set(reqIDHdr, id)

	// r.Header.Set("Access-Control-Allow-Origin", "*")
	// r.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")

	return nil
}

var (
	blacklistedSources []string
	blacklistSrcMu     sync.RWMutex
	sourceRates        = map[string]*rate.Limiter{}
)

// Per-source-IP new-connection limiter. Default is 1 conn/sec, burst 1 (the
// original anti-abuse setting for the public deployment). This is far too tight
// for reverse-proxying short-lived connections (e.g. HTTP/1.0, where each
// request is a fresh connection -> a fresh /p websocket), so it's configurable:
//   SRC_CONN_RATE   conns/sec (float). "off"/"0"/negative disables limiting.
//   SRC_CONN_BURST  burst size (int).
var (
	srcConnRate     = rate.Limit(1)
	srcConnBurst    = 1
	srcLimitEnabled = true
)

func init() {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("SRC_CONN_RATE"))); v != "" {
		if v == "off" || v == "no" {
			srcLimitEnabled = false
		} else if f, err := strconv.ParseFloat(v, 64); err == nil {
			if f <= 0 {
				srcLimitEnabled = false
			} else {
				srcConnRate = rate.Limit(f)
			}
		}
	}
	if v := os.Getenv("SRC_CONN_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			srcConnBurst = n
		}
	}
}

func getIPAdress(ws *websocket.Conn) (bool, []string) {
	// using sprintf as it panics locally
	var ips []string
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip"} {
		addresses := strings.Split(ws.Request().Header.Get(h), ",")
		for i := len(addresses) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(addresses[i])

			ips = append(ips, ip)
		}
	}
	ips = append(ips, fmt.Sprintf("%v", ws.RemoteAddr()))
	blacklistSrcMu.RLock()
	defer blacklistSrcMu.RUnlock()
	if sourceRates[ips[0]] == nil {
		sourceRates[ips[0]] = rate.NewLimiter(srcConnRate, srcConnBurst)
	}
	for _, bi := range blacklistedSources {
		for _, ip := range ips {
			if strings.HasPrefix(ip, bi) {
				return true, ips
			}
		}
	}
	if !srcLimitEnabled {
		return false, ips
	}
	return !sourceRates[ips[0]].Allow(), ips
}

var (
	blacklistedTargets = []string{"localhost", "127.0.0.1", "::1"}
	// blacklistedTargets = []string{}
	blacklistMu sync.RWMutex
)

func isAllowedTarger(host string) bool {
	blacklistMu.RLock()
	for _, h := range blacklistedTargets {
		if host == h {
			return false
		}
	}
	blacklistMu.RUnlock()

	return true
}

var (
	freeLimit                 = 1024 * 1024 * 1024
	maxLimitedRate rate.Limit = 100 * 1024
	maxBurst                  = 64 * 1024
)

func newLimters(w1, w2 io.Writer, logger logger) (*limitedWriter, *limitedWriter) {
	l := rate.NewLimiter(maxLimitedRate, maxBurst)
	return &limitedWriter{w: w1, limiter: l, log: logger}, &limitedWriter{w: w2, limiter: l, log: logger}
}

type limitedWriter struct {
	w       io.Writer
	written int
	limiter *rate.Limiter
	log     logger
}

func (w *limitedWriter) Write(b []byte) (n int, err error) {
	if w.written > freeLimit {
		if err := w.limiter.WaitN(context.Background(), len(b)); err != nil {
			w.log.logf("limiter wait error: %v", err)
		}
	}
	w.written += len(b)
	return w.w.Write(b)
}

type meteredWriter struct {
	w io.Writer
	c prometheus.Counter
}

func (w *meteredWriter) Write(b []byte) (n int, err error) {
	n, err = w.w.Write(b)
	w.c.Add(float64(n))
	return n, err
}

type logger string

func (l logger) logf(fmt string, args ...interface{}) {
	log.Printf(string(l)+fmt, args...)
}

func newLogger() logger {
	u, err := uuid.NewRandom()
	id := "not-uuid"
	if err == nil {
		id = u.String()
	}
	return logFromID(id)
}

func logFromID(id string) logger {
	return logger(id[:8] + " ")
}
