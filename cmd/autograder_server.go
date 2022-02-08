package main

import (
	autograder_pb "autograder-server/pkg/api/proto"
	autograder_grpc "autograder-server/pkg/grpc"
	"autograder-server/pkg/middleware"
	"autograder-server/pkg/storage"
	"github.com/go-chi/chi"
	chiMiddleware "github.com/go-chi/chi/middleware"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/rs/cors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"net/http"
	"time"
)

func main() {
	ls := &storage.LocalStorage{}
	corsHandler := cors.New(cors.Options{
		AllowOriginFunc: func(origin string) bool {
			return true
		},
		AllowedHeaders:   []string{"Upload-token", "Download-token"},
		ExposedHeaders:   nil,  // make sure that this is *nil*, otherwise the WebResponse overwrite will not work.
		AllowCredentials: true, // always allow credentials, otherwise :authorization headers won't work
		MaxAge:           int(10 * time.Minute / time.Second),
	})

	grpcServer := grpc.NewServer()
	autograderService := autograder_grpc.NewAutograderServiceServer(ls)
	autograder_pb.RegisterAutograderServiceServer(grpcServer, autograderService)
	wrappedGrpc := grpcweb.WrapServer(grpcServer, grpcweb.WithOriginFunc(func(origin string) bool {
		return true
	}), grpcweb.WithWebsockets(true), grpcweb.WithWebsocketOriginFunc(func(r *http.Request) bool {
		return true
	}))
	router := chi.NewRouter()
	router.Use(chiMiddleware.Logger, chiMiddleware.Recoverer, middleware.NewGrpcWebMiddleware(wrappedGrpc).Handler)
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Options("/AutograderService/FileUpload", corsHandler.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP)
	router.Post("/AutograderService/FileUpload", corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileUpload)).ServeHTTP)
	router.Get("/AutograderService/FileDownload/{filename}", corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileDownload)).ServeHTTP)
	if err := http.ListenAndServe(":9315", router); err != nil {
		grpclog.Fatalf("Failed starting http2 server: %v", err)
	}
}
