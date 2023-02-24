package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	grader_grpc "autograder-server/pkg/grader/grpc"
	grader_pb "autograder-server/pkg/grader/proto"
	autograder_grpc "autograder-server/pkg/grpc"
	"autograder-server/pkg/logging"
	"autograder-server/pkg/mailer"
	"autograder-server/pkg/middleware"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"autograder-server/pkg/storage"
	"autograder-server/pkg/web"
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
	"google.golang.org/grpc/keepalive"
)

type ServerProvidedTokens struct {
	ServerProvided  string
	HcaptchaSiteKey string
	GithubClientId  string
}

const initialConfig = `
[server]
	development=false

[smtp]
    addr=""
    user=""
    pass=""
    from=""

[mailgun]
	enabled = false
	domain = ""
	api-key = ""

[hub]
	port=9999
	token=""
    heartbeat-timeout="15s"

[web]
	port=9315

[fs.http]
	port=19999
	token=""

[fs.local]
	dir=""

[hcaptcha]
    site-key=""
    secret=""

[github]
    client-id=""
    client-secret=""

[metrics]
	port=29999
    path="/metrics"

[db.local]
	path="db"

[token.secret]
	session="user-session-token-secret"
	upload="upload-token-secret"
	download="download-token-secret"

[log]
	development=false
	level="info"
	file="server.log"
`

var initializeMarker = []byte("__autograder_initialized")
var passwordRunes = []rune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func RandStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = passwordRunes[rand.Intn(len(passwordRunes))]
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
	rootUser := &model_pb.User{Username: "root", Password: passwordHash, Email: email, Nickname: "root", IsAdmin: true}
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

type ServerEnvKeyReplacer struct {
}

func (r *ServerEnvKeyReplacer) Replace(s string) string {
	v := strings.ReplaceAll(s, ".", "_")
	return strings.ReplaceAll(v, "-", "_")
}

func serverReadConfig() {
	*viper.GetViper() = *viper.NewWithOptions(viper.EnvKeyReplacer(&ServerEnvKeyReplacer{}))
	viper.SetConfigName("config")
	viper.SetConfigType("toml")
	viper.AddConfigPath("/etc/autograder-server/")  // path to look for the config file in
	viper.AddConfigPath("$HOME/.autograder-server") // call multiple times to add many search paths
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	viper.SetDefault("hub.port", "9999")
	viper.SetDefault("fs.http.port", "19999")
	viper.SetDefault("web.port", "9315")
	viper.SetDefault("hub.heartbeat-timeout", "15s")
	viper.SetDefault("metrics.port", "29999")
	viper.SetDefault("metrics.path", "/metrics")
	viper.SetDefault("db.local.path", "db")
	viper.SetDefault("server.development", false)
	viper.SetDefault("token.secret.session", "user-session-token-secret")
	viper.SetDefault("token.secret.upload", "upload-token-secret")
	viper.SetDefault("token.secret.download", "download-token-secret")
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.file", "server.log")
	viper.SetDefault("log.development", "false")

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

func initEmbeddedStaticWebResources(router chi.Router) {
	distFS, err := fs.Sub(web.WebResources, "dist")
	if err != nil {
		panic(err)
	}
	providedTokens := &ServerProvidedTokens{
		ServerProvided:  "true",
		HcaptchaSiteKey: viper.GetString("hcaptcha.site-key"),
		GithubClientId:  viper.GetString("github.client-id"),
	}
	tmpl, err := template.ParseFS(distFS, "index.html")
	var writeTemplate func(w http.ResponseWriter, r *http.Request)
	if err == nil {
		rendered := &bytes.Buffer{}
		err = tmpl.Execute(rendered, providedTokens)
		if err != nil {
			panic(err)
		}
		indexHTML := rendered.Bytes()
		writeTemplate = func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexHTML)
		}
	}
	fsrv := http.FileServer(http.FS(distFS))

	router.Get(
		"/*", func(w http.ResponseWriter, r *http.Request) {
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
		},
	)
}

