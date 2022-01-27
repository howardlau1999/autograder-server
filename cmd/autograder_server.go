package main

import (
	"autograder-server/pkg"
	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/middleware"
	"autograder-server/pkg/storage"
	"fmt"
	"github.com/go-chi/chi"
	chiMiddleware "github.com/go-chi/chi/middleware"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/rs/cors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"net/http"
	"strings"
	"time"
)

func main() {
	ls := &storage.LocalStorage{}
	corsHandler := cors.New(cors.Options{
		AllowOriginFunc: func(origin string) bool {
			return true
		},
		AllowedHeaders:   []string{"Upload-token"},
		ExposedHeaders:   nil,  // make sure that this is *nil*, otherwise the WebResponse overwrite will not work.
		AllowCredentials: true, // always allow credentials, otherwise :authorization headers won't work
		MaxAge:           int(10 * time.Minute / time.Second),
	})

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
		w.WriteHeader(http.StatusOK)
	})
	router.Options("/AutograderService/FileUpload", corsHandler.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

	})).ServeHTTP)
	router.Post("/AutograderService/FileUpload", corsHandler.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		normalizedContentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-type")))
		//uploadTokenString := strings.TrimSpace(r.Header.Get("Upload-token"))
		//uploadToken, err := jwt.Parse(uploadTokenString, func(token *jwt.Token) (interface{}, error) {
		//	if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
		//		return nil, fmt.Errorf("unexpected singning method: %v", token.Header["alg"])
		//	}
		//
		//	return []byte("upload-token-sign-secret"), nil
		//})
		//if err != nil {
		//	w.WriteHeader(http.StatusBadRequest)
		//	return
		//}
		//
		//claims, ok := uploadToken.Claims.(jwt.MapClaims)
		//if !ok || !uploadToken.Valid || claims.Valid() != nil {
		//	w.WriteHeader(http.StatusBadRequest)
		//	return
		//}
		//payloadString, ok := claims["payload"].(string)
		//if !ok {
		//	w.WriteHeader(http.StatusBadRequest)
		//	return
		//}
		//
		//payload, err := base64.StdEncoding.DecodeString(payloadString)
		//if !ok {
		//	w.WriteHeader(http.StatusBadRequest)
		//	return
		//}
		//
		//var payloadPB model_pb.UploadTokenPayload
		//err = proto.Unmarshal(payload, &payloadPB)
		//if err != nil {
		//	w.WriteHeader(http.StatusBadRequest)
		//	return
		//}

		if !strings.HasPrefix(normalizedContentType, "multipart/form-data; boundary") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		err = r.ParseMultipartForm(10 * 1024 * 1024)
		if err != nil {
			grpclog.Errorf("Parse upload multipart form error: %v\n", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		uploadFile, header, err := r.FormFile("file")
		if err != nil {
			grpclog.Errorf("Get form file error: %v\n", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fileHeader := make([]byte, 512)
		_, err = uploadFile.Read(fileHeader)
		_, err = uploadFile.Seek(0, 0)
		fileContentType := http.DetectContentType(fileHeader)
		grpclog.Infof("mime_header = %v, detected_content_type = %s",
			header.Header, fileContentType)
		err = ls.Put(
			r.Context(),
			fmt.Sprintf("uploads/%d/%d", 1, 1),
			fmt.Sprintf("%d", time.Now().UnixNano()),
			uploadFile)
		if err != nil {
			grpclog.Errorf("failed to put file: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	})).ServeHTTP)
	if err := http.ListenAndServe(":9315", router); err != nil {
		grpclog.Fatalf("Failed starting http2 server: %v", err)
	}
}
