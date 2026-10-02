// Package logger carries the request-scoped zap logger through a context.
package logger

import (
	"context"

	"go.uber.org/zap"
)

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

// WithLogger attaches a logger to the context for use by GetRequestLoggerFromContext.
func WithLogger(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, LOGGER_CONTEXT, l)
}

// Key for the request logger
const LOGGER_CONTEXT contextKey = "agentLogger"

// Define a custom type for keys
type contextKey string
