# Autograder

Autograder is a web-based system designed for software project auto-grading, utilizing Docker.

This repository contains the server-side code. For the web-based client-side code, please visit 
[https://github.com/howardlau1999/autograder-web](https://github.com/howardlau1999/autograder-web).

For documentation, please head for [https://autograder-docs.howardlau.me](https://autograder-docs.howardlau.me)

知乎文章介绍：[Autograder - 一个适合项目作业的评测系统](https://zhuanlan.zhihu.com/p/479027855)

## Build

Go 1.26+ is needed for building the server (go.mod pins the exact patch release; newer Go
toolchains download it automatically).

To build without client-side webpage code (which means you need a reverse-proxy like nginx to serve the static contents)

```bash
go build -tags containers_image_openpgp -o autograder-server ./cmd/autograder-server
```

To build the grader service

```bash
go build -tags containers_image_openpgp -o autograder-grader ./cmd/autograder-grader
```

To build with the client-side webpage code, Node.js 24 (or 22.22+) is needed.

```bash
git submodule update --init
cd web
npm install && npm run install:vcd-stream && npm run build:server   # writes pkg/web/dist
cd ..
go build -tags containers_image_openpgp -o autograder-server ./cmd/autograder-server
```

`install:vcd-stream` installs the (pinned) VCD waveform parser separately because its install
script builds a native addon; `build:server` is `ng build` with the output laid out flat in
`pkg/web/dist`, which is what `pkg/web/embed.go` embeds.

## Development checks

```bash
gofmt -l cmd pkg            # must print nothing
go vet -tags containers_image_openpgp ./...
go test -race -tags containers_image_openpgp ./...
```

### Dependency notes

- The key-value store is `github.com/cockroachdb/pebble/v2`. Pebble v2 cannot open stores
  written by the previous releases (pebble v1 at its default format), so `repository.OpenDB`
  upgrades such a store in place on first start: it is ratcheted with pebble v1.1 (kept as a
  dependency for exactly this) to v1's newest format, which v2 accepts and which the previous
  release can still read, so a rollback to that release stays possible. The upgrade is logged as
  `DB.FormatUpgrade`; back up the data directory before deploying a new release anyway. Once no
  deployment runs the old format any more, the `pebble` v1 dependency can be dropped.
- gRPC-Web is served by `pkg/middleware.GrpcWeb`, a small in-process bridge in front of the
  grpc-go server (the archived `improbable-eng/grpc-web` is gone). It supports unary and
  server-streaming calls, binary and base64 ("grpc-web-text") framing and CORS; the web client
  reads streams from the fetch response body, there is no WebSocket transport any more.
- Interceptors come from `github.com/grpc-ecosystem/go-grpc-middleware/v2` (logging, recovery,
  auth) and its `providers/prometheus` module, which exports the same `grpc_server_*` /
  `grpc_client_*` metric names as the retired `go-grpc-prometheus`. Request-scoped loggers are
  obtained with `logging.Extract(ctx)` from `pkg/logging`.
- Docker is accessed through the split-out `github.com/moby/moby/client` SDK, not
  `github.com/docker/docker`, so govulncheck no longer reports the daemon-only advisories of the
  monolithic module.

## Configuration

Both binaries read `config.toml` from `/etc/autograder-server/`, `$HOME/.autograder-server/` or the
working directory; `autograder-server --config` prints a template. Any key can be overridden by an
environment variable named after it with `.` and `-` replaced by `_` (e.g. `HUB_TOKEN`,
`TOKEN_SECRET_SESSION`). The server refuses to start while any of `token.secret.*`, `hub.token`
or `fs.http.token` is empty or still set to the placeholder from the template.

Keys that need attention when deploying:

- `server.trusted-proxy-count` — number of reverse-proxy hops (nginx, CDN, load balancer) in
  front of the web port. With the default `0` the rate limiter (API requests under
  `/AutograderService/` only; static files are not counted) keys on the socket address and
  ignores `X-Forwarded-For`, so behind a proxy *all* users would share one bucket; set it to the
  real hop count there.
- `cors.allowed-origins` — origins allowed to make cross-origin requests in production (in
  `server.development=true` every origin is reflected). Leave it empty when the web client is
  served by this server: same-origin requests are always accepted. Only list an origin when the
  client is served from a different host or port, e.g. `["http://localhost:4200"]` for
  `ng serve`. Server-streaming RPCs are plain HTTP responses that are flushed message by message
  (the server sets `X-Accel-Buffering: no`, which nginx honours); a proxy that buffers responses
  regardless would delay them.
- `smtp.tls-mode` — `implicit` (TLS from the start, port 465, the default), `starttls` (port 587)
  or `none` (plain, for localhost relays only).
- `hcaptcha.site-key` / `hcaptcha.secret` — both are required for the sign-up and
  password-reset captcha (`hcaptcha.secret-key` is still read as a fallback). The site key is
  injected into the served web client.
