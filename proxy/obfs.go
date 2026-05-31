package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net"
	"os"
	"strings"
	"sync"
)

// Traffic obfuscation for the proxy <-> WASM client websocket.
//
// This hides the plaintext connection header ({host,port} JSON) and the
// cleartext "SSH-2.0-" banner from naive websocket inspection / SSH
// fingerprinting. It is OBFUSCATION, not confidentiality: in pre-shared-key
// mode the key ships in the client; in negotiated mode the anonymous X25519
// exchange has no authentication and so does not resist an active MITM. The SSH
// session carried inside stays end-to-end encrypted by SSH itself.
//
// Configuration (environment; the client must be configured to match):
//   OBFS_ENABLED  - "false"/"0"/"no"/"off" disables. Default: enabled.
//   OBFS_KEY      - pre-shared passphrase. If empty, each connection negotiates
//                   a fresh random key via ephemeral X25519.

// obfsEnabled reports whether the obfuscation layer is active (default: yes).
func obfsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OBFS_ENABLED"))) {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// obfsPSK returns the pre-shared passphrase, or "" to negotiate per connection.
func obfsPSK() string {
	return os.Getenv("OBFS_KEY")
}

// obfsSeedLen is the random per-connection seed used in pre-shared-key mode so
// each connection's keystream differs even with a fixed key.
const obfsSeedLen = 16

// obfConn wraps a net.Conn and XORs every byte with an AES-CTR keystream, using
// independent keystreams per direction. The handshake (which establishes the
// shared secret) runs lazily on the first Read/Write.
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
	// Server reads client->proxy ("up") and writes proxy->client ("down");
	// the client is the mirror image.
	if o.server {
		o.r, o.w = up, down
	} else {
		o.r, o.w = down, up
	}
	return nil
}

// handshakePSK derives the secret from the pre-shared key plus a random seed
// that the client sends as the first bytes on the wire.
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

// handshakeECDH negotiates a fresh shared secret via ephemeral X25519. The
// both sides send their public key immediately and then read the peer's, so
// the two keys cross on the wire (≈0.5 RTT instead of 1). The 32-byte keys
// look random, so there is still no fixed signature.
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

// obfStreams derives one AES-CTR keystream per direction from the shared secret.
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
