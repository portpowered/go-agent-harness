package wire

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
)

func TestNewServiceBuildsPublicContract(t *testing.T) {
	service := NewService(metricsreplay.Dependencies{})
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	if _, err := service.Collect(context.Background(), "fixture", ""); err == nil {
		t.Fatal("service built by Wire accepted missing dependencies")
	}
}
