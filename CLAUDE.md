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

Public exposure is via the `cloudflared` service (ingress → `http://proxy:5555`),
or directly: compose publishes the site on the host's `:5555` (TOTP-gated). The
admin `:6666` is **not** published (internal only — it has a weak default key).

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

### PWA / disguise / theme

The app is an installable PWA branded **"python3"** to hide its SSH nature.
**Keep all user-facing strings free of "ssh"/"sftp"/"tunnel"** (titles, labels,
manifest, modal copy, WebAuthn RP name). `web/html/sw.js` does **network-first
offline caching only** (the old `/portforward` "Open URL" tunneling was removed).

The displayed name is **white-labelled** via `APP_NAME` (default `python3`):
`index.html` and `manifest.webmanifest` carry an `__APP_NAME__` placeholder that
`loadBranding` substitutes once at startup; the WebAuthn RP name and login page
title use it too (via an injected `window.APP_NAME`). Don't hardcode the name.

UI is shadcn-style mono dark with a light theme. Colours come from CSS tokens in
`:root` (and `:root[data-theme="light"]`); the theme is toggled by setting
`data-theme` on `<html>`, persisted in `localStorage`, and applied by a tiny
`<head>` script before first paint. A single unified control style (one height/
radius/font) covers buttons, inputs, tabs, and the Bootstrap modals — keep new
controls on that system rather than adding bespoke styles.

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
- **Never persist credentials.** `connectNow()` (and the URL auto-connect path)
  pass the password/key to the WASM by value, then immediately blank the fields;
  the password/key inputs have `autocomplete="off"`. Reconnect does NOT reuse the
  password — it reopens the connect form to re-enter it.
- **Host-key trust is TOFU** (`web/html` `showServerKey`): remembered keys live
  in `localStorage` (`knownHosts`, plus `alwaysAcceptHostKey`); a remembered host
  auto-connects, a *changed* key always warns. Auto-accept must reply via
  `setTimeout` — Go calls `showServerKey` synchronously and only then waits on the
  accept channel, so an inline reply deadlocks the WASM call.
- **Connect-card auth tabs**: the single `#passInp` is both the password and the
  key passphrase — JS moves it between the Password/Key panes and relabels it; the
  active tab is remembered. Don't split it into two fields.
- **Permalink**: on connect the URL hash becomes `#user@host:port` (no password);
  on load that hash prefills the fields. "New connection" opens `location.href`
  in a new tab to duplicate the window.
- **Disconnect UX**: a stream read error calls `showReconnect`, which shows a
  persistent one-line top banner (`#disconnectBanner`) with a Reconnect button —
  not a modal, so the terminal stays usable. There is **no auto-reconnect**.
- **Heartbeat**: the WASM client sends an SSH-level keepalive
  (`keepalive@openssh.com`, a namespaced request name — not an email) every 30s
  so idle sessions aren't dropped by `sshd`/NAT; the transport also has its own
  (yamux keepalive on `/pm`, a 20s WebSocket ping on legacy `/p`).
- Terminal **search** highlight: the active match is the xterm *selection*
  (themed black-on-yellow) since the search addon can't recolour matched glyphs;
  other matches use a translucent decoration.

## Commands

```bash
# Full stack (build WASM + proxy image, run proxy + cloudflared)
docker compose build proxy && docker compose up -d

# Build the WASM client locally (from web/)
cd web && GOOS=js GOARCH=wasm go build -o main.wasm

# Build / test the proxy (from proxy/)
cd proxy && go build . && go test .
go test -run TestObfRoundtripECDH ./...  # a single test
```

Required env (see `.env.example`, copied to gitignored `.env`): `TUNNEL_TOKEN`,
`TOTP_SEED` (base32, also add to an authenticator app), `SESSION_SECRET`.
Optional: `SESSION_TTL`, `AUTH_DISABLED`, `OBFS_KEY`, `SRC_CONN_RATE`/`SRC_CONN_BURST`.

## Testing

Unit tests are proxy-side only: `cd proxy && go test ./...` (obfuscation
round-trip for PSK + ECDH, TOTP vectors, rate limiter). There is no runner for
`web/` (WASM/DOM), and there are no committed test fixtures.

End-to-end/manual testing is done by the maintainer on their own server: deploy
the stack (`docker compose build proxy && docker compose up -d`), log in with
the authenticator code, and connect to a real host.
