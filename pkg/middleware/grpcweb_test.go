package middleware

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// echoService is a gRPC service described by hand so the test needs no
// generated code: Unary echoes a string, Stream sends it n times.
var echoService = grpc.ServiceDesc{
	ServiceName: "test.Echo",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Unary",
		Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
			in := &wrapperspb.StringValue{}
			if err := dec(in); err != nil {
				return nil, err
			}
			if in.Value == "fail" {
				return nil, status.Error(codes.InvalidArgument, "BAD_INPUT")
			}
			if err := grpc.SetHeader(ctx, metadata.Pairs("token", "refreshed")); err != nil {
				return nil, err
			}
			if err := grpc.SetTrailer(ctx, metadata.Pairs("x-echo-count", "1")); err != nil {
				return nil, err
			}
			return wrapperspb.String("echo: " + in.Value), nil
		},
	}},
	Streams: []grpc.StreamDesc{{
		StreamName:    "Stream",
		ServerStreams: true,
		Handler: func(_ any, stream grpc.ServerStream) error {
			in := &wrapperspb.StringValue{}
			if err := stream.RecvMsg(in); err != nil {
				return err
			}
			for i := 0; i < len(in.Value); i++ {
				if err := stream.SendMsg(wrapperspb.String(in.Value[i : i+1])); err != nil {
					return err
				}
			}
			if strings.HasSuffix(in.Value, "!") {
				return status.Error(codes.Aborted, "STOPPED")
			}
			return nil
		},
	}},
}

type frame struct {
	trailer bool
	payload []byte
}

func parseFrames(t *testing.T, body []byte) []frame {
	t.Helper()
	var frames []frame
	for len(body) > 0 {
		if len(body) < 5 {
			t.Fatalf("truncated frame prefix: %x", body)
		}
		n := binary.BigEndian.Uint32(body[1:5])
		if uint32(len(body)-5) < n {
			t.Fatalf("frame announces %d bytes, %d left", n, len(body)-5)
		}
		frames = append(frames, frame{trailer: body[0]&0x80 != 0, payload: body[5 : 5+n]})
		body = body[5+n:]
	}
	return frames
}

func encodeFrame(t *testing.T, m proto.Message) []byte {
	t.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 5, 5+len(raw))
	binary.BigEndian.PutUint32(out[1:], uint32(len(raw)))
	return append(out, raw...)
}

func decodeString(t *testing.T, payload []byte) string {
	t.Helper()
	v := &wrapperspb.StringValue{}
	if err := proto.Unmarshal(payload, v); err != nil {
		t.Fatal(err)
	}
	return v.Value
}

func newTestServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	grpcServer := grpc.NewServer()
	grpcServer.RegisterService(&echoService, nil)
	t.Cleanup(grpcServer.Stop)
	passedThrough := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passedThrough++
		w.WriteHeader(http.StatusTeapot)
	})
	bridge := NewGrpcWeb(grpcServer, func(origin string) bool { return origin == "https://allowed.example" })
	srv := httptest.NewServer(bridge.Handler(next))
	t.Cleanup(srv.Close)
	return srv, &passedThrough
}

