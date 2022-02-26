package main

import (
	autograder_pb "autograder-server/pkg/api/proto"
	autograder_grpc "autograder-server/pkg/grpc"
	"autograder-server/pkg/mailer"
	"autograder-server/pkg/middleware"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"autograder-server/pkg/storage"
	"autograder-server/pkg/web"
	"bytes"
	"context"
	"fmt"
	"github.com/cockroachdb/pebble"
	"github.com/go-chi/chi"
	chiMiddleware "github.com/go-chi/chi/middleware"
	"github.com/go-chi/httprate"
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
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	zapLogger *zap.Logger
)

type ServerProvidedTokens struct {
	ServerProvided  string
	HcaptchaSiteKey string
	GithubClientId  string
}

const initialConfig = `
[smtp]
    addr=""
    user=""
    pass=""
    from=""

[hcaptcha]
    site-key=""
    secret=""

[github]
    client-id=""
    client-secret=""
`

const dbPath = "db"

var initializeMarker = []byte("__autograder_initialized")
var letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func RandStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func dbInit(db *pebble.DB, email string) bool {
	if !isDatabaseUninitialized(db) {
		log.Printf("Database is already initialized. If you want to initialize again please delete the database manually.")
		return false
	}
	rootPassword := RandStringRunes(16)
	userRepo := repository.NewKVUserRepository(db)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(rootPassword), bcrypt.DefaultCost)
	rootUser := &model_pb.User{Username: "root", Password: passwordHash, Email: email, Nickname: "root"}
	_, err = userRepo.CreateUser(context.Background(), rootUser)
	if err != nil {
		panic(err)
	}
	err = db.Set(initializeMarker, nil, pebble.Sync)
	if err != nil {
		panic(err)
	}

	log.Printf("Database initialized. Root user information\nUsername: root\nPassword: %s", rootPassword)
	return true
}

type EnvKeyReplacer struct {
}

func (r *EnvKeyReplacer) Replace(s string) string {
	v := strings.ReplaceAll(s, ".", "_")
	return strings.ReplaceAll(v, "-", "_")
}

func readConfig() {
	*viper.GetViper() = *viper.NewWithOptions(viper.EnvKeyReplacer(&EnvKeyReplacer{}))
	viper.SetConfigName("config")
	viper.SetConfigType("toml")
	viper.AddConfigPath("/etc/autograder-server/")  // path to look for the config file in
	viper.AddConfigPath("$HOME/.autograder-server") // call multiple times to add many search paths
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	viper.SetDefault("grader.concurrency", 5)
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	err := viper.ReadInConfig()
	if err != nil {
		panic(err)
	}
}

func isDatabaseUninitialized(db *pebble.DB) bool {
	_, closer, err := db.Get(initializeMarker)
	if err == nil {
		defer closer.Close()
	}
	return err == pebble.ErrNotFound
}

func main() {
	rand.Seed(time.Now().UnixNano())
	var isInit bool
	var initEmail string
	var err error
	var port int
	var printUsage bool
	pflag.BoolVar(&isInit, "init", false, "Pass this flag to initialize database.")
	pflag.StringVar(&initEmail, "email", "", "The email for the initialized root user.")
	pflag.IntVar(&port, "port", 4200, "The port to listen on.")
	pflag.BoolVar(&printUsage, "help", false, "Print this message.")
	pflag.Parse()
	if printUsage {
		pflag.Usage()
		return
	}
	db, err := pebble.Open(dbPath, &pebble.Options{Merger: repository.NewKVMerger()})
	if err != nil {
		panic(err)
	}
	if isInit {
		if initEmail == "" {
			log.Printf("Please provide email.")
			return
		}
		success := dbInit(db, initEmail)
		if !success {
			return
		}
		_, err = os.Stat("config.toml")
		if !os.IsNotExist(err) {
			log.Printf("Failed to write initial config file.")
			return
		}
		f, err := os.OpenFile("config.toml", os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			panic(err)
		}
		_, err = f.Write([]byte(initialConfig))
		if err != nil {
			panic(err)
		}
		return
	}
	if isDatabaseUninitialized(db) {
		log.Printf("Database is not initialized. Please run autograder-server --init first.")
		return
	}
	readConfig()
	ls := &storage.LocalStorage{}
	m := mailer.NewSMTPMailer(viper.GetString("smtp.addr"), viper.GetString("smtp.user"), viper.GetString("smtp.pass"))
	distFS, err := fs.Sub(web.WebResources, "dist")
	if err != nil {
		panic(err)
	}
	hcaptchaClient := hcaptcha.New(viper.GetString("hcaptcha.secret-key"))
	hcaptchaClient.HTTPClient.Timeout = 30 * time.Second
	githubOauth2Config := &oauth2.Config{
		ClientID:     viper.GetString("github.client-id"),
		ClientSecret: viper.GetString("github.client-secret"),
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
	autograderService := autograder_grpc.NewAutograderServiceServer(db, ls, m, hcaptchaClient, githubOauth2Config)
	autograder_pb.RegisterAutograderServiceServer(grpcServer, autograderService)
	wrappedGrpc := grpcweb.WrapServer(grpcServer, grpcweb.WithOriginFunc(func(origin string) bool {
		return true
	}), grpcweb.WithWebsockets(true), grpcweb.WithWebsocketOriginFunc(func(r *http.Request) bool {
		return true
	}))
	router := chi.NewRouter()
	router.Use(
		chiMiddleware.RealIP,
		httprate.LimitByIP(100, 3*time.Second),
		chiMiddleware.Logger,
		chiMiddleware.Recoverer,
		middleware.NewGrpcWebMiddleware(wrappedGrpc).Handler,
	)
	router.Get("/metrics", promhttp.Handler().ServeHTTP)
	router.Options("/AutograderService/FileUpload", corsHandler.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP)
	router.Post("/AutograderService/FileUpload", corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileUpload)).ServeHTTP)
	router.Get("/AutograderService/FileDownload/{filename}", corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileDownload)).ServeHTTP)
	providedTokens := &ServerProvidedTokens{
		ServerProvided:  "true",
		HcaptchaSiteKey: viper.GetString("hcaptcha.site-key"),
		GithubClientId:  viper.GetString("github.client-id"),
	}
	tmpl, err := template.ParseFS(distFS, "index.html")
	var writeTemplate func(w http.ResponseWriter, r *http.Request)
	if err == nil {
		rendered := &bytes.Buffer{}
		tmpl.Execute(rendered, providedTokens)
		writeTemplate = func(w http.ResponseWriter, r *http.Request) {
			io.Copy(w, rendered)
		}
	}
	fsrv := http.FileServer(http.FS(distFS))

	router.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if len(r.URL.Path) > 1 {
			p = r.URL.Path[1:]
		}
		_, err := distFS.Open(p)
		if err != nil {
			if writeTemplate != nil {
				writeTemplate(w, r)
				return
			}
			http.StripPrefix(r.URL.Path, fsrv).ServeHTTP(w, r)
		} else {
			fsrv.ServeHTTP(w, r)
		}
	}))

	grpclog.Infof("Server listening on port %d. Open http://localhost:%d", port, port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), router); err != nil {
		grpclog.Fatalf("Failed starting http2 server: %v", err)
	}
}
