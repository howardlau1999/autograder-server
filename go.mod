module autograder-server

go 1.26.9

require (
	github.com/avast/retry-go/v4 v4.7.0
	github.com/cockroachdb/pebble v1.1.5
	github.com/cockroachdb/pebble/v2 v2.1.7
	github.com/go-chi/chi/v5 v5.3.2
	github.com/go-chi/httprate v0.16.1
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/go-cmp v0.7.0
	github.com/google/go-github/v86 v86.0.0
	github.com/google/uuid v1.6.0
	github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus v1.1.1
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.3.4
	github.com/kataras/hcaptcha v0.0.2
	github.com/mailgun/mailgun-go/v5 v5.19.3
	github.com/moby/moby/api v1.56.1
	github.com/moby/moby/client v0.6.2
	github.com/opencontainers/image-spec v1.1.1
	github.com/prometheus/client_golang v1.25.0
	github.com/rs/cors v1.11.1
	github.com/spf13/pflag v1.0.10
	github.com/spf13/viper v1.21.0
	go.uber.org/zap v1.28.0
	golang.org/x/crypto v0.58.0
	golang.org/x/oauth2 v0.37.0
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gotest.tools/v3 v3.5.2
)

require (
	github.com/DataDog/zstd v1.5.7 // indirect
	github.com/Microsoft/go-winio v0.6.3 // indirect
	github.com/RaduBerinde/axisds v0.1.0 // indirect
	github.com/RaduBerinde/btreemap v0.0.0-20250419174037-3d62b7205d54 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cockroachdb/crlib v0.0.0-20241112164430-1264a2edc35b // indirect
	github.com/cockroachdb/errors v1.14.0 // indirect
	github.com/cockroachdb/fifo v0.0.0-20240816210425-c5d0cb0b6fc0 // indirect
	github.com/cockroachdb/logtags v0.0.0-20241215232642-bb51bb14a506 // indirect
	github.com/cockroachdb/redact v1.1.8 // indirect
	github.com/cockroachdb/swiss v0.0.0-20260820225851-333444432258 // indirect
	github.com/cockroachdb/tokenbucket v0.0.0-20250429170803-42689b6311bb // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-connections v0.8.2 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/getsentry/sentry-go v0.50.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/klauspost/compress v1.20.1 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/minio/minlz v1.0.1-0.20250507153514-87eb42fe8882 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/oapi-codegen/runtime v1.6.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.72.1 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/sagikazarmark/locafero v0.12.0 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20261009195045-ca0d7ba23607 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261005182115-fad411399dd8 // indirect
)

exclude github.com/docker/distributions v2.8.0+incompatible
