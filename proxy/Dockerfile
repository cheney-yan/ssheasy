# Build context is the repo root (see docker-compose.yaml) so we can compile
# both the WASM client (web/) and the proxy, and bundle the static site.

# 1. Compile the WASM ssh/sftp client.
FROM golang:1.23.3-alpine as wasm
WORKDIR /go/src/web
COPY web .
RUN GOOS=js GOARCH=wasm go build -o main.wasm

# 2. Build the proxy binary.
FROM golang:1.20.1-alpine as builder
WORKDIR /go/src/proxy
COPY proxy .
RUN go build .

# 3. Assemble: proxy binary + static assets served from ./html.
FROM alpine:3.17
WORKDIR /
COPY --from=builder /go/src/proxy/proxy /proxy
COPY web/html /html
COPY --from=wasm /go/src/web/main.wasm /html/
COPY --from=wasm /usr/local/go/misc/wasm/wasm_exec.js /html/
CMD ["/proxy"]
