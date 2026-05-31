package main

// TOTP login gate. The server holds a TOTP seed (TOTP_SEED, base32); users
// enter the rotating 6-digit code from an authenticator app and receive an
// HMAC-signed session cookie valid for SESSION_TTL (default 24h). The gate is
// enforced as middleware in front of the static site and the websocket.
//
// If TOTP_SEED or SESSION_SECRET is unset the gate is disabled and the site is
// served openly (useful for local dev / the test profile).

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cookieName = "ssheasy_session"

var (
	gateEnabled bool
	totpSeed    []byte        // decoded TOTP secret
	signKey     []byte        // HMAC key for session cookies
	sessionTTL  time.Duration // how long a session lasts
)

// Per-IP brute-force lockout: after maxLoginFails wrong codes an IP is blocked
// for loginLockout. Entries are kept in memory and expire after loginLockout of
// inactivity (the "times out for 24 hours" cache). A successful login clears
// the IP immediately.
const (
	maxLoginFails = 10
	loginLockout  = 24 * time.Hour
)

type loginAttempts struct {
	fails       int
	lockedUntil time.Time
	last        time.Time
}

var (
	loginMu   sync.Mutex
	loginByIP = map[string]*loginAttempts{}
)

// clientIP extracts the requester's address, honouring the proxy headers set in
// front of us (Cloudflare / nginx) before falling back to the socket address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if xr := r.Header.Get("X-Real-Ip"); xr != "" {
		return strings.TrimSpace(xr)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// loginLocked reports whether ip is currently locked out, purging stale entries.
func loginLocked(ip string) bool {
	loginMu.Lock()
	defer loginMu.Unlock()
	now := time.Now()
	for k, a := range loginByIP { // opportunistic sweep of expired entries
		if now.Sub(a.last) > loginLockout && now.After(a.lockedUntil) {
			delete(loginByIP, k)
		}
	}
	a := loginByIP[ip]
	return a != nil && now.Before(a.lockedUntil)
}

// recordLogin updates the failure counter for ip. A success clears it; the
// maxLoginFails-th failure starts the lockout.
func recordLogin(ip string, ok bool) {
	loginMu.Lock()
	defer loginMu.Unlock()
	if ok {
		delete(loginByIP, ip)
		return
	}
	a := loginByIP[ip]
	if a == nil {
		a = &loginAttempts{}
		loginByIP[ip] = a
	}
	a.fails++
	a.last = time.Now()
	if a.fails >= maxLoginFails {
		a.lockedUntil = time.Now().Add(loginLockout)
		log.Printf("auth: locked out %s after %d failed attempts", ip, a.fails)
	}
}

// initAuth configures the gate from the environment. It returns whether the
// gate is enabled.
func initAuth() bool {
	rawSeed := strings.ToUpper(strings.ReplaceAll(os.Getenv("TOTP_SEED"), " ", ""))
	secret := os.Getenv("SESSION_SECRET")
	if rawSeed == "" || secret == "" {
		log.Print("auth gate DISABLED (set TOTP_SEED and SESSION_SECRET to enable)")
		return false
	}

	var err error
	totpSeed, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(rawSeed)
	if err != nil {
		log.Fatalf("TOTP_SEED is not valid base32: %v", err)
	}
	if len(secret) < 16 {
		log.Fatal("SESSION_SECRET must be at least 16 characters")
	}
	signKey = []byte(secret)

	sessionTTL = 24 * time.Hour
	if v := os.Getenv("SESSION_TTL"); v != "" {
		if sessionTTL, err = time.ParseDuration(v); err != nil {
			log.Fatalf("invalid SESSION_TTL: %v", err)
		}
	}

	gateEnabled = true
	log.Printf("auth gate ENABLED (session ttl %s)", sessionTTL)
	return true
}

// authMiddleware gates every request except the login/logout endpoints. An
// unauthenticated browser navigation is redirected to the login page; anything
// else (e.g. the websocket handshake) gets a bare 401.
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gateEnabled || strings.HasPrefix(r.URL.Path, "/auth/") || isPublicAsset(r.URL.Path) || authed(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
}

// isPublicAsset reports whether a path is a PWA asset that must be reachable
// without a session cookie. Browsers fetch the web app manifest (and the icon
// downloads for installation) with credentials omitted, so gating these would
// stop Chrome from ever offering "Install app". They carry no sensitive info.
func isPublicAsset(p string) bool {
	switch p {
	case "/manifest.webmanifest", "/favicon.ico", "/icon.svg":
		return true
	}
	return strings.HasPrefix(p, "/icon-") && strings.HasSuffix(p, ".png")
}

func authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && validSession(c.Value)
}

// handleLogin serves the login form (GET) and verifies the TOTP code (POST).
func handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, loginPage(""))
	case http.MethodPost:
		ip := clientIP(r)
		if loginLocked(ip) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, loginPage("Too many attempts. Try again later."))
			return
		}
		// Throttle verification to slow brute-forcing the 6-digit code: every
		// attempt costs at least a second of wall-clock, applied uniformly so it
		// also avoids leaking a fast/slow timing difference between outcomes.
		time.Sleep(time.Second)
		code := strings.TrimSpace(r.FormValue("code"))
		ok := verifyTOTP(code, time.Now())
		recordLogin(ip, ok)
		if !ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, loginPage("Invalid code, try again."))
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    newSession(time.Now().Add(sessionTTL)),
			Path:     "/",
			MaxAge:   int(sessionTTL.Seconds()),
			HttpOnly: true,
			Secure:   r.Header.Get("X-Forwarded-Proto") == "https",
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

// --- session tokens: "<expiryUnix>.<hex hmac>" ---

func newSession(expiry time.Time) string {
	payload := strconv.FormatInt(expiry.Unix(), 10)
	return payload + "." + sign(payload)
}

func validSession(token string) bool {
	payload, mac, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(mac), []byte(sign(payload))) != 1 {
		return false
	}
	expiry, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expiry
}

func sign(payload string) string {
	m := hmac.New(sha256.New, signKey)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

// --- TOTP (RFC 6238: SHA1, 6 digits, 30s step) ---

// verifyTOTP accepts the current step and ±1 step to tolerate clock skew.
func verifyTOTP(code string, now time.Time) bool {
	if len(code) != 6 {
		return false
	}
	counter := now.Unix() / 30
	for _, c := range []int64{counter - 1, counter, counter + 1} {
		if subtle.ConstantTimeCompare([]byte(code), []byte(totpAt(c))) == 1 {
			return true
		}
	}
	return false
}

func totpAt(counter int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	m := hmac.New(sha1.New, totpSeed)
	m.Write(buf[:])
	sum := m.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	v := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", v%1000000)
}
