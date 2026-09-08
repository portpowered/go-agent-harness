package openai

import (
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func isProviderCloseTransportError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		websocket.IsCloseError(err, websocket.CloseNormalClosure)
}

func sessionDone(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

type gorillaDialer struct{}

var _ transport.Dialer = (*gorillaDialer)(nil)

// NewDefaultWebSocketDialer returns the live OpenAI realtime WebSocket dialer.
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
