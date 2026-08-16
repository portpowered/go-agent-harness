// Package localai contains testing helpers for the optional local LocalAI
// realtime server.
package localai

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// DefaultEndpoint is the LocalAI realtime WebSocket endpoint used by the
	// deploy/localai Compose fixture.
	DefaultEndpoint = "ws://localhost:8080/v1/realtime?model=gpt-realtime"

	realtimeEndpointEnv = "LOCALAI_REALTIME_URL"
	// LocalAI may rehydrate one pipeline backend when a fresh realtime
	// connection is opened. Keep discovery bounded in the low seconds without
	// making a ready fixture look absent during that warm-up.
	probeTimeout = 10 * time.Second
)

var failedEndpoints = struct {
	sync.RWMutex
	urls map[string]struct{}
}{
	urls: make(map[string]struct{}),
}

// Endpoint resolves and probes the optional LocalAI realtime endpoint.
//
// It returns the exact endpoint that was attempted, including an
// LOCALAI_REALTIME_URL override, and whether a realtime WebSocket produced a
// session.created event. Failed endpoint probes are cached for the lifetime of
// the process so an absent server does not delay every live test.
func Endpoint(t testing.TB) (wsURL string, ok bool) {
	t.Helper()

	wsURL = resolveEndpoint()
	if endpointFailed(wsURL) {
		return wsURL, false
	}
	if probe(wsURL) {
		return wsURL, true
	}

	rememberFailedEndpoint(wsURL)
	return wsURL, false
}

func resolveEndpoint() string {
	if endpoint := os.Getenv(realtimeEndpointEnv); endpoint != "" {
		return endpoint
	}
	return DefaultEndpoint
}

func endpointFailed(endpoint string) bool {
	failedEndpoints.RLock()
	defer failedEndpoints.RUnlock()
	_, failed := failedEndpoints.urls[endpoint]
	return failed
}

func rememberFailedEndpoint(endpoint string) {
	failedEndpoints.Lock()
	failedEndpoints.urls[endpoint] = struct{}{}
	failedEndpoints.Unlock()
}

func probe(endpoint string) bool {
	deadline := time.Now().Add(probeTimeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	dialer := websocket.Dialer{HandshakeTimeout: probeTimeout}
	conn, response, err := dialer.DialContext(ctx, endpoint, http.Header{})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil || conn == nil {
		return false
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(deadline); err != nil {
		return false
	}
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			return false
		}
		if messageType != websocket.TextMessage {
			continue
		}

		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &event); err == nil && event.Type == "session.created" {
			return true
		}
	}
}
