package logging

import (
	"context"
	"fmt"

	grpclogging "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"go.uber.org/zap"
)

// GrpcLogger adapts a zap logger to the go-grpc-middleware logging
// interceptor, which reports call events as alternating key/value fields.
func GrpcLogger(l *zap.Logger) grpclogging.Logger {
	l = l.WithOptions(zap.AddCallerSkip(1))
	return grpclogging.LoggerFunc(func(_ context.Context, level grpclogging.Level, msg string, fields ...any) {
		logger := l.With(zapFields(fields)...)
		switch level {
		case grpclogging.LevelDebug:
			logger.Debug(msg)
		case grpclogging.LevelInfo:
			logger.Info(msg)
		case grpclogging.LevelWarn:
			logger.Warn(msg)
		case grpclogging.LevelError:
			logger.Error(msg)
		default:
			logger.Panic(fmt.Sprintf("unknown log level %v", level))
		}
	})
}

// Extract returns the global logger annotated with the fields the gRPC
// logging interceptor stored in ctx (service, method, peer...), so handlers
// can log in the context of the call they serve.
func Extract(ctx context.Context) *zap.Logger {
	return zap.L().With(zapFields(grpclogging.ExtractFields(ctx))...)
}

func zapFields(fields []any) []zap.Field {
	out := make([]zap.Field, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			key = fmt.Sprint(fields[i])
		}
		switch v := fields[i+1].(type) {
		case string:
			out = append(out, zap.String(key, v))
		case int:
			out = append(out, zap.Int(key, v))
		case bool:
			out = append(out, zap.Bool(key, v))
		case error:
			out = append(out, zap.NamedError(key, v))
		default:
			out = append(out, zap.Any(key, v))
		}
	}
	return out
}
