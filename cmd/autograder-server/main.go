package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

	"github.com/cockroachdb/pebble/v2"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	grpclogging "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"github.com/kataras/hcaptcha"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapgrpc"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
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
	# Number of trusted reverse-proxy hops in front of this server (nginx,
	# CDN, load balancer). 0 means the socket address is used for rate
	# limiting and X-Forwarded-For is ignored; set it to the real hop count
	# when deployed behind a proxy, or clients can spoof their IP.
	trusted-proxy-count=0

[cors]
	# Cross-origin requests are only accepted from these origins in production.
	# Leave empty when the web client is served from the same origin.
	allowed-origins=[]

[smtp]
    addr=""
    user=""
    pass=""
    from=""
    # Connection security: "implicit" (TLS from the start, e.g. port 465),
    # "starttls" (plain connect upgraded via STARTTLS, e.g. port 587), or
    # "none" (plain; credentials are only sent to a localhost server).
    tls-mode="implicit"

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

func dbInit(db *pebble.DB, email string) error {
	if !isDatabaseUninitialized(db) {
		return fmt.Errorf(
			"database is already initialized; delete it manually to re-initialize",
		)
	}
	rootPassword := RandStringRunes(16)
	userRepo, err := repository.NewKVUserRepository(db)
	if err != nil {
		return err
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(rootPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	rootUser := &model_pb.User{Username: "root", Password: passwordHash, Email: email, Nickname: "root", IsAdmin: true}
	if _, err = userRepo.CreateUser(context.Background(), rootUser); err != nil {
		return err
	}
	if err = db.Set(initializeMarker, nil, pebble.Sync); err != nil {
		return err
	}

	log.Printf("Database initialized. Root user information\nUsername: root\nPassword: %s", rootPassword)
	return nil
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
	viper.SetDefault("server.trusted-proxy-count", 0)
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.file", "server.log")
	viper.SetDefault("log.development", "false")

	err := viper.ReadInConfig()
	if err != nil {
		// A missing config file is fine (defaults and environment variables
		// still apply); anything else means the file exists but is unusable.
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			log.Fatalf("Failed to read config file: %v", err)
		}
	}
}

// insecureSecretDefaults holds the placeholder values shipped in the config
// template. They must never be used in a running server.
var insecureSecretDefaults = map[string]string{
	"token.secret.session":  "user-session-token-secret",
	"token.secret.upload":   "upload-token-secret",
	"token.secret.download": "download-token-secret",
}

// validateSecrets aborts startup when any security-critical secret is empty or
// still set to its shipped placeholder value, so the server fails closed rather
// than signing tokens with a well-known key.
func validateSecrets() {
	for key, placeholder := range insecureSecretDefaults {
		v := viper.GetString(key)
		if v == "" || v == placeholder {
			zap.L().Fatal(
				"Config.Secret.Insecure", zap.String("key", key),
				zap.String("hint", "set it to a strong, non-default value"),
			)
		}
	}
	for _, key := range []string{"hub.token", "fs.http.token"} {
		if viper.GetString(key) == "" {
			zap.L().Fatal(
				"Config.Secret.Missing", zap.String("key", key),
				zap.String("hint", "set it to a non-empty value"),
			)
		}
	}
}

func isDatabaseUninitialized(db *pebble.DB) bool {
	_, closer, err := db.Get(initializeMarker)
	if err == nil {
		defer closer.Close()
	}
	return err == pebble.ErrNotFound
}

// contentSecurityPolicy is served with the embedded web client. The web XSS
// root cause is fixed by sanitising rendered markdown, but a CSP is defence in
// depth: it blocks injected inline scripts (there are none of our own now that
// the server-provided values travel as JSON data), plugins, <base>/frame
// injection and clickjacking. The allow-lists cover what the client actually
// loads: same-origin bundles/KaTeX/Prism/xterm, the hCaptcha widget, the Google
// Fonts mirror, WebAssembly (the VCD waveform parser) and blob workers (zip.js).
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'wasm-unsafe-eval' blob: https://hcaptcha.com https://*.hcaptcha.com; " +
	"style-src 'self' 'unsafe-inline' https://fonts.loli.net https://hcaptcha.com https://*.hcaptcha.com; " +
	"font-src 'self' data: https://fonts.gstatic.com https://fonts.loli.net https://gstatic.loli.net; " +
	"img-src 'self' https: data: blob:; " +
	"connect-src 'self' https://hcaptcha.com https://*.hcaptcha.com; " +
	"worker-src 'self' blob:; " +
	"frame-src https://hcaptcha.com https://*.hcaptcha.com; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'"

// setWebSecurityHeaders attaches the CSP and a few companion headers to a web
// client response.
func setWebSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

func initEmbeddedStaticWebResources(router chi.Router) error {
	distFS, err := fs.Sub(web.WebResources, "dist")
	if err != nil {
		return err
	}
	providedTokens := &ServerProvidedTokens{
		ServerProvided:  "true",
		HcaptchaSiteKey: viper.GetString("hcaptcha.site-key"),
		GithubClientId:  viper.GetString("github.client-id"),
	}
	return initStaticWebResources(router, distFS, providedTokens)
}

func initStaticWebResources(router chi.Router, distFS fs.FS, providedTokens *ServerProvidedTokens) error {
	indexTemplate, err := fs.ReadFile(distFS, "index.html")
	var indexHTML []byte
	switch {
	case err == nil:
		tmpl, err := template.New("index.html").Parse(string(indexTemplate))
		if err != nil {
			return fmt.Errorf("parse web index: %w", err)
		}
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, providedTokens); err != nil {
			return fmt.Errorf("render web index: %w", err)
		}
		indexHTML = rendered.Bytes()
	case errors.Is(err, fs.ErrNotExist):
		// Server-only builds deliberately embed no index.html.
	default:
		return fmt.Errorf("read web index: %w", err)
	}
	fsrv := http.FileServer(http.FS(distFS))

	router.Get(
		"/*", func(w http.ResponseWriter, r *http.Request) {
			setWebSecurityHeaders(w)
			p := strings.TrimPrefix(r.URL.Path, "/")
			_, err := fs.Stat(distFS, p)
			if indexHTML != nil && (p == "" || p == "index.html" || errors.Is(err, fs.ErrNotExist)) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(indexHTML)
				return
			}
			fsrv.ServeHTTP(w, r)
		},
	)
	return nil
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
		db, err := repository.OpenDB(viper.GetString("db.local.path"))
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer db.Close()
		if initEmail == "" {
			log.Printf("Please provide email.")
			return true
		}
		if err := dbInit(db, initEmail); err != nil {
			log.Fatalf("Failed to initialize database: %v", err)
		}
		return true
	}
	return false
}

