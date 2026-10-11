package main

import (
	"net/http"
	"net/url"
)

// originPolicy decides which browser origins may talk to the server. It backs
// both the CORS handlers and the gRPC-Web wrapper (including its WebSocket
// transport, which the client uses for server-streaming RPCs).
type originPolicy struct {
	allowed map[string]bool
	devMode bool
}

func newOriginPolicy(allowedOrigins []string, devMode bool) *originPolicy {
	p := &originPolicy{allowed: make(map[string]bool, len(allowedOrigins)), devMode: devMode}
	for _, o := range allowedOrigins {
		p.allowed[o] = true
	}
	return p
}

// allowCrossOrigin reports whether a cross-origin request from origin is
// permitted. In development every origin is reflected for convenience; in
// production the origin must be on the configured whitelist.
func (p *originPolicy) allowCrossOrigin(origin string) bool {
	return p.allowed[origin] || p.devMode
}

// allowWebsocket reports whether a WebSocket handshake may proceed. Browsers
// send an Origin header on every handshake, same-origin ones included, so the
// whitelist alone would reject the web client served by this very server
// whenever cors.allowed-origins is left empty. A handshake whose Origin host
// matches the request Host is therefore always accepted; any other origin has
// to pass allowCrossOrigin. Requests without an Origin header do not come from
// a browser and need no origin check.
func (p *originPolicy) allowWebsocket(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if u, err := url.Parse(origin); err == nil && u.Host != "" && u.Host == r.Host {
		return true
	}
	return p.allowCrossOrigin(origin)
}