func processCommandLineOptions() bool {
	var isInit bool
	var initEmail string
	var printUsage bool
	var printConfigTemplate bool
	pflag.BoolVar(&isInit, "init", false, "Pass this flag to initialize database.")
	pflag.StringVar(&initEmail, "email", "", "The email for the initialized root user.")
	pflag.BoolVar(&printConfigTemplate, "config", false, "Print config template.")
	pflag.BoolVar(&printUsage, "help", false, "Print this message.")
	pflag.Parse()
	if printUsage {
		pflag.Usage()
		return true
	}
	if printConfigTemplate {
		fmt.Print(initialConfig)
		return true
	}

	if isInit {
		db, err := pebble.Open(viper.GetString("db.local.path"), &pebble.Options{Merger: repository.NewKVMerger()})
		if err != nil {
			panic(err)
		}
		if initEmail == "" {
			log.Printf("Please provide email.")
			return true
		}
		success := dbInit(db, initEmail)
		if !success {
			return true
		}
		return true
	}
	return false
}

func main() {
	rand.Seed(time.Now().UnixNano())
	serverReadConfig()
	zapLogger := logging.Init(
		viper.GetString("log.level"), viper.GetString("log.file"), viper.GetBool("log.development"),
	)
	defer zapLogger.Sync()
	if processCommandLineOptions() {
		return
	}
	db, err := pebble.Open(viper.GetString("db.local.path"), &pebble.Options{Merger: repository.NewKVMerger()})
	if err != nil {
		zap.L().Fatal("DB.Open", zap.Error(err))
	}
	if isDatabaseUninitialized(db) {
		log.Printf("Database is not initialized. Please run autograder-server --init first.")
		return
	}
	heartbeatTimeout, err := time.ParseDuration(viper.GetString("hub.heartbeat-timeout"))
	if err != nil {
		zap.L().Fatal("Hub.HeartbeatTimeout.Invalid", zap.Error(err))
	}

	localStorageDir := viper.GetString("fs.local.dir")
	if localStorageDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			zap.L().Fatal("OS.Getwd", zap.Error(err))
		}
		localStorageDir = cwd
	}
	localStorage := storage.NewLocalStorage(localStorageDir)

	// Setup external services
	var m mailer.Mailer
	if viper.GetBool("mailgun.enabled") {
		m = mailer.NewMailgunMailer(viper.GetString("mailgun.domain"), viper.GetString("mailgun.api-key"))
	} else {
		m = mailer.NewSMTPMailer(
			viper.GetString("smtp.addr"), viper.GetString("smtp.user"), viper.GetString("smtp.pass"),
		)
	}
	hcaptchaClient := hcaptcha.New(viper.GetString("hcaptcha.secret-key"))
	hcaptchaClient.HTTPClient.Timeout = 30 * time.Second
	githubOauth2Config := &oauth2.Config{
		ClientID:     viper.GetString("github.client-id"),
		ClientSecret: viper.GetString("github.client-secret"),
		Scopes:       []string{"user:email", "read:user"},
		Endpoint:     github.Endpoint,
	}

	corsHandler := cors.New(
		cors.Options{
			AllowOriginFunc: func(origin string) bool {
				return true
			},
			AllowedHeaders:   []string{"Upload-token", "Download-token"},
			ExposedHeaders:   nil,  // make sure that this is *nil*, otherwise the WebResponse overwrite will not work.
			AllowCredentials: true, // always allow credentials, otherwise :authorization headers won't work
			MaxAge:           int(10 * time.Minute / time.Second),
		},
	)
	grpc_zap.ReplaceGrpcLoggerV2(zapLogger)
	autograderServer := grpc.NewServer(
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
	kaep := keepalive.EnforcementPolicy{PermitWithoutStream: true, MinTime: 1 * time.Second}
	ksap := keepalive.ServerParameters{Time: 5 * time.Second, Timeout: 1 * time.Hour}
	graderHubServer := grpc.NewServer(
		grpc.KeepaliveEnforcementPolicy(kaep),
		grpc.KeepaliveParams(ksap),
		grpc_middleware.WithUnaryServerChain(
			grpc_ctxtags.UnaryServerInterceptor(grpc_ctxtags.WithFieldExtractor(grpc_ctxtags.CodeGenRequestFieldExtractor)),
			grpc_opentracing.UnaryServerInterceptor(),
			grpc_prometheus.UnaryServerInterceptor,
			grpc_zap.UnaryServerInterceptor(zapLogger),
			grpc_recovery.UnaryServerInterceptor(),
		),
		grpc_middleware.WithStreamServerChain(
			grpc_ctxtags.StreamServerInterceptor(grpc_ctxtags.WithFieldExtractor(grpc_ctxtags.CodeGenRequestFieldExtractor)),
			grpc_opentracing.StreamServerInterceptor(),
			grpc_prometheus.StreamServerInterceptor,
			grpc_zap.StreamServerInterceptor(zapLogger),
			grpc_recovery.StreamServerInterceptor(),
		),
	)
	srr := repository.NewKVSubmissionReportRepository(db)
	graderHubService := grader_grpc.NewGraderHubService(db, srr, viper.GetString("hub.token"), heartbeatTimeout)
	autograderService := autograder_grpc.NewAutograderServiceServer(
		db, localStorage, m, hcaptchaClient, githubOauth2Config, srr, graderHubService,
	)
	autograder_pb.RegisterAutograderServiceServer(autograderServer, autograderService)
	grader_pb.RegisterGraderHubServiceServer(graderHubServer, graderHubService)
	wrappedGrpc := grpcweb.WrapServer(
		autograderServer, grpcweb.WithOriginFunc(
			func(origin string) bool {
				return true
			},
		), grpcweb.WithWebsockets(true), grpcweb.WithWebsocketOriginFunc(
			func(r *http.Request) bool {
				return true
			},
		),
	)
	router := chi.NewRouter()
	router.Use(
		chiMiddleware.RealIP,
		httprate.LimitByIP(100, 3*time.Second),
		chiMiddleware.Logger,
		chiMiddleware.Recoverer,
		middleware.NewGrpcWebMiddleware(wrappedGrpc).Handler,
	)
	router.Options(
		"/AutograderService/FileUpload",
		corsHandler.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP,
	)
	router.Post(
		"/AutograderService/FileUpload",
		corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileUpload)).ServeHTTP,
	)
	router.Get(
		"/AutograderService/FileDownload/{filename}",
		corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileDownload)).ServeHTTP,
	)
	initEmbeddedStaticWebResources(router)

	// Communicate with graders
	graderHubPort := viper.GetInt("hub.port")
	zap.L().Info("GraderHub.Listen", zap.Int("port", graderHubPort))
	graderHubLis, err := net.Listen("tcp", fmt.Sprintf(":%d", graderHubPort))
	if err != nil {
		zap.L().Fatal("Server.Grader.Listen", zap.Error(err))
	}
	go func() {
		err := graderHubServer.Serve(graderHubLis)
		if err != nil {
			zap.L().Fatal("Server.Grader.Serve", zap.Error(err))
		}
	}()

	// Prometheus metrics
	metricsPort := viper.GetInt("metrics.port")
	zap.L().Info("Metrics.Listen", zap.Int("port", metricsPort))
	metricsRouter := chi.NewRouter()
	metricsRouter.Handle(viper.GetString("metrics.path"), promhttp.Handler())
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), metricsRouter); err != nil {
			zap.L().Error("Metrics.Serve", zap.Error(err))
		}
	}()

	// Simple File Server for graders
	httpFSPort := viper.GetInt("fs.http.port")
	zap.L().Info("HTTPFS.Listen", zap.Int("port", httpFSPort))
	fileSrvRouter := chi.NewRouter()
	verifyHTTPFSToken := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get("token")
			if token != viper.GetString("fs.http.token") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	fileSrvRouter.Post("/*", verifyHTTPFSToken(autograderService.PushFile))
	fileSrvRouter.Get("/*", verifyHTTPFSToken(http.FileServer(http.Dir(".")).ServeHTTP))
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf(":%d", httpFSPort), fileSrvRouter); err != nil {
			zap.L().Fatal("HTTPFS.Serve", zap.Error(err))
		}
	}()

	go func() {
		http.ListenAndServe(":54321", http.DefaultServeMux)
	}()

	port := viper.GetInt("web.port")
	zap.L().Info("Web.Listen", zap.Int("port", port))
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), router); err != nil {
		zap.L().Fatal("Web.Serve", zap.Error(err))
	}
}
