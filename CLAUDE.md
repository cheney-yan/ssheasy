# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

This is a self-hosted fork of ssheasy: a browser SSH/SFTP client where the SSH
engine runs as WebAssembly in the page, behind a TOTP login gate, exposed via a
Cloudflare Tunnel.

## Architecture (big picture)

There are **two Go modules** plus the static frontend:

- `web/` — the client, compiled to **WASM** (`GOOS=js GOARCH=wasm`). It runs
  `golang.org/x/crypto/ssh` *in the browser*: the real SSH client lives in the
  page (`web/main.go`), SFTP/file-browser in `web/browser.go`, WebAuthn keys in
  `web/webauth.go`. The browser can't open raw TCP, so the SSH transport is a
  WebSocket to the proxy. `web/html/index.html` is the whole UI (xterm.js
  terminal + connect card + operations drawer) — a single large file with
  inline JS that calls into WASM-exported functions.
- `proxy/` — a single Go server (`:5555` public, `:6666` admin) that **serves
  the static site + compiled WASM, enforces the TOTP gate, and relays the
  browser's WebSocket to target TCP hosts**. There is no nginx and no separate
  auth service — the proxy does all of it (`proxy/static.go` serves files with
  SPA fallback; `proxy/fileserver.go` is a vendored copy of net/http's file
  server).

Public exposure is via the `cloudflared` service (ingress → `http://proxy:5555`).
Ports are bound to `127.0.0.1` in compose; the tunnel is the only public path.

### The connection path (read this before touching transport code)

`web/main.go:con(host, port)` is the single entry for every proxied connection
(the SSH transport itself **and** each reverse-proxy target dial). It:

1. Dials a **multiplexed** session over `/pm` (`web/mux.go`): one long-lived,
   obfuscated WebSocket carrying a **yamux** session; each logical connection is
   one stream with a length-prefixed `{Host,Port}` header. The server side is
   `proxy/mux.go` (handles `/pm`, accepts streams, dials targets).
2. Falls back to a **dedicated** `/p` WebSocket (`conLegacy` / `proxy` handleWss)
   if the mux session can't be established.

Multiplexing amortizes the per-connection obfuscation handshake and makes the
traffic look like one persistent stream rather than many short connections.

### Obfuscation layer (`web/obfs.go` and `proxy/obfs.go`)

Wraps the WebSocket: AES-CTR XOR keystream per direction, key from ephemeral
X25519 (default) or a pre-shared `OBFS_KEY`. It hides the SSH banner/connection
header from naive inspection — it is **obfuscation, not confidentiality** (SSH
stays e2e encrypted inside). **These two files must stay byte-compatible** —
client and proxy run the same handshake; changing one requires the other.
The handshake is `sync.Once`-guarded because yamux drives Read and Write from
separate goroutines.

### Auth gate (`proxy/auth.go`)

TOTP (RFC 6238) → HMAC-signed session cookie. **Fail-closed**: the proxy
refuses to start without `TOTP_SEED` + `SESSION_SECRET` unless `AUTH_DISABLED=true`.
Hardening: a uniform 1s verify delay and a per-IP lockout (10 fails → 24h).
`authMiddleware` gates everything except `/auth/*` and `isPublicAsset` (the PWA
manifest/icons, which browsers fetch without credentials).

### PWA / disguise

The app is an installable PWA branded **"python3"** to hide its SSH nature.
**Keep all user-facing strings free of "ssh"/"sftp"/"tunnel"** (titles, labels,
manifest, modal copy, WebAuthn RP name). `web/html/sw.js` is **dual-purpose**:
it tunnels `fetch` through the SSH connection for the "Open URL" feature (do not
break the `/portforward` logic) *and* does network-first offline caching.

## Decisions / invariants (don't regress these)

- **`web/obfs.go` ⇄ `proxy/obfs.go` must match.** Same for the mux header format
  in `web/mux.go` ⇄ `proxy/mux.go`.
- **Element IDs in `index.html` are an API** the WASM calls by name
  (`initConnection`, `connected`, `showErr`, `term`, `#terminal`, `#conInf`,
  `passInp`/`pkInp`/`hostInp`/etc., `webauthnKeySelect`). Preserve them when
  restyling.
- **Reverse proxy Stop gates, never closes** the ssh listener (`web/reverse.go`):
  `x/crypto/ssh`'s `listener.Close()` (cancel-tcpip-forward) panics/hangs the
  next re-add on this transport. Remote bind uses an **IP literal** (`127.0.0.1`),
  not `"localhost"` (no DNS in WASM). Reverse targets are dialed from the **proxy
  side**, so to reach the Docker host use `host.docker.internal`.
- **The terminal is `position: fixed; inset: 0`** — never set an inline pixel
  height on `#mainContentArea` (it freezes the size; refit via `refitTerminal`).
- **WASM is bundled into the proxy image at build time.** A redeploy needs
  `docker compose build proxy`; the browser may also need a hard reload / SW
  clear because of caching.
- Per-source connection rate limit (`proxy` `SRC_CONN_RATE`/`SRC_CONN_BURST`,
  default `off` in compose) — the old hardcoded 1/sec broke reverse-proxying
  many short connections.

## Commands

```bash
# Full stack (build WASM + proxy image, run proxy + cloudflared)
docker compose build proxy && docker compose up -d
# opt-in test SSH servers (testssh root/root, testopenssh for WebAuthn) as an overlay:
docker compose -f docker-compose.yaml -f test/docker-compose.yaml up

# Build the WASM client locally (from web/)
cd web && GOOS=js GOARCH=wasm go build -o main.wasm

# Build / test the proxy (from proxy/)
cd proxy && go build . && go test .
go test -run TestObfRoundtripECDH ./...  # a single test
```

Required env (see `.env.example`, copied to gitignored `.env`): `TUNNEL_TOKEN`,
`TOTP_SEED` (base32, also add to an authenticator app), `SESSION_SECRET`.
Optional: `SESSION_TTL`, `AUTH_DISABLED`, `OBFS_KEY`, `SRC_CONN_RATE`/`SRC_CONN_BURST`.

There is no test runner for `web/` (WASM/DOM). End-to-end checks are done by
building the proxy image, running it with a `testssh` container, and driving the
page with headless Chrome over the DevTools Protocol (navigate, `Runtime.evaluate`
to call `initConnection`/`reverseForward`, screenshot).
