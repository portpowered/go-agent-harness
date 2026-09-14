package livehost

import (
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// ReplayDialer is the narrow raw-wire compatibility port retained for
// injected provider tests. Replay planning and lifecycle ownership live in the
// runtime replay service.
type ReplayDialer interface {
	transport.Dialer
	Done() <-chan struct{}
	Err() error
	Model() string
}

// NewReplayDialer adapts the gateway's strict cursor for a provider builder.
// Timing is the only compatibility choice exposed by this stateless adapter.
func NewReplayDialer(path string, recordedTiming bool) (ReplayDialer, error) {
	if recordedTiming {
		return testing.NewReplayWebSocketDialer(path, testing.WithRecordedSessionTiming())
	}
	return testing.NewReplayWebSocketDialer(path)
}