func main() {
	serverReadConfig()
	if processCommandLineOptions() {
		return
	}
	zapLogger := logging.Init(
		viper.GetString("log.level"), viper.GetString("log.file"), viper.GetBool("log.development"),
	)
	defer zapLogger.Sync()
	validateSecrets()
	db, err := repository.OpenDB(viper.GetString("db.local.path"))
	if err != nil {
		zap.L().Fatal("DB.Open", zap.Error(err))
	}
	defer db.Close()
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
		smtpTLSMode, err := mailer.ParseSMTPTLSMode(viper.GetString("smtp.tls-mode"))
		if err != nil {
			zap.L().Fatal("SMTP.TLSMode.Invalid", zap.Error(err))
		}
		m = mailer.NewSMTPMailer(
			viper.GetString("smtp.addr"), viper.GetString("smtp.user"), viper.GetString("smtp.pass"),
			smtpTLSMode,
		)
	}
	// The template documents hcaptcha.secret; hcaptcha.secret-key is the name
	// the code read before and is kept so existing deployments do not break.
	hcaptchaSecret := viper.GetString("hcaptcha.secret")
	if hcaptchaSecret == "" {
		hcaptchaSecret = viper.GetString("hcaptcha.secret-key")
	}
	hcaptchaClient := hcaptcha.New(hcaptchaSecret)
	hcaptchaClient.HTTPClient.Timeout = 30 * time.Second
	githubOauth2Config := &oauth2.Config{
		ClientID:     viper.GetString("github.client-id"),
		ClientSecret: viper.GetString("github.client-secret"),
		Scopes:       []string{"user:email", "read:user"},
		Endpoint:     github.Endpoint,
	}

	origins := newOriginPolicy(viper.GetStringSlice("cors.allowed-origins"), viper.GetBool("server.development"))

	corsHandler := cors.New(
		cors.Options{
			AllowOriginFunc:  origins.allowCrossOrigin,
			AllowedHeaders:   []string{"Upload-token", "Download-token"},
			ExposedHeaders:   nil,  // make sure that this is *nil*, otherwise the WebResponse overwrite will not work.
			AllowCredentials: true, // always allow credentials, otherwise :authorization headers won't work
			MaxAge:           int(10 * time.Minute / time.Second),
		},
	)
	grpclog.SetLoggerV2(zapgrpc.NewLogger(zapLogger.Named("grpc")))
	srvMetrics := grpcprom.NewServerMetrics(grpcprom.WithServerHandlingTimeHistogram())
	prometheus.MustRegister(srvMetrics)
	grpcLogger := logging.GrpcLogger(zapLogger)
	// One line per finished call; the per-call logger handlers obtain through
	// logging.Extract carries the same fields. Recovery is innermost so a
	// panic is reported as an Internal error by the outer interceptors.
	logOpts := []grpclogging.Option{grpclogging.WithLogOnEvents(grpclogging.FinishCall)}
	autograderServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			srvMetrics.UnaryServerInterceptor(),
			grpclogging.UnaryServerInterceptor(grpcLogger, logOpts...),
			autograder_grpc.UnaryAuth(),
			recovery.UnaryServerInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			srvMetrics.StreamServerInterceptor(),
			grpclogging.StreamServerInterceptor(grpcLogger, logOpts...),
			autograder_grpc.StreamAuth(),
			recovery.StreamServerInterceptor(),
		),
	)
	kaep := keepalive.EnforcementPolicy{PermitWithoutStream: true, MinTime: 1 * time.Second}
	ksap := keepalive.ServerParameters{Time: 5 * time.Second, Timeout: 1 * time.Hour}
	srr := repository.NewKVSubmissionReportRepository(db)
	graderHubService, err := grader_grpc.NewGraderHubService(
		db, srr, viper.GetString("hub.token"), heartbeatTimeout,
	)
	if err != nil {
		zap.L().Fatal("GraderHub.Init", zap.Error(err))
	}
	defer graderHubService.Close()
	graderHubServer := grpc.NewServer(
		grpc.KeepaliveEnforcementPolicy(kaep),
		grpc.KeepaliveParams(ksap),
		grpc.ChainUnaryInterceptor(
			srvMetrics.UnaryServerInterceptor(),
			grpclogging.UnaryServerInterceptor(grpcLogger, logOpts...),
			graderHubService.UnaryAuthInterceptor(),
			recovery.UnaryServerInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			srvMetrics.StreamServerInterceptor(),
			grpclogging.StreamServerInterceptor(grpcLogger, logOpts...),
			graderHubService.StreamAuthInterceptor(),
			recovery.StreamServerInterceptor(),
		),
	)
	autograderService, err := autograder_grpc.NewAutograderServiceServer(
		db, localStorage, m, hcaptchaClient, githubOauth2Config, srr, graderHubService,
	)
	if err != nil {
		zap.L().Fatal("AutograderService.Init", zap.Error(err))
	}
	defer autograderService.Close()
	autograder_pb.RegisterAutograderServiceServer(autograderServer, autograderService)
	grader_pb.RegisterGraderHubServiceServer(graderHubServer, graderHubService)
	// Pre-populates the per-method counters so they are exported as 0.
	srvMetrics.InitializeMetrics(autograderServer)
	srvMetrics.InitializeMetrics(graderHubServer)
	grpcWeb := middleware.NewGrpcWeb(autograderServer, origins.allowCrossOrigin)
	// Resolve the client IP before rate limiting. By default the socket
	// address is used and X-Forwarded-For is ignored, so a client cannot
	// spoof its way around the limiter; behind a reverse proxy set
	// server.trusted-proxy-count to the real number of hops.
	trustedProxyCount := viper.GetInt("server.trusted-proxy-count")
	var clientIPMiddleware func(http.Handler) http.Handler
	if trustedProxyCount > 0 {
		clientIPMiddleware = chiMiddleware.ClientIPFromXFFTrustedProxies(trustedProxyCount)
	} else {
		clientIPMiddleware = chiMiddleware.ClientIPFromRemoteAddr
	}
	clientIPKey := func(r *http.Request) (string, error) {
		// CanonicalizeIP buckets IPv6 clients by /64 so they cannot rotate
		// within their own prefix to win a fresh bucket.
		return httprate.CanonicalizeIP(chiMiddleware.GetClientIP(r.Context())), nil
	}
	const rateLimitRequests = 100
	const rateLimitWindow = 3 * time.Second
	router := chi.NewRouter()
	router.Use(
		clientIPMiddleware,
		apiOnly(httprate.LimitBy(rateLimitRequests, rateLimitWindow, clientIPKey)),
		chiMiddleware.Logger,
		chiMiddleware.Recoverer,
		grpcWeb.Handler,
	)
	router.Options(
		apiPathPrefix+"FileUpload",
		corsHandler.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP,
	)
	router.Post(
		apiPathPrefix+"FileUpload",
		corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileUpload)).ServeHTTP,
	)
	router.Get(
		apiPathPrefix+"FileDownload/{filename}",
		corsHandler.Handler(http.HandlerFunc(autograderService.HandleFileDownload)).ServeHTTP,
	)
	if err := initEmbeddedStaticWebResources(router); err != nil {
		zap.L().Fatal("Web.Init", zap.Error(err))
	}

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
	metricsSrv := &http.Server{Addr: fmt.Sprintf(":%d", metricsPort), Handler: metricsRouter}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zap.L().Error("Metrics.Serve", zap.Error(err))
		}
	}()

	// Simple File Server for graders
	httpFSPort := viper.GetInt("fs.http.port")
	zap.L().Info("HTTPFS.Listen", zap.Int("port", httpFSPort))
	fileSrvRouter := chi.NewRouter()
	verifyHTTPFSToken := func(next http.HandlerFunc) http.HandlerFunc {
		expected := []byte(viper.GetString("fs.http.token"))
		return func(w http.ResponseWriter, r *http.Request) {
			token := []byte(r.Header.Get("token"))
			if subtle.ConstantTimeCompare(token, expected) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	fileSrvRouter.Post("/*", verifyHTTPFSToken(autograderService.PushFile))
	fileSrvRouter.Get("/*", verifyHTTPFSToken(http.FileServer(http.Dir(localStorageDir)).ServeHTTP))
	fileSrv := &http.Server{Addr: fmt.Sprintf(":%d", httpFSPort), Handler: fileSrvRouter}
	go func() {
		if err := fileSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zap.L().Fatal("HTTPFS.Serve", zap.Error(err))
		}
	}()

	// The pprof debug endpoints are only exposed in development, bound to
	// localhost, so they are never reachable in production.
	var pprofSrv *http.Server
	if viper.GetBool("server.development") {
		pprofSrv = &http.Server{Addr: "127.0.0.1:54321", Handler: http.DefaultServeMux}
		go func() {
			if err := pprofSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				zap.L().Error("Pprof.Serve", zap.Error(err))
			}
		}()
	}

	port := viper.GetInt("web.port")
	zap.L().Info("Web.Listen", zap.Int("port", port))
	webSrv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: router}
	go func() {
		if err := webSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zap.L().Fatal("Web.Serve", zap.Error(err))
		}
	}()

	// Block until SIGINT/SIGTERM, then shut down in dependency order: stop the
	// background goroutines first, drain HTTP and gRPC, close the database last.
	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-runCtx.Done()
	zap.L().Info("Server.Shutdown")
	autograderService.Close()
	graderHubService.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range []*http.Server{webSrv, metricsSrv, fileSrv, pprofSrv} {
		if srv == nil {
			continue
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			zap.L().Error("Server.Shutdown.Error", zap.String("addr", srv.Addr), zap.Error(err))
		}
	}
	grpcDone := make(chan struct{})
	go func() {
		autograderServer.GracefulStop()
		graderHubServer.GracefulStop()
		close(grpcDone)
	}()
	select {
	case <-grpcDone:
	case <-shutdownCtx.Done():
		autograderServer.Stop()
		graderHubServer.Stop()
	}
}
