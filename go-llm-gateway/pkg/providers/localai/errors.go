package localai

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// ErrConnection identifies a LocalAI endpoint connection failure.
var ErrConnection = errors.New("localai connection unavailable")

// ConnectionError reports a LocalAI endpoint that could not be reached.
type ConnectionError struct {
	Endpoint string
	Err      error
}

func NewConnectionError(endpoint string, err error) *ConnectionError {
	return &ConnectionError{Endpoint: endpoint, Err: err}
}

func (e *ConnectionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("localai: unable to connect to %s; start LocalAI and ensure its realtime server is running: %v", e.Endpoint, e.Err)
}

// Unwrap preserves both LocalAI identity, shared transport classification, and
// the original dial failure for errors.Is/errors.As callers.
func (e *ConnectionError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{ErrConnection, providers.ErrTransport, e.Err}
}

type dialError struct{ Err error }

func (e *dialError) Error() string { return e.Err.Error() }
func (e *dialError) Unwrap() error { return e.Err }
