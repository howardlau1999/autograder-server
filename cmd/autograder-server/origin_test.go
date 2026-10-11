package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	autograder_pb "autograder-server/pkg/api/proto"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"google.golang.org/grpc"
)

func websocketHandshake(host, origin string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+host+autograder_pb.AutograderService_StreamLog_FullMethodName, nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Sec-Websocket-Protocol", "grpc-websockets")
	return r
}

func TestOriginPolicyCrossOrigin(t *testing.T) {
	prod := newOriginPolicy([]string{"https://app.example.com"}, false)
	if !prod.allowCrossOrigin("https://app.example.com") {
		t.Error("whitelisted origin must be allowed")
	}
	if prod.allowCrossOrigin("https://evil.example.com") {
		t.Error("unlisted origin must be rejected in production")
	}
	if prod.allowCrossOrigin("") {
		t.Error("empty origin is not on the whitelist")
	}
	dev := newOriginPolicy(nil, true)
	if !dev.allowCrossOrigin("https://anything.example.com") {
		t.Error("development mode reflects any origin")
	}
}

// The web client is normally served by this server and opens its
// server-streaming RPCs over WebSocket; browsers always send Origin on the
// handshake, so a same-origin handshake must pass even with an empty whitelist.
func TestOriginPolicyWebsocketSameOrigin(t *testing.T) {
	p := newOriginPolicy(nil, false)
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"same origin without port", "autograder.example.com", "https://autograder.example.com", true},
		{"same origin with port", "localhost:9315", "http://localhost:9315", true},
		{"no origin header (non-browser client)", "autograder.example.com", "", true},
		{"different host", "autograder.example.com", "https://evil.example.com", false},
		{"different port", "localhost:9315", "http://localhost:4200", false},
		{"unparseable origin", "autograder.example.com", "::not a url", false},
	}
	for _, c := range cases {
		if got := p.allowWebsocket(websocketHandshake(c.host, c.origin)); got != c.want {
			t.Errorf("%s: allowWebsocket = %v, want %v", c.name, got, c.want)
		}
	}
	whitelisted := newOriginPolicy([]string{"http://localhost:4200"}, false)
	if !whitelisted.allowWebsocket(websocketHandshake("localhost:9315", "http://localhost:4200")) {
		t.Error("a whitelisted cross-origin websocket handshake must be allowed")
	}
}

// End-to-end through the grpc-web wrapper: a same-origin handshake must not be
// refused at the origin gate (403). The recorder cannot be hijacked, so the
// upgrade itself fails later with a different status, which is fine here. The
// wrapper also 403s handshakes for unregistered endpoints, hence the real
// service registration.
func TestGrpcWebWrapperAcceptsSameOriginWebsocket(t *testing.T) {
	p := newOriginPolicy(nil, false)
	srv := grpc.NewServer()
	autograder_pb.RegisterAutograderServiceServer(srv, &autograder_pb.UnimplementedAutograderServiceServer{})
	wrapped := grpcweb.WrapServer(
		srv, grpcweb.WithOriginFunc(p.allowCrossOrigin),
		grpcweb.WithWebsockets(true), grpcweb.WithWebsocketOriginFunc(p.allowWebsocket),
	)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, websocketHandshake("autograder.example.com", "https://autograder.example.com"))
	if rec.Code == http.StatusForbidden {
		t.Fatal("same-origin websocket handshake was rejected with 403")
	}
	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, websocketHandshake("autograder.example.com", "https://evil.example.com"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin websocket handshake from an unlisted origin got %d, want 403", rec.Code)
	}
}
