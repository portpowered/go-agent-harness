package grok

import (
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// gorillaDialer adapts gorilla/websocket to the shared transport.Dialer contract.
type gorillaDialer struct{}

// NewDefaultWebSocketDialer returns the live Grok WebSocket dialer.
func NewDefaultWebSocketDialer() transport.Dialer {
	return &gorillaDialer{}
}

func (d *gorillaDialer) Dial(url string, headers map[string]string) (transport.Conn, error) {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}

	conn, _, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		return nil, err
	}
	return conn, nil
}
