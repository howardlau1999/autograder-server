package grpc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strconv"

	grader_pb "autograder-server/pkg/grader/proto"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Metadata keys a grader must attach to every call after registration.
// gRPC lower-cases metadata keys, so these are spelled in lower case.
const (
	MetadataHubToken     = "token"
	MetadataGraderId     = "graderid"
	MetadataSessionToken = "session-token"
)

type graderIdentityCtxKey struct{}

// GraderIdFromContext returns the grader id authenticated by the hub
// interceptors for the current call.
func GraderIdFromContext(ctx context.Context) (uint64, bool) {
	id, ok := ctx.Value(graderIdentityCtxKey{}).(uint64)
	return id, ok
}

type graderIdGetter interface {
	GetGraderId() uint64
}

func (g *GraderHubService) newSession(graderId uint64) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	g.sessionsMu.Lock()
	g.sessions[graderId] = token
	g.sessionsMu.Unlock()
	return token, nil
}

func (g *GraderHubService) checkSession(graderId uint64, token string) bool {
	g.sessionsMu.Lock()
	expected, ok := g.sessions[graderId]
	g.sessionsMu.Unlock()
	if !ok || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1
}

func (g *GraderHubService) checkHubToken(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(g.token)) == 1
}

// sessionFromMetadata reports the grader id carried in md if md also carries a
// valid session token for it.
func (g *GraderHubService) sessionFromMetadata(md metadata.MD) (uint64, bool) {
	ids := md.Get(MetadataGraderId)
	tokens := md.Get(MetadataSessionToken)
	if len(ids) != 1 || len(tokens) != 1 {
		return 0, false
	}
	graderId, err := strconv.ParseUint(ids[0], 10, 64)
	if err != nil || !g.checkSession(graderId, tokens[0]) {
		return 0, false
	}
	return graderId, true
}

// authenticate enforces the hub's access rules for one RPC. RegisterGrader is
// admitted on the shared hub token alone; every other method additionally
// requires the grader id and session token issued at registration, and a
// request body naming a grader id must name the authenticated one.
func (g *GraderHubService) authenticate(ctx context.Context, fullMethod string, req interface{}) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if fullMethod == grader_pb.GraderHubService_RegisterGrader_FullMethodName {
		r, ok := req.(*grader_pb.RegisterGraderRequest)
		if !ok || !g.checkHubToken(r.GetToken()) {
			return nil, status.Error(codes.PermissionDenied, "INVALID_TOKEN")
		}
		// A grader re-registering with its current session is allowed to
		// take over its own name even if it still looks online.
		if graderId, ok := g.sessionFromMetadata(md); ok {
			ctx = context.WithValue(ctx, graderIdentityCtxKey{}, graderId)
		}
		return ctx, nil
	}
	tokens := md.Get(MetadataHubToken)
	if len(tokens) != 1 || !g.checkHubToken(tokens[0]) {
		return nil, status.Error(codes.Unauthenticated, "INVALID_TOKEN")
	}
	graderId, ok := g.sessionFromMetadata(md)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "INVALID_SESSION")
	}
	if r, ok := req.(graderIdGetter); ok && r.GetGraderId() != graderId {
		return nil, status.Error(codes.PermissionDenied, "GRADER_ID_MISMATCH")
	}
	return context.WithValue(ctx, graderIdentityCtxKey{}, graderId), nil
}

// UnaryAuthInterceptor authenticates unary GraderHubService calls.
func (g *GraderHubService) UnaryAuthInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (interface{}, error) {
		ctx, err := g.authenticate(ctx, info.FullMethod, req)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamAuthInterceptor authenticates streaming GraderHubService calls. Stream
// requests carry no body up front, so the grader id is taken from metadata
// only; handlers must then use GraderIdFromContext instead of trusting ids
// embedded in individual messages.
func (g *GraderHubService) StreamAuthInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := g.authenticate(ss.Context(), info.FullMethod, nil)
		if err != nil {
			return err
		}
		wrapped := grpc_middleware.WrapServerStream(ss)
		wrapped.WrappedContext = ctx
		return handler(srv, wrapped)
	}
}
