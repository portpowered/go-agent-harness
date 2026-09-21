package livehost

import (
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// RecordingDialer is the narrow raw-wire compatibility port used by the
// provider builder. Capture admission and semantic evidence remain owned by
// the runtime recording service.
type RecordingDialer interface {
	transport.Dialer
	FlushToFile(string) error
}

// NewRecordingDialer adapts the existing provider wire recorder for the
// legacy raw --record transport seam. It owns no session or policy state.
func NewRecordingDialer(inner transport.Dialer, provider, model string) RecordingDialer {
	return testing.NewRecordingWebSocketDialer(inner, provider, model)
}
