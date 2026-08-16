package localai

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

// WebSocketDialer and WebSocketConn expose the existing OpenAI-compatible
// injection seam without creating a second protocol abstraction.
type WebSocketDialer = openai.WebSocketDialer
type WebSocketConn = openai.WebSocketConn

const defaultHandshakeTimeout = 1500 * time.Millisecond

type defaultWebSocketDialer struct{}

func newDefaultWebSocketDialer() WebSocketDialer { return defaultWebSocketDialer{} }

func (defaultWebSocketDialer) Dial(endpoint string, _ map[string]string) (openai.WebSocketConn, error) {
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: defaultHandshakeTimeout}).Dial(endpoint, http.Header{})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

type noAuthDialer struct{ inner openai.WebSocketDialer }

func (d *noAuthDialer) Dial(endpoint string, _ map[string]string) (openai.WebSocketConn, error) {
	conn, err := d.inner.Dial(endpoint, nil)
	if err != nil {
		return nil, &dialError{Err: err}
	}
	return conn, nil
}
