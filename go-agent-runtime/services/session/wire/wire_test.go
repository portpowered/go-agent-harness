package wire

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestLiveServiceRejectsMissingInferencerFactory(t *testing.T) {
	_, err := NewLiveService(LiveDependencies{}).OpenLive(context.Background(), session.LiveRequest{})
	if err == nil || !strings.Contains(err.Error(), "live inferencer factory is required") {
		t.Fatalf("OpenLive error = %v, want missing-factory failure", err)
	}
}
