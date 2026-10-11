// Package middleware holds the HTTP middleware of the web server.
package middleware

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rs/cors"
	"google.golang.org/grpc"
)

const (
	grpcContentType        = "application/grpc"
	grpcWebContentType     = "application/grpc-web"
	grpcWebTextContentType = "application/grpc-web-text"
)

// GrpcWeb serves gRPC-Web calls with a grpc-go server, in process.
//
// gRPC-Web (grpc/grpc doc/PROTOCOL-WEB.md) is gRPC made to fit what browsers
// can do: it runs over HTTP/1.1 as well as HTTP/2, may base64-encode the body
// ("application/grpc-web-text") and carries the trailers that hold the status
// in a final body frame flagged 0x80 instead of HTTP trailers. Requests are
// therefore handed to grpc.Server.ServeHTTP as if they were HTTP/2 and the
// response is re-framed on the way out. Unary and server-streaming methods
// are reachable this way; client streaming would need WebSockets.
type GrpcWeb struct {
	server *grpc.Server
	cors   *cors.Cors
}

// NewGrpcWeb returns the bridge for server. allowOrigin decides which
// cross-origin callers may use it; same-origin calls never involve CORS.
func NewGrpcWeb(server *grpc.Server, allowOrigin func(origin string) bool) *GrpcWeb {
	return &GrpcWeb{
		server: server,
		cors: cors.New(cors.Options{
			AllowOriginFunc:  allowOrigin,
			AllowedMethods:   []string{http.MethodPost},
			AllowedHeaders:   []string{"*"},
			AllowCredentials: true, // the authorization header does not work without it
			MaxAge:           int(10 * time.Minute / time.Second),
		}),
	}
}

// Handler is the middleware: gRPC-Web calls and their CORS preflights are
// answered here, every other request is passed to next.
func (g *GrpcWeb) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isGrpcWebRequest(r) || isGrpcWebPreflight(r) {
			g.cors.Handler(http.HandlerFunc(g.serve)).ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isGrpcWebRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), grpcWebContentType)
}

func isGrpcWebPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions &&
		strings.Contains(strings.ToLower(r.Header.Get("Access-Control-Request-Headers")), "x-grpc-web")
}

func (g *GrpcWeb) serve(w http.ResponseWriter, r *http.Request) {
	contentType := r.Header.Get("Content-Type")
	webType := grpcWebContentType
	text := strings.HasPrefix(contentType, grpcWebTextContentType)
	if text {
		webType = grpcWebTextContentType
	}

	req := r.Clone(r.Context())
	// grpc-go only accepts HTTP/2 requests; the body framing is identical.
	req.ProtoMajor, req.ProtoMinor = 2, 0
	req.Header.Set("Content-Type", grpcContentType+strings.TrimPrefix(contentType, webType))
	// The length describes the HTTP/1.1 body, base64-encoded in text mode;
	// HTTP/2 requests carry no length and grpc-go reads until EOF.
	req.Header.Del("Content-Length")
	req.ContentLength = -1
	if text {
		req.Body = readCloser{base64.NewDecoder(base64.StdEncoding, r.Body), r.Body}
	}

	resp := newResponseWriter(w, webType, text)
	g.server.ServeHTTP(resp, req)
	resp.finish()
}

type readCloser struct {
	io.Reader
	io.Closer
}

// responseWriter is what grpc-go writes the gRPC response into. It keeps its
// own header map so that the trailers grpc-go sets after the headers went out
// (plain, or with the http.TrailerPrefix) can be collected and sent as the
// final body frame.
type responseWriter struct {
	w           http.ResponseWriter
	header      http.Header
	contentType string
	body        io.Writer      // w, or the base64 encoder in text mode
	encoder     io.WriteCloser // non-nil in text mode
	wroteHeader bool
}

func newResponseWriter(w http.ResponseWriter, contentType string, text bool) *responseWriter {
	rw := &responseWriter{w: w, header: make(http.Header), contentType: contentType, body: w}
	if text {
		rw.newEncoder()
	}
	return rw
}

func (rw *responseWriter) newEncoder() {
	rw.encoder = base64.NewEncoder(base64.StdEncoding, rw.w)
	rw.body = rw.encoder
}

func (rw *responseWriter) Header() http.Header {
	return rw.header
}

func (rw *responseWriter) WriteHeader(status int) {
	if rw.wroteHeader {
		return
	}
	rw.wroteHeader = true
	out := rw.w.Header()
	for key, values := range rw.header {
		if key == "Trailer" || strings.HasPrefix(key, http.TrailerPrefix) {
			continue
		}
		if key == "Content-Type" {
			values = []string{rw.contentType + strings.TrimPrefix(values[0], grpcContentType)}
		}
		out[key] = values
	}
	// Let a cross-origin client read the status and any custom metadata.
	exposed := map[string]bool{"grpc-status": true, "grpc-message": true}
	for key := range out {
		exposed[strings.ToLower(key)] = true
	}
	names := make([]string, 0, len(exposed))
	for name := range exposed {
		names = append(names, name)
	}
	sort.Strings(names)
	out.Set("Access-Control-Expose-Headers", strings.Join(names, ", "))
	// Streams are delivered message by message; tell nginx not to hold them.
	out.Set("X-Accel-Buffering", "no")
	rw.w.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.body.Write(b)
}

// Flush sends buffered data. Before the headers went out it does nothing:
// grpc-go flushes once before writing the status of a call that produced no
// message, and the status is better sent in the headers than after a bare 200.
func (rw *responseWriter) Flush() {
	if rw.wroteHeader {
		rw.flush()
	}
}

func (rw *responseWriter) flush() {
	if rw.encoder != nil {
		// gRPC-Web allows the body to be a sequence of padded base64 chunks.
		_ = rw.encoder.Close()
		rw.newEncoder()
	}
	if f, ok := rw.w.(http.Flusher); ok {
		f.Flush()
	}
}

// finish completes the gRPC-Web response once grpc-go is done: either a
// trailers-only response, with the status in the headers, or the trailer
// frame after the messages.
func (rw *responseWriter) finish() {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
		rw.flush()
		return
	}
	trailers := rw.trailers()
	frame := make([]byte, 5, 5+trailers.Len())
	frame[0] = 1 << 7
	binary.BigEndian.PutUint32(frame[1:], uint32(trailers.Len()))
	_, _ = rw.body.Write(append(frame, trailers.Bytes()...))
	rw.flush()
}

// trailers encodes, as HTTP/1 header lines with lower-case names as the
// protocol requires, every header grpc-go set after the headers were sent.
func (rw *responseWriter) trailers() *bytes.Buffer {
	sent := rw.w.Header()
	lines := make([]string, 0, len(rw.header))
	for key, values := range rw.header {
		if key == "Trailer" {
			continue
		}
		if _, ok := sent[key]; ok {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(key, http.TrailerPrefix))
		for _, value := range values {
			lines = append(lines, fmt.Sprintf("%s: %s\r\n", name, value))
		}
	}
	sort.Strings(lines)
	buf := new(bytes.Buffer)
	for _, line := range lines {
		buf.WriteString(line)
	}
	return buf
}
