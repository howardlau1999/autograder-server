package logging

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

const (
	// DPanic, Panic and Fatal level can not be set by user
	DebugLevelStr   string = "debug"
	InfoLevelStr    string = "info"
	WarningLevelStr string = "warning"
	ErrorLevelStr   string = "error"
)

var (
	globalLogger *zap.Logger
	devMode      bool = false
)

func Sync() error {
	return globalLogger.Sync()
}

func Init(logLevel string, logFile string, dev bool) *zap.Logger {
	devMode = dev
	var level zapcore.Level
	switch logLevel {
	case DebugLevelStr:
		level = zap.DebugLevel
	case InfoLevelStr:
		level = zap.InfoLevel
	case WarningLevelStr:
		level = zap.WarnLevel
	case ErrorLevelStr:
		level = zap.ErrorLevel
	default:
		level = zap.InfoLevel
	}

	ws := zapcore.AddSync(
		&lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    1, //MB
			MaxBackups: 30,
			MaxAge:     90, //days
			Compress:   false,
		},
	)

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout), ws),
		zap.NewAtomicLevelAt(level),
	)
	var _globalLogger *zap.Logger
	if dev {
		_globalLogger = zap.New(core, zap.AddCaller(), zap.Development())
	} else {
		_globalLogger = zap.New(core, zap.AddCaller())
	}
	zap.ReplaceGlobals(_globalLogger)
	globalLogger = _globalLogger
	return _globalLogger
}
