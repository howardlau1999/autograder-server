# Autograder

Autograder is a web-based system designed for software project auto-grading, utilizing Docker.

This repository contains the server-side code. For the web-based client-side code, please visit 
[https://github.com/howardlau1999/autograder-web](https://github.com/howardlau1999/autograder-web).

For documentation, please head for [https://autograder-docs.howardlau.me](https://autograder-docs.howardlau.me)

知乎文章介绍：[Autograder - 一个适合项目作业的评测系统](https://zhuanlan.zhihu.com/p/479027855)

## Build

Go 1.26+ is needed for building the server (go.mod pins 1.26.6; newer Go
toolchains download it automatically).

To build without client-side webpage code (which means you need a reverse-proxy like nginx to serve the static contents)

```bash
go build -tags containers_image_openpgp -o autograder-server ./cmd/autograder-server
```

To build the grader service

```bash
go build -tags containers_image_openpgp -o autograder-grader ./cmd/autograder-grader
```

To build with the client-side webpage code, Node.js 18+ is needed.

```bash
git submodule update --init 
npm install -g @angular/cli
cd web
npm install && npm install --no-save --ignore-scripts vcd-stream && ng build --output-path ../pkg/web/dist
cd ..
go build -tags containers_image_openpgp -o autograder-server ./cmd/autograder-server
```

## Development checks

```bash
gofmt -l cmd pkg            # must print nothing
go vet -tags containers_image_openpgp ./...
go test -race -tags containers_image_openpgp ./...
```

## Configuration

Both binaries read `config.toml` from `/etc/autograder-server/`, `$HOME/.autograder-server/` or the
working directory; `autograder-server --config` prints a template. Any key can be overridden by an
environment variable named after it with `.` and `-` replaced by `_` (e.g. `HUB_TOKEN`,
`TOKEN_SECRET_SESSION`). The server refuses to start while any of `token.secret.*`, `hub.token`
or `fs.http.token` is empty or still set to the placeholder from the template.

Keys that need attention when deploying:

- `server.trusted-proxy-count` — number of reverse-proxy hops (nginx, CDN, load balancer) in
  front of the web port. With the default `0` the rate limiter keys on the socket address and
  ignores `X-Forwarded-For`, so behind a proxy *all* users would share one bucket; set it to the
  real hop count there.
- `cors.allowed-origins` — origins allowed to make cross-origin requests in production (in
  `server.development=true` every origin is reflected). Leave it empty when the web client is
  served by this server: same-origin requests, including the WebSocket handshakes used for
  server-streaming RPCs, are always accepted. Only list an origin when the client is served from a
  different host or port, e.g. `["http://localhost:4200"]` for `ng serve`. A reverse proxy must
  pass the original `Host` header through (`proxy_set_header Host $host;` in nginx), otherwise
  same-origin WebSocket handshakes cannot be recognised and the origin has to be listed explicitly.
- `smtp.tls-mode` — `implicit` (TLS from the start, port 465, the default), `starttls` (port 587)
  or `none` (plain, for localhost relays only).
- `hcaptcha.site-key` / `hcaptcha.secret` — both are required for the sign-up and
  password-reset captcha (`hcaptcha.secret-key` is still read as a fallback). The site key is
  injected into the served web client.
