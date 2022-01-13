package main

import (
	"autograder-server/pkg"
	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/middleware"
	"github.com/go-chi/chi"
	chiMiddleware "github.com/go-chi/chi/middleware"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"net/http"
)

func main() {
	grpcServer := grpc.NewServer()
	autograderService := pkg.NewAutograderServiceServer()
	autograder_pb.RegisterAutograderServiceServer(grpcServer, autograderService)
	wrappedGrpc := grpcweb.WrapServer(grpcServer, grpcweb.WithOriginFunc(func(origin string) bool {
		return true
	}), grpcweb.WithWebsockets(true), grpcweb.WithWebsocketOriginFunc(func(r *http.Request) bool {
		return true
	}))
	router := chi.NewRouter()
	router.Use(chiMiddleware.Logger, chiMiddleware.Recoverer, middleware.NewGrpcWebMiddleware(wrappedGrpc).Handler)
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	if err := http.ListenAndServe(":9315", router); err != nil {
		grpclog.Fatalf("Failed starting http2 server: %v", err)
	}
}
