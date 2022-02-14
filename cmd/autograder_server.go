package main

import (
	autograder_pb "autograder-server/pkg/api/proto"
	autograder_grpc "autograder-server/pkg/grpc"
	"autograder-server/pkg/mailer"
	"autograder-server/pkg/middleware"
	"autograder-server/pkg/storage"
	"autograder-server/pkg/web"
	"github.com/go-chi/chi"
	chiMiddleware "github.com/go-chi/chi/middleware"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_zap "github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	grpc_recovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	grpc_opentracing "github.com/grpc-ecosystem/go-grpc-middleware/tracing/opentracing"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/kataras/hcaptcha"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"io/fs"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

var (
	zapLogger *zap.Logger
)

func readConfig() {
	viper.SetConfigName("config")
	viper.SetConfigType("toml")
	viper.AddConfigPath("/etc/autograder-server/")  // path to look for the config file in
	viper.AddConfigPath("$HOME/.autograder-server") // call multiple times to add many search paths
	viper.AddConfigPath(".")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	err := viper.ReadInConfig()
	if err != nil {
		panic(err)
	}
}

func main() {
	var err error
	rand.Seed(time.Now().UnixNano())
	readConfig()
	ls := &storage.LocalStorage{}
	m := mailer.NewSMTPMailer(viper.GetString("smtp-addr"), viper.GetString("smtp-user"), viper.GetString("smtp-pass"))
	distFS, err := fs.Sub(web.WebResources, "dist")
	if err != nil {
		panic(err)
	}
	hcaptchaClient := hcaptcha.New(viper.GetString("hcaptcha-secret"))
	hcaptchaClient.HTTPClient.Timeout = 30 * time.Second
	githubOauth2Config := &oauth2.Config{
		ClientID:     viper.GetString("github-client-id"),
		ClientSecret: viper.GetString("github-client-secret"),
		Scopes:       []string{"user:email", "read:user"},
		Endpoint:     github.Endpoint,
	}
	corsHandler := cors.New(cors.Options{
		AllowOriginFunc: func(origin string) bool {
			return true
		},
		AllowedHeaders:   []string{"Upload-token", "Download-token"},
		ExposedHeaders:   nil,  // make sure that this is *nil*, otherwise the WebResponse overwrite will not work.
		AllowCredentials: true, // always allow credentials, otherwise :authorization headers won't work
		MaxAge:           int(10 * time.Minute / time.Second),
	})
	zapLogger, err = zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	defer zapLogger.Sync()
	grpc_zap.ReplaceGrpcLoggerV2(zapLogger)
	grpcServer := grpc.NewServer(
		grpc_middleware.WithUnaryServerChain(
			grpc_ctxtags.UnaryServerInterceptor(grpc_ctxtags.WithFieldExtractor(grpc_ctxtags.CodeGenRequestFieldExtractor)),
			grpc_opentracing.UnaryServerInterceptor(),
			grpc_prometheus.UnaryServerInterceptor,
			grpc_zap.UnaryServerInterceptor(zapLogger),
			autograder_grpc.UnaryAuth(),
			grpc_recovery.UnaryServerInterceptor(),
		),
		grpc_middleware.WithStreamServerChain(
			grpc_ctxtags.StreamServerInterceptor(grpc_ctxtags.WithFieldExtractor(grpc_ctxtags.CodeGenRequestFieldExtractor)),
			grpc_opentracing.StreamServerInterceptor(),
			grpc_prometheus.StreamServerInterceptor,
			grpc_zap.StreamServerInterceptor(zapLogger),
			autograder_grpc.StreamAuth(),
			grpc_recovery.StreamServerInterceptor(),
		),
	)
	autograderService := autograder_grpc.NewAutograderServiceServer(ls, m, hcaptchaClient, githubOauth2Config)
	autograder_pb.RegisterAutograderServiceServer(grpcServer, autograderService)
	wrappedGrpc := grpcweb.WrapServer(grpcServer, grpcweb.WithOriginFunc(func(origin string) bool {
		return true
	}), grpcweb.WithWebsockets(true), grpcweb.WithWebsocketOriginFunc(func(r *http.Request) bool {
		return true
	}))
	router := chi.NewRouter()
	router.Use(chiMiddleware.RealIP, chiMiddleware.Logger, chiMiddleware.Recoverer, middleware.NewGrpcWebMiddleware(wrappedGrpc).Handler)
	router.Get("/metrics", promhttp.Handler().ServeHTTP)
	router.Options("/AutograderService/FileUpload", corsHandler.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP)
	router.Post("/AutograderService/FileUpload", corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileUpload)).ServeHTTP)
	router.Get("/AutograderService/FileDownload/{filename}", corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileDownload)).ServeHTTP)
	fsrv := http.FileServer(http.FS(distFS))

	router.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if len(r.URL.Path) > 1 {
			p = r.URL.Path[1:]
		}
		_, err := distFS.Open(p)
		if err != nil {
			http.StripPrefix(r.URL.Path, fsrv).ServeHTTP(w, r)
		} else {
			fsrv.ServeHTTP(w, r)
		}
	}))

	if err := http.ListenAndServe(":9315", router); err != nil {
		grpclog.Fatalf("Failed starting http2 server: %v", err)
	}
}
