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

// Prober resolves and probes the optional LocalAI realtime endpoint. It
// remembers endpoints whose probe failed so an absent server delays only the
// first live test that shares the prober.
type Prober struct {
	mu     sync.RWMutex
	failed map[string]struct{}
}

// NewProber returns a prober with no remembered failures.
func NewProber() *Prober {
	return &Prober{failed: make(map[string]struct{})}
}

// Endpoint resolves and probes the optional LocalAI realtime endpoint.
//
// It returns the exact endpoint that was attempted, including an
// LOCALAI_REALTIME_URL override, and whether a realtime WebSocket produced a
// session.created event. The probe is bounded by tb's context and
// probeTimeout; a failed endpoint is not probed again by this prober.
func (p *Prober) Endpoint(tb testing.TB) (wsURL string, ok bool) {
	tb.Helper()

	wsURL = resolveEndpoint()
	if p.endpointFailed(wsURL) {
		return wsURL, false
	}
	if probe(tb.Context(), wsURL) {
		return wsURL, true
	}

	p.rememberFailedEndpoint(wsURL)
	return wsURL, false
}

func resolveEndpoint() string {
	if endpoint := os.Getenv(realtimeEndpointEnv); endpoint != "" {
		return endpoint
	}
	return DefaultEndpoint
}

func (p *Prober) endpointFailed(endpoint string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, failed := p.failed[endpoint]
	return failed
}

func (p *Prober) rememberFailedEndpoint(endpoint string) {
	p.mu.Lock()
	p.failed[endpoint] = struct{}{}
	p.mu.Unlock()
}

func probe(parent context.Context, endpoint string) bool {
	deadline := time.Now().Add(probeTimeout)
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()

	dialer := websocket.Dialer{HandshakeTimeout: probeTimeout}
	conn, response, err := dialer.DialContext(ctx, endpoint, http.Header{})
	if response != nil && response.Body != nil {
		// Gorilla buffers the handshake body in memory; releasing it cannot fail after a successful dial.
		err = joinOnFailure(err, response.Body.Close())
	}
	if err != nil || conn == nil {
		return false
	}
	defer discardClose(conn)

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
