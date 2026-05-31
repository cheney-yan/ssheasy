# ssheasy

Source repositorty of the online ssh, sftp client [ssheasy.com](https://ssheasy.com)

![SSH and open tunnel in new tab](./doc/tunnel.gif)

## Building, running locally

`docker-compose up`

This will compile the wasm ssh, sftp client and the proxy component. The proxy
serves the web frontend, proxies tcp connections for the client running in the
browser through a websocket, and enforces an optional TOTP login gate.

### Login gate (TOTP)

To require a 6-digit authenticator code before the site is reachable, set
`TOTP_SEED` (base32) and `SESSION_SECRET` in `.env` (see `.env.example`). The
proxy then serves a login page; a correct code grants a signed session cookie
lasting `SESSION_TTL` (default 24h). Add `TOTP_SEED` to your authenticator app
(Google Authenticator / Authy / 1Password) as a manual key. If these are unset,
the gate is disabled and the site is served openly.

The published ports are bound to `127.0.0.1`; public access is intended to go
through the Cloudflare Tunnel (`cloudflared` service), so the gate can't be
bypassed by hitting the container directly.

### connect endpoint

Use `/connect?host=HOST&port=PORT&user=USER&password=PASSWORD` for initiating connection right away after opening the url.


| Parameter      | Description                                 | Default Value |
|----------------|---------------------------------------------|--------------|
| `host`         | SSH server hostname or IP address           | –            |
| `port`         | SSH server port                             | 22           |
| `user`         | SSH username                                | –            |
| `password`     | SSH password                                | –            |
| `pk`           | Private key (as string, for key auth)       | –            |
| `webauthnKey`  | WebAuthn key ID (for WebAuthn auth)         | -1           |
| `connect`      | Whether to auto-connect (`"true"`/`"false"`) | "true"      |

*Notes:*
- `host` is mandatory if `connect` is `"true"` (or not provided).
- If `connect` is `"false"`, the connection will not auto-initiate, but the provided connection data will be filled in the connection form.


## Testing

For testing docker-compose can set up an sshd in a separate container (opt-in
via the `test` profile: `docker compose --profile test up`). After starting up
the stack open http://localhost:8081 in your browser and use the host testssh
with user root and password root.

### Testing Webauthn

After building the project and creating a webauthn key copy the displayed public key to the `ssh_conf/authorized_keys` file and start the testopenssh service in the docker compose if you have not started it yet. User name is `linuxserver.io` hostname: `testopenssh` port: `2222`.  

## Project structure

* nginx: web server config, Dockerfile for building the wasm ssh/sftp client
* proxy: golang proxy service for tunneling tcp connections through websocket
* web: source of the ssh, sftp wasm client, and the httml for the frontend

### Filemanager UI

The filemanager is based on [forked version](https://github.com/hullarb/angular-filemanager)) of [angular-filemanager](https://github.com/joni2back/angular-filemanager). The fork replaces the backend api calls with calls to the wasm sftp client.
The fork has to be built separately and copied to the *web/html/node_modules* directory.
