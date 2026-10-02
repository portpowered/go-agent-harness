package logger

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestLoggerContextAttachAndDefaultBehavior(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	ctx := WithLogger(context.Background(), zap.New(core))
	GetRequestLoggerFromContext(ctx).Info("context logger", zap.String("scope", "attached"))
	if entries := observed.FilterMessage("context logger").All(); len(entries) != 1 {
		t.Fatalf("attached context logger emitted %d records, want 1", len(entries))
	}

	GetRequestLoggerFromContext(context.Background()).Info("default noop")
	GetDefaultLogger().Info("default noop")
	if entries := observed.FilterMessage("default noop").All(); len(entries) != 0 {
		t.Fatalf("no-op default logger emitted %d records", len(entries))
	}
	if GetRequestLoggerFromContext(context.WithValue(context.Background(), LOGGER_CONTEXT, "not a logger")) == nil {
		t.Fatal("wrongly typed context value must fall back to the default logger")
	}
}