func call(t *testing.T, srv *httptest.Server, method, contentType string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+method, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Grpc-Web", "1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGrpcWebUnary(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := call(t, srv, "/test.Echo/Unary", "application/grpc-web+proto", encodeFrame(t, wrapperspb.String("hi")), nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/grpc-web+proto" {
		t.Errorf("content-type %q", got)
	}
	if got := resp.Header.Get("Token"); got != "refreshed" {
		t.Errorf("custom header token = %q, want refreshed", got)
	}
	if exposed := resp.Header.Get("Access-Control-Expose-Headers"); !strings.Contains(exposed, "token") || !strings.Contains(exposed, "grpc-status") {
		t.Errorf("exposed headers %q", exposed)
	}
	if resp.Header.Get("Grpc-Status") != "" {
		t.Errorf("status must travel in the trailer frame once a message was sent, got header %q", resp.Header.Get("Grpc-Status"))
	}
	frames := parseFrames(t, readAll(t, resp.Body))
	if len(frames) != 2 || frames[0].trailer || !frames[1].trailer {
		t.Fatalf("frames: %+v", frames)
	}
	if got := decodeString(t, frames[0].payload); got != "echo: hi" {
		t.Errorf("message %q", got)
	}
	trailers := string(frames[1].payload)
	if !strings.Contains(trailers, "grpc-status: 0\r\n") || !strings.Contains(trailers, "x-echo-count: 1\r\n") {
		t.Errorf("trailer frame %q", trailers)
	}
	if strings.Contains(strings.ToLower(trailers), "trailer:") {
		t.Errorf("trailer frame leaks the http.TrailerPrefix: %q", trailers)
	}
}

func TestGrpcWebUnaryErrorIsTrailersOnly(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := call(t, srv, "/test.Echo/Unary", "application/grpc-web+proto", encodeFrame(t, wrapperspb.String("fail")), nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Grpc-Status"); got != fmt.Sprint(int(codes.InvalidArgument)) {
		t.Errorf("grpc-status header %q", got)
	}
	if got := resp.Header.Get("Grpc-Message"); got != "BAD_INPUT" {
		t.Errorf("grpc-message header %q", got)
	}
	if body := readAll(t, resp.Body); len(body) != 0 {
		t.Errorf("trailers-only response has a body: %x", body)
	}
}

func TestGrpcWebTrailersOnlyPreservesMetadata(t *testing.T) {
	setMetadata := func(ctx context.Context) error {
		if err := grpc.SetHeader(ctx, metadata.Pairs("x-shared", "header")); err != nil {
			return err
		}
		return grpc.SetTrailer(ctx, metadata.Pairs(
			"retry-after", "60",
			"x-shared", "trailer-1", "x-shared", "trailer-2",
			"opaque-bin", "\x00\xff\x01",
		))
	}
	desc := grpc.ServiceDesc{
		ServiceName: "test.Trailers",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Error",
			Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				if err := dec(&wrapperspb.StringValue{}); err != nil {
					return nil, err
				}
				if err := setMetadata(ctx); err != nil {
					return nil, err
				}
				return nil, status.Error(codes.ResourceExhausted, "RETRY_LATER")
			},
		}},
		Streams: []grpc.StreamDesc{{
			StreamName:    "Empty",
			ServerStreams: true,
			Handler: func(_ any, stream grpc.ServerStream) error {
				if err := stream.RecvMsg(&wrapperspb.StringValue{}); err != nil {
					return err
				}
				return setMetadata(stream.Context())
			},
		}},
	}
	server := grpc.NewServer()
	server.RegisterService(&desc, nil)
	t.Cleanup(server.Stop)
	bridge := NewGrpcWeb(server, func(origin string) bool { return origin == "https://allowed.example" })
	srv := httptest.NewServer(bridge.Handler(http.NotFoundHandler()))
	t.Cleanup(srv.Close)
	for _, contentType := range []string{"application/grpc-web+proto", "application/grpc-web-text+proto"} {
		for _, method := range []string{"Error", "Empty"} {
			t.Run(contentType+"/"+method, func(t *testing.T) {
				body := encodeFrame(t, wrapperspb.String("test"))
				if strings.HasPrefix(contentType, grpcWebTextContentType) {
					body = []byte(base64.StdEncoding.EncodeToString(body))
				}
				resp := call(t, srv, "/test.Trailers/"+method, contentType, body,
					map[string]string{"Origin": "https://allowed.example"})
				code, message := codes.OK, ""
				if method == "Error" {
					code, message = codes.ResourceExhausted, "RETRY_LATER"
				}
				if resp.StatusCode != http.StatusOK || resp.Header.Get("Grpc-Status") != fmt.Sprint(int(code)) || resp.Header.Get("Grpc-Message") != message {
					t.Errorf("unexpected status: HTTP %d, headers %v", resp.StatusCode, resp.Header)
				}
				for key, want := range map[string]string{
					"Content-Type":                contentType,
					"Retry-After":                 "60",
					"Opaque-Bin":                  base64.StdEncoding.EncodeToString([]byte{0, 255, 1}),
					"Access-Control-Allow-Origin": "https://allowed.example",
				} {
					if got := resp.Header.Get(key); got != want {
						t.Errorf("%s = %q, want %q", key, got, want)
					}
				}
				if got := resp.Header.Values("X-Shared"); !slices.Equal(got, []string{"header", "trailer-1", "trailer-2"}) {
					t.Errorf("shared metadata = %q", got)
				}
				exposed := strings.Split(resp.Header.Get("Access-Control-Expose-Headers"), ", ")
				for _, name := range []string{"retry-after", "x-shared", "opaque-bin"} {
					if !slices.Contains(exposed, name) {
						t.Errorf("custom metadata %q is not exposed to CORS clients", name)
					}
				}
				if body := readAll(t, resp.Body); len(body) != 0 {
					t.Errorf("trailers-only response has a body: %x", body)
				}
			})
		}
	}
}

func TestGrpcWebUnknownMethod(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := call(t, srv, "/test.Echo/Missing", "application/grpc-web+proto", encodeFrame(t, wrapperspb.String("x")), nil)
	if got := resp.Header.Get("Grpc-Status"); got != fmt.Sprint(int(codes.Unimplemented)) {
		t.Errorf("grpc-status header %q, want Unimplemented", got)
	}
}

