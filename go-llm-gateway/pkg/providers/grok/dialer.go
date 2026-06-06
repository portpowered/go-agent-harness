package grok

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// gorillaDialer adapts gorilla/websocket to the WebSocketDialer interface.
type gorillaDialer struct{}

// NewDefaultWebSocketDialer returns the live Grok WebSocket dialer.
func NewDefaultWebSocketDialer() WebSocketDialer {
	return &gorillaDialer{}
}

func (d *gorillaDialer) Dial(url string, headers map[string]string) (WebSocketConn, error) {
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
