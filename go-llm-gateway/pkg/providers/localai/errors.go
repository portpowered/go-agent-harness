package localai

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

var ErrConnection = errors.New("localai connection unavailable")

type ConnectionError struct {
	Endpoint string
	Err      error
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("localai: unable to connect to %s; start LocalAI and ensure its realtime server is running: %v", e.Endpoint, e.Err)
}
func (e *ConnectionError) Is(target error) bool {
	return target == ErrConnection || target == providers.ErrTransport
}
func (e *ConnectionError) Unwrap() error { return e.Err }

type dialError struct{ error }

type noAuthDialer struct{ inner openai.WebSocketDialer }

func (d *noAuthDialer) Dial(endpoint string, _ map[string]string) (openai.WebSocketConn, error) {
	conn, err := d.inner.Dial(endpoint, nil)
	if err != nil {
		return nil, &dialError{err}
	}
	return conn, nil
}