func TestGrpcWebServerStream(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, tc := range []struct {
		input    string
		messages []string
		code     codes.Code
	}{
		{"abc", []string{"a", "b", "c"}, codes.OK},
		{"xy!", []string{"x", "y", "!"}, codes.Aborted},
	} {
		resp := call(t, srv, "/test.Echo/Stream", "application/grpc-web+proto", encodeFrame(t, wrapperspb.String(tc.input)), nil)
		frames := parseFrames(t, readAll(t, resp.Body))
		if len(frames) != len(tc.messages)+1 {
			t.Fatalf("%q: %d frames, want %d", tc.input, len(frames), len(tc.messages)+1)
		}
		for i, want := range tc.messages {
			if frames[i].trailer {
				t.Fatalf("%q: frame %d is a trailer", tc.input, i)
			}
			if got := decodeString(t, frames[i].payload); got != want {
				t.Errorf("%q: message %d = %q, want %q", tc.input, i, got, want)
			}
		}
		last := frames[len(frames)-1]
		if !last.trailer || !strings.Contains(string(last.payload), fmt.Sprintf("grpc-status: %d\r\n", tc.code)) {
			t.Errorf("%q: trailer frame %q", tc.input, last.payload)
		}
	}
}

func TestGrpcWebTextFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	body := base64.StdEncoding.EncodeToString(encodeFrame(t, wrapperspb.String("hi")))
	resp := call(t, srv, "/test.Echo/Unary", "application/grpc-web-text+proto", []byte(body), nil)

	if got := resp.Header.Get("Content-Type"); got != "application/grpc-web-text+proto" {
		t.Errorf("content-type %q", got)
	}
	// The body is a sequence of padded base64 chunks, one per flush.
	var decoded []byte
	for _, chunk := range strings.SplitAfter(string(readAll(t, resp.Body)), "=") {
		if chunk == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil {
			t.Fatalf("chunk %q: %v", chunk, err)
		}
		decoded = append(decoded, b...)
	}
	frames := parseFrames(t, decoded)
	if len(frames) != 2 || decodeString(t, frames[0].payload) != "echo: hi" || !frames[1].trailer {
		t.Fatalf("frames: %+v", frames)
	}
}

func TestGrpcWebCors(t *testing.T) {
	srv, _ := newTestServer(t)
	preflight := func(origin string) *http.Response {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/test.Echo/Unary", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", "x-grpc-web, content-type, authorization")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	resp := preflight("https://allowed.example")
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://allowed.example" {
		t.Errorf("allowed origin got %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("allow-credentials %q", got)
	}
	if got := strings.ToLower(resp.Header.Get("Access-Control-Allow-Headers")); !strings.Contains(got, "authorization") {
		t.Errorf("allow-headers %q", got)
	}

	resp = preflight("https://evil.example")
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("rejected origin still got %q", got)
	}

	// An actual cross-origin call from an allowed origin carries the CORS
	// headers next to the gRPC-Web ones.
	resp = call(t, srv, "/test.Echo/Unary", "application/grpc-web+proto", encodeFrame(t, wrapperspb.String("hi")),
		map[string]string{"Origin": "https://allowed.example"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://allowed.example" {
		t.Errorf("call: allow-origin %q", got)
	}
	if got := resp.Header.Get("Token"); got != "refreshed" {
		t.Errorf("call: token %q", got)
	}
}

func TestGrpcWebPassesOtherRequestsThrough(t *testing.T) {
	srv, passedThrough := newTestServer(t)
	for _, r := range []struct{ method, contentType string }{
		{http.MethodGet, ""},
		{http.MethodPost, "application/json"},
		{http.MethodOptions, ""},
	} {
		req, _ := http.NewRequest(r.method, srv.URL+"/test.Echo/Unary", nil)
		if r.contentType != "" {
			req.Header.Set("Content-Type", r.contentType)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTeapot {
			t.Errorf("%s %q: status %d, want the next handler's", r.method, r.contentType, resp.StatusCode)
		}
	}
	if *passedThrough != 3 {
		t.Errorf("next handler ran %d times, want 3", *passedThrough)
	}
}

// Streams must reach the client message by message, so every message is
// flushed and nginx is told not to buffer.
func TestGrpcWebFlushesEachMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec, grpcWebContentType, false)
	rw.Header().Set("Content-Type", "application/grpc+proto")
	rw.Flush() // before any header: must not commit a bare 200
	if rec.Flushed || rec.Header().Get("Content-Type") != "" {
		t.Fatal("flushed before headers were written")
	}
	if _, err := rw.Write([]byte{0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	rw.Flush()
	if !rec.Flushed {
		t.Error("message not flushed")
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/grpc-web+proto" {
		t.Errorf("content-type %q", got)
	}
}
