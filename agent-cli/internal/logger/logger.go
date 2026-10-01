package logger

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	// logDirPerm is the permission for the log directory under the config dir.
	logDirPerm os.FileMode = 0o755
	// logFilePerm is the permission for the agent.log file.
	logFilePerm os.FileMode = 0o644
)

// newConsoleWriteSyncer returns the default console sink: stdout and stderr.
func newConsoleWriteSyncer() zapcore.WriteSyncer {
	return zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(os.Stdout),
		zapcore.AddSync(os.Stderr),
	)
}

// LoggerConfig holds configuration for logger initialization.
type LoggerConfig struct {
	VerbosityLevel int    // 0 = none, 1 = info, 2+ = debug
	ConfigDir      string // Config directory for file logging
	LogToStdout    bool   // If true, log to stdout/stderr instead of file
	// ConsoleSink overrides the stdout/stderr destination used when
	// LogToStdout is true. Nil selects the process stdout and stderr.
	ConsoleSink zapcore.WriteSyncer
}

// fileLoggerCloser syncs the logger and closes the log file so the directory can be removed (e.g. in tests).
type fileLoggerCloser struct {
	logger *zap.Logger
	file   *os.File
}

func (c *fileLoggerCloser) Close() error {
	return errors.Join(c.logger.Sync(), c.file.Close())
}

// NewLoggerWithCloser creates a logger and an optional closer. When logging to a file, the caller must call closer.Close() when done so the file handle is released.
// When LogToStdout is true, the returned closer is nil (nothing to close).
func NewLoggerWithCloser(cfg LoggerConfig) (*zap.Logger, io.Closer, error) {
	// Determine log level based on verbosity
	var level zapcore.Level
	switch cfg.VerbosityLevel {
	case 0:
		level = zapcore.ErrorLevel // Only errors when not verbose
	case 1:
		level = zapcore.InfoLevel
	default:
		level = zapcore.DebugLevel
	}

	// Build encoder config
	encoderConfig := zap.NewDevelopmentEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeLevel = zapcore.LowercaseColorLevelEncoder
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	var writeSyncer zapcore.WriteSyncer
	var logFile *os.File
	switch {
	case cfg.LogToStdout && cfg.ConsoleSink != nil:
		writeSyncer = cfg.ConsoleSink
	case cfg.LogToStdout:
		writeSyncer = newConsoleWriteSyncer()
	default:
		var err error
		logFile, err = openLogFile(cfg.ConfigDir)
		if err != nil {
			return nil, nil, err
		}
		writeSyncer = zapcore.AddSync(logFile)
	}

	// Build core
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		writeSyncer,
		zap.NewAtomicLevelAt(level),
	)

	// Build logger
	l := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	var closer io.Closer
	if logFile != nil {
		closer = &fileLoggerCloser{logger: l, file: logFile}
	}
	return l, closer, nil
}

// openLogFile opens agent.log for appending in configDir, defaulting to
// ~/.agent-cli and creating the directory when needed.
func openLogFile(configDir string) (*os.File, error) {
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		configDir = filepath.Join(home, ".agent-cli")
	}
	if err := os.MkdirAll(configDir, logDirPerm); err != nil {
		return nil, err
	}
	logPath := filepath.Join(configDir, "agent.log")
	return os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFilePerm)
}

// NewLogger creates a logger based on the provided configuration.
// By default, logs are written to a file in the config directory.
// If LogToStdout is true, logs are written to stdout/stderr instead.
// For file logging, prefer NewLoggerWithCloser so the file can be closed when the CLI command exits.
func NewLogger(cfg LoggerConfig) (*zap.Logger, error) {
	l, _, err := NewLoggerWithCloser(cfg)
	return l, err
}

// NewDefaultLogger creates a logger with default settings (no verbosity, file logging).
func NewDefaultLogger() *zap.Logger {
	cfg := LoggerConfig{
		VerbosityLevel: 0,
		LogToStdout:    false,
	}
	l, err := NewLogger(cfg)
	if err != nil {
		// Fallback to no-op logger if file logging fails
		return zap.NewNop()
	}
	return l
}

// NewRequestLogger returns the context's logger (see GetRequestLoggerFromContext)
// annotated with the request ID from the context in every log entry. If no
// request ID is found, it returns the context's logger unchanged.
func NewRequestLogger(ctx context.Context) *zap.Logger {
	base := GetRequestLoggerFromContext(ctx)
	requestID := GetRequestID(ctx)
	if requestID == "" {
		return base
	}
	return base.With(zap.String("request_id", requestID))
}

// GetRequestID retrieves the request ID from the context
func GetRequestID(ctx context.Context) string {
	if requestID, ok := ctx.Value(REQUEST_ID).(string); ok {
		return requestID
	}
	return ""
}

// GetDefaultLogger returns the default logger used when no logger is attached
// to a context: a no-op logger. Attach a real logger with WithLogger.
func GetDefaultLogger() *zap.Logger {
	return zap.NewNop()
}

// GetRequestLoggerFromContext retrieves the logger from the request context
// If no logger is found in the context, it returns the default logger
func GetRequestLoggerFromContext(ctx context.Context) *zap.Logger {
	if logger, ok := ctx.Value(LOGGER_CONTEXT).(*zap.Logger); ok {
		return logger
	}
	return GetDefaultLogger()
}

// NewVerboseLogger creates a logger based on verbosity level and config directory.
// verbosityLevel: 0 = none, 1 = info, 2+ = debug
// configDir: directory for file logging (empty uses default ~/.agent-cli)
// logToStdout: if true, log to stdout/stderr instead of file
func NewVerboseLogger(verbosityLevel int, configDir string, logToStdout bool) *zap.Logger {
	l, _ := NewVerboseLoggerWithCloser(verbosityLevel, configDir, logToStdout)
	return l
}

// NewVerboseLoggerWithCloser returns a logger and an optional closer. When logging to a file, the caller must call closer.Close() when done (e.g. when the CLI command exits).
// When LogToStdout is true or on error, the returned closer is nil.
func NewVerboseLoggerWithCloser(verbosityLevel int, configDir string, logToStdout bool) (*zap.Logger, io.Closer) {
	cfg := LoggerConfig{
		VerbosityLevel: verbosityLevel,
		ConfigDir:      configDir,
		LogToStdout:    logToStdout,
	}
	l, closer, err := NewLoggerWithCloser(cfg)
	if err != nil {
		return zap.NewNop(), nil
	}
	return l, closer
}

// WithLogger attaches a logger to the context for use by GetRequestLoggerFromContext.
func WithLogger(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, LOGGER_CONTEXT, l)
}

// Key for the request logger
const LOGGER_CONTEXT contextKey = "agentLogger"
const REQUEST_ID contextKey = "agentRequestID"

// Define a custom type for keys
type contextKey string
