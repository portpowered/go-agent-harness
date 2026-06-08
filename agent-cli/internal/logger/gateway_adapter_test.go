package logger

import (
	"testing"

	gatewaylogging "github.com/portpowered/go-llm-gateway/pkg/logging"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestZapGatewayAdapterWritesStructuredFields(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	logger := NewZapGatewayAdapter(zap.New(core))

	logger.Warn("gateway warning", gatewaylogging.Field{Key: "provider", Value: "openai"})

	entries := observed.FilterMessage("gateway warning").All()
	if len(entries) != 1 {
		t.Fatalf("expected one gateway warning log entry, got %d", len(entries))
	}
	if got := entries[0].ContextMap()["provider"]; got != "openai" {
		t.Fatalf("expected provider field to be openai, got %v", got)
	}
}
