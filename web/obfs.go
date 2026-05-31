package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net"
	"sync"
)

// Traffic obfuscation for the WASM client <-> proxy websocket. See
// proxy/obfs.go for the full rationale and the matching server configuration.
//
// This is OBFUSCATION, not secrecy: it hides the connection header and the
// "SSH-2.0-" banner from naive inspection, but the pre-shared key ships in this
// client and the negotiated mode is unauthenticated. SSH stays e2e encrypted.
//
// The client is configured via JS globals read in main.go (obfsClientEnabled /
// obfsClientKey): window.OBFS_ENABLED (default true) and window.OBFS_KEY. They
// must match the proxy's OBFS_ENABLED / OBFS_KEY.

// obfsSeedLen is the random per-connection seed used in pre-shared-key mode.
const obfsSeedLen = 16

// obfConn wraps a net.Conn and XORs every byte with an AES-CTR keystream, with
// independent keystreams per direction. Handshake is lazy on first Read/Write.
type obfConn struct {
	net.Conn
	server bool
	psk    string // "" => negotiate via X25519
	r      cipher.Stream
	w      cipher.Stream
	once   sync.Once
	hsErr  error
}

func newObfConn(c net.Conn, server bool, psk string) *obfConn {
	return &obfConn{Conn: c, server: server, psk: psk}
}

// handshake runs once, even if Read and Write race to trigger it (yamux drives
// each from a separate goroutine).
func (o *obfConn) handshake() error {
	o.once.Do(func() { o.hsErr = o.doHandshake() })
	return o.hsErr
}

func (o *obfConn) doHandshake() error {
	var secret []byte
	var err error
	if o.psk != "" {
		secret, err = o.handshakePSK()
	} else {
		secret, err = o.handshakeECDH()
	}
	if err != nil {
		return err
	}
	keymat := sha256.Sum256(secret)
	up, down, err := obfStreams(keymat[:])
	if err != nil {
		return err
	}
	if o.server {
		o.r, o.w = up, down
	} else {
		o.r, o.w = down, up
	}
	return nil
}

func (o *obfConn) handshakePSK() ([]byte, error) {
	base := sha256.Sum256([]byte(o.psk))
	seed := make([]byte, obfsSeedLen)
	if o.server {
		if _, err := io.ReadFull(o.Conn, seed); err != nil {
			return nil, err
		}
	} else {
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		if _, err := o.Conn.Write(seed); err != nil {
			return nil, err
		}
	}
	return append(base[:], seed...), nil
}

func (o *obfConn) handshakeECDH() ([]byte, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pub := priv.PublicKey().Bytes()
	peer := make([]byte, len(pub))
	// Send our key while reading the peer's so the two cross on the wire
	// (writing in a goroutine avoids a deadlock on unbuffered transports).
	// Must match proxy/obfs.go.
	werr := make(chan error, 1)
	go func() { _, e := o.Conn.Write(pub); werr <- e }()
	if _, err := io.ReadFull(o.Conn, peer); err != nil {
		return nil, err
	}
	if err := <-werr; err != nil {
		return nil, err
	}
	peerKey, err := curve.NewPublicKey(peer)
	if err != nil {
		return nil, err
	}
	return priv.ECDH(peerKey)
}

func obfStreams(keymat []byte) (up, down cipher.Stream, err error) {
	keyH := sha256.Sum256(append(append([]byte{}, keymat...), []byte("key")...))
	block, err := aes.NewCipher(keyH[:])
	if err != nil {
		return nil, nil, err
	}
	ivUp := sha256.Sum256(append(append([]byte{}, keymat...), []byte("up")...))
	ivDown := sha256.Sum256(append(append([]byte{}, keymat...), []byte("down")...))
	return cipher.NewCTR(block, ivUp[:aes.BlockSize]), cipher.NewCTR(block, ivDown[:aes.BlockSize]), nil
}

func (o *obfConn) Read(p []byte) (int, error) {
	if err := o.handshake(); err != nil {
		return 0, err
	}
	n, err := o.Conn.Read(p)
	if n > 0 {
		o.r.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}

func (o *obfConn) Write(p []byte) (int, error) {
	if err := o.handshake(); err != nil {
		return 0, err
	}
	buf := make([]byte, len(p))
	o.w.XORKeyStream(buf, p)
	return o.Conn.Write(buf)
}
