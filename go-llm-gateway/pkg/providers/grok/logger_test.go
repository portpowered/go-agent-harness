package grok

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

type stubLogger struct{}

func (stubLogger) Debug(string, ...logging.Field) {}
func (stubLogger) Info(string, ...logging.Field)  {}
func (stubLogger) Warn(string, ...logging.Field)  {}
func (stubLogger) Error(string, ...logging.Field) {}
func (stubLogger) Fatal(string, ...logging.Field) {}
func (stubLogger) Panic(string, ...logging.Field) {}

func TestNew_DefaultLoggerIsSafe(t *testing.T) {
	provider := New()
	if provider.logger == nil {
		t.Fatal("expected default logger")
	}

	provider.logger.Info("info")
	provider.logger.Warn("warn")
	provider.logger.Error("error")
}

func TestWithLoggerUsesInjectedGatewayLogger(t *testing.T) {
	logger := stubLogger{}
	provider := New(WithLogger(logger))
	if provider.logger != logger {
		t.Fatal("expected injected logger to be preserved")
	}
}
