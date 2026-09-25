package grok

import (
	"errors"
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

	conn, response, err := websocket.DefaultDialer.Dial(url, h)
	if response != nil {
		// Gorilla replaces the handshake body with an in-memory reader, so
		// closing it releases nothing and cannot fail after a successful dial.
		err = errors.Join(err, response.Body.Close())
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}
