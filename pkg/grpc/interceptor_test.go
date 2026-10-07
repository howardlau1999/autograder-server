package grpc

import (
	"strings"
	"testing"

	autograder_pb "autograder-server/pkg/api/proto"
)

// TestAuthTableCoversEveryMethod fails when an RPC exists in the service
// descriptor but has no entry in the auth table. Unregistered methods are
// denied at runtime (fail-closed), but that would surface as a broken endpoint
// rather than a test failure without this check.
func TestAuthTableCoversEveryMethod(t *testing.T) {
	svc := &AutograderService{}
	svc.initAuthFuncs()

	desc := autograder_pb.AutograderService_ServiceDesc
	for _, m := range desc.Methods {
		full := "/" + desc.ServiceName + "/" + m.MethodName
		if svc.authFuncs[full] == nil {
			t.Errorf("unary method %s has no entry in the auth table", full)
		}
	}
	for _, s := range desc.Streams {
		full := "/" + desc.ServiceName + "/" + s.StreamName
		if svc.authFuncs[full] == nil {
			t.Errorf("stream method %s has no entry in the auth table", full)
		}
	}
}

// TestAuthTableKeysAreFullMethodNames pins the table keys to the
// /Service/Method form that gRPC passes to interceptors; a mismatch would make
// AuthFunc reject every request.
func TestAuthTableKeysAreFullMethodNames(t *testing.T) {
	svc := &AutograderService{}
	svc.initAuthFuncs()

	prefix := "/" + autograder_pb.AutograderService_ServiceDesc.ServiceName + "/"
	for key := range svc.authFuncs {
		if !strings.HasPrefix(key, prefix) {
			t.Errorf("auth table key %q does not start with %q", key, prefix)
		}
	}
	if svc.authFuncs[autograder_pb.AutograderService_Login_FullMethodName] == nil {
		t.Error("Login is not registered under its generated FullMethodName constant")
	}
}
