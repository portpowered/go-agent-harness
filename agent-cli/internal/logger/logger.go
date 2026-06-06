package logger

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// log is deprecated - use context-based logging instead
var log *zap.Logger

// LoggerConfig holds configuration for logger initialization.
type LoggerConfig struct {
	VerbosityLevel int    // 0 = none, 1 = info, 2+ = debug
	ConfigDir      string // Config directory for file logging
	LogToStdout    bool   // If true, log to stdout/stderr instead of file
}

// fileLoggerCloser syncs the logger and closes the log file so the directory can be removed (e.g. in tests).
type fileLoggerCloser struct {
	logger *zap.Logger
	file   *os.File
}

func (c *fileLoggerCloser) Close() error {
	_ = c.logger.Sync()
	return c.file.Close()
}

// NewLoggerWithCloser creates a logger and an optional closer. When logging to a file, the caller must call closer.Close() when done so the file handle is released.
// When LogToStdout is true, the returned closer is nil (nothing to close).
func NewLoggerWithCloser(cfg LoggerConfig) (*zap.Logger, io.Closer, error) {
	// Determine log level based on verbosity
	var level zapcore.Level
	if cfg.VerbosityLevel == 0 {
		level = zapcore.ErrorLevel // Only errors when not verbose
	} else if cfg.VerbosityLevel == 1 {
		level = zapcore.InfoLevel
	} else {
		level = zapcore.DebugLevel
	}

	// Build encoder config
	encoderConfig := zap.NewDevelopmentEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeLevel = zapcore.LowercaseColorLevelEncoder
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	var writeSyncer zapcore.WriteSyncer
	var logFile *os.File
	if cfg.LogToStdout {
		// Log to stdout/stderr
		writeSyncer = zapcore.NewMultiWriteSyncer(
			zapcore.AddSync(os.Stdout),
			zapcore.AddSync(os.Stderr),
		)
	} else {
		// Log to file in config directory
		if cfg.ConfigDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, nil, err
			}
			cfg.ConfigDir = filepath.Join(home, ".agent-cli")
		}

		// Ensure config directory exists
		if err := os.MkdirAll(cfg.ConfigDir, 0755); err != nil {
			return nil, nil, err
		}

		// Create log file path
		logPath := filepath.Join(cfg.ConfigDir, "agent.log")
		var err error
		logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
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

// NewRequestLogger creates a new logger instance that includes the request ID from the context
// in every log entry. If no request ID is found, it returns the default logger.
func NewRequestLogger(ctx context.Context) *zap.Logger {
	requestID := GetRequestID(ctx)
	if requestID == "" {
		return GetDefaultLogger()
	}

	// Create a new logger with the request ID field
	return GetDefaultLogger().With(zap.String("request_id", requestID))
}

// GetRequestID retrieves the request ID from the context
func GetRequestID(ctx context.Context) string {
	if requestID, ok := ctx.Value(REQUEST_ID).(string); ok {
		return requestID
	}
	return ""
}

// WithRequestID creates a logger with a specific request ID
func WithRequestID(requestID string) *zap.Logger {
	if requestID == "" {
		return GetDefaultLogger()
	}

	return GetDefaultLogger().With(zap.String("request_id", requestID))
}

// GetDefaultLogger returns the default logger instance
// If the logger hasn't been initialized, it returns a no-op logger
func GetDefaultLogger() *zap.Logger {
	if log == nil {
		return zap.NewNop()
	}
	return log
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
