# Browser SSH / SFTP client

A self-hosted **SSH and SFTP client that runs entirely in the browser**. The SSH
engine is compiled to WebAssembly and runs *inside the page* — your keystrokes
and credentials never touch a third-party server. A small Go proxy serves the
site and relays the browser's WebSocket to your target hosts, behind an optional
one-time-code login gate.

It installs as a PWA and is white-labelled (default name **`python3`**) so it
blends in on a shared machine.

```
Browser tab                          Your server                Target host
┌────────────────────────┐          ┌──────────────┐           ┌──────────┐
│ xterm.js terminal       │          │              │           │          │
│ SFTP file browser       │  WSS +   │  Go proxy    │   TCP     │  sshd    │
│ SSH client (WebAssembly)│◄────────►│  :5555       │◄─────────►│  :22     │
│  ↳ real x/crypto/ssh    │  obfusc. │  TOTP gate   │           │          │
└────────────────────────┘          └──────────────┘           └──────────┘
        end-to-end encrypted SSH session (the proxy only sees ciphertext)
```

---

## Table of contents

- [Features](#features)
- [How it works](#how-it-works)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Using the client](#using-the-client)
- [Security model](#security-model)
- [Development](#development)
- [Project structure](#project-structure)
- [Credits](#credits)

---

## Features

- **Full terminal** — xterm.js with search, resize, copy/paste, and an SSH-level
  keepalive so idle sessions don't drop.
- **SFTP file browser** — browse, rename, move, copy, delete, chmod, edit, and
  download files; multi-select download zips on the fly.
- **Drag-and-drop upload** — drop files from your desktop onto the window to
  copy them to `~/Downloads` on the remote (created if missing). A toast reports
  the final remote path, including any collision-renamed filename.
- **Reverse port forwarding** — expose a remote port back through the proxy.
- **Key auth + WebAuthn** — password, private key (with passphrase), or hardware
  security keys.
- **Host-key TOFU** — trust-on-first-use; remembered hosts auto-connect, a
  *changed* key always warns.
- **Permalinks** — the URL hash becomes `#user@host:port` (never the password)
  so you can bookmark a connection.
- **Installable PWA** with a light/dark theme.
- **Optional TOTP login gate** and traffic obfuscation for safe public exposure
  via Cloudflare Tunnel.

## How it works

There are two Go modules plus the static frontend:

| Component  | Runs where        | Responsibility |
|------------|-------------------|----------------|
| `web/`     | **In the browser** (`GOOS=js GOARCH=wasm`) | The real SSH/SFTP client. Browsers can't open raw TCP, so its transport is a WebSocket to the proxy. |
| `proxy/`   | Your server (Go)  | Serves the static site + compiled WASM, enforces the TOTP gate, and relays the browser's WebSocket to target TCP hosts. |

The connection path:

1. The WASM client opens a single long-lived, **obfuscated** WebSocket to the
   proxy and runs a [yamux](https://github.com/hashicorp/yamux) session over it.
   Every logical connection (the SSH transport itself, and each reverse-proxy
   target) is one multiplexed stream.
2. The proxy accepts each stream, reads a small `{host, port}` header, and dials
   the target over plain TCP.
3. The SSH session is established **end-to-end between the browser and the target** —
   the proxy only ever sees the obfuscated, already-SSH-encrypted bytes.

The obfuscation layer (AES-CTR keystream keyed by an ephemeral X25519 exchange)
hides the SSH banner from naive traffic inspection. It is *obfuscation, not
confidentiality* — SSH stays end-to-end encrypted regardless.

## Quick start

You need Docker with Compose. No Go toolchain required — the WASM client and
proxy are built inside the image.

```bash
git clone <this-repo> && cd <this-repo>
cp .env.example .env        # then edit .env (see below)
docker compose up -d --build
```

Open **http://localhost:5555**, enter your authenticator code (if the gate is
enabled), then fill in host / port / user / password (or key) and connect.

### Just trying it locally?

Skip the login gate by setting `AUTH_DISABLED=true` in `.env` — then the only
required value is empty. **Never do this on a public deployment.**

### Enabling the login gate

Generate the two secrets and add them to `.env`:

```bash
# TOTP seed — also add this to Google Authenticator / Authy / 1Password
head -c20 /dev/urandom | base32 | tr -d '='

# Session cookie signing key
openssl rand -hex 32
```

The proxy **fails to start** if `TOTP_SEED` / `SESSION_SECRET` are missing
(unless `AUTH_DISABLED=true`), so you can't accidentally publish an open client.

### Public access via Cloudflare Tunnel

Set `TUNNEL_TOKEN` in `.env` and point the tunnel's ingress at
`http://proxy:5555` in the Cloudflare Zero Trust dashboard. The published port
is bound to localhost, so the gate can't be bypassed by hitting the container
directly. `docker compose up -d` starts the `cloudflared` service alongside the
proxy.

## Configuration

All settings go in `.env` (copied from `.env.example`):

| Variable         | Purpose | Default |
|------------------|---------|---------|
| `APP_NAME`       | White-label name shown as title / PWA name | `python3` |
| `TOTP_SEED`      | Base32 TOTP secret for the login gate | *(required unless `AUTH_DISABLED`)* |
| `SESSION_SECRET` | Key used to sign session cookies (≥16 chars) | *(required unless `AUTH_DISABLED`)* |
| `AUTH_DISABLED`  | `true` serves with **no** login gate (dev only) | unset |
| `SESSION_TTL`    | How long a session lasts after a valid code | `24h` |
| `TUNNEL_TOKEN`   | Cloudflare Tunnel token | *(optional)* |
| `SRC_CONN_RATE`  | Per-source-IP new-connection rate limit (`off`, or conns/sec) | `off` |
| `SRC_CONN_BURST` | Burst for the rate limit | unset |

## Using the client

**Connect** — fill in the connect card and hit connect. The auth tab switches
between password / private key / WebAuthn. Credentials are passed to the WASM by
value and the inputs are blanked immediately — nothing is persisted.

**File browser** — open it from the operations drawer once connected (it's
disabled if the server doesn't offer SFTP).

**Drag-and-drop upload** — drag files from your OS onto the window; an overlay
appears, and on drop they're copied to `~/Downloads` on the remote.

**Auto-connect link** — open the client with query parameters to prefill (and
optionally auto-start) a connection:

```
/connect?host=HOST&port=PORT&user=USER&password=PASSWORD
```

| Parameter     | Description                                  | Default |
|---------------|----------------------------------------------|---------|
| `host`        | SSH server hostname or IP (required)         | –       |
| `port`        | SSH server port                              | 22      |
| `user`        | SSH username                                 | –       |
| `password`    | SSH password                                 | –       |
| `pk`          | Private key as a string (for key auth)       | –       |
| `webauthnKey` | WebAuthn key ID                              | -1      |
| `connect`     | Auto-connect (`"true"` / `"false"`)          | `"true"`|

With `connect=false` the form is prefilled but the session isn't started.

## Security model

- The SSH session is **end-to-end encrypted between your browser and the target**;
  the proxy relays ciphertext only.
- **Credentials are never persisted** — they're blanked from the DOM right after
  use, and reconnecting re-prompts.
- The TOTP gate is **fail-closed**, adds a uniform verify delay, and locks out a
  source IP after repeated failures.
- The transport is **obfuscated** (not a substitute for SSH's own encryption).
- Host keys use **trust-on-first-use**; a changed key always warns.

## Development

```bash
# Full stack (build WASM + proxy image, run proxy + cloudflared)
docker compose up -d --build

# Build the WASM client locally (from web/)
cd web && GOOS=js GOARCH=wasm go build -o main.wasm

# Build / test the proxy (from proxy/)
cd proxy && go build . && go test ./...
```

Unit tests live in the proxy module (obfuscation round-trip, TOTP vectors, rate
limiter). There is no automated runner for the WASM/DOM client — end-to-end
testing is done by deploying the stack and connecting to a real host.

> **Note:** the WASM is bundled into the proxy image at build time. After a
> change, rebuild (`docker compose up -d --build`) and hard-reload the browser
> (clear the Service Worker) to bypass cached assets.

## Project structure

```
web/     SSH/SFTP client compiled to WASM + the single-file HTML/JS frontend
proxy/   Go server: serves the site, TOTP gate, WebSocket↔TCP relay, mux
doc/     Screenshots / assets
```

## Credits

This is a self-hosted fork of [ssheasy](https://ssheasy.com)
([hullarb/ssheasy](https://github.com/hullarb/ssheasy)), with a consolidated Go
proxy (no nginx), a TOTP gate, traffic obfuscation, white-labelling, a themed
single-file UI, and drag-and-drop upload.

See [`LICENSE`](./LICENSE).
