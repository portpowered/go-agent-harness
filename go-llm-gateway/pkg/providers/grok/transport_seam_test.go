package grok_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// TestTransportSeam_DefaultDialerRoundTripsOverLocalWebSocket proves the
// provider-neutral seam is genuinely shared rather than merely type-compatible on
// paper: one transport.Dialer-typed value — the result of grok's own default
// constructor — flows into the grok provider via WithWebSocketDialer, dials a local
// WebSocket test server, carries one client-to-server message (the initial
// session.update) and one server-to-client message (session.created translated into
// SESSION.CREATED) through the resulting transport.Conn, and closes cleanly.
func TestTransportSeam_DefaultDialerRoundTripsOverLocalWebSocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	serverReceived := make(chan string, 1)
	serverDone := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()

		// Client-to-server: ConnectSession's initial session.update.
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("server read client message: %v", err)
			return
		}
		serverReceived <- string(data)

		// Server-to-client: session.created, which grok must translate into
		// SESSION.OPEN / SESSION.CREATED StreamMessages.
		if err := conn.WriteMessage(messageType, []byte(`{"type":"session.created","session_id":"seam-1","model":"grok-seam"}`)); err != nil {
			t.Errorf("server write session.created: %v", err)
			return
		}

		// Drain until the client closes the connection.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Assignment compatibility of grok's constructor result with the shared
	// transport.Dialer interface is exercised right here: this compiles only if
	// NewDefaultWebSocketDialer satisfies the provider-neutral contract.
	var d transport.Dialer = grok.NewDefaultWebSocketDialer()

	provider := grok.New(
		grok.WithAPIKey("seam-key"),
		grok.WithBaseURL(wsURL),
		grok.WithWebSocketDialer(d),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := provider.ConnectSession(ctx, models.SessionConfig{Model: "grok-seam"})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}

	// Observe the client-to-server message on the local server.
	select {
	case got := <-serverReceived:
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(got), &envelope); err != nil {
			t.Fatalf("unmarshal client message %q: %v", got, err)
		}
		if envelope.Type != "session.update" {
			t.Errorf("first client message type = %q, want session.update", envelope.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for client-to-server session.update")
	}

	// Observe the server-to-client message through the session's inbound buffer.
	recv := session.Receive()
	done := session.Done()
	sawOpen, sawCreated := false, false
	for !(sawOpen && sawCreated) {
		msg, ok := recv.ReadBlocking(done)
		if !ok {
			t.Fatal("session ended before SESSION.CREATED was delivered")
		}
		switch msg.Type {
		case messages.StreamTypeSessionOpen:
			sawOpen = true
		case messages.StreamTypeSessionCreated:
			sawCreated = true
			value, ok := msg.Value.(*messages.SessionCreatedValue)
			if !ok {
				t.Fatalf("SESSION.CREATED value type = %T, want *messages.SessionCreatedValue", msg.Value)
			}
			if value.SessionID != "seam-1" || value.Model != "grok-seam" {
				t.Errorf("SESSION.CREATED = {session_id:%q model:%q}, want {seam-1 grok-seam}", value.SessionID, value.Model)
			}
		}
	}

	// Close cleanly: no error from Close, and the server handler observes the
	// connection terminate (no leaked connection or handler goroutine).
	if err := session.Close(); err != nil {
		t.Fatalf("session close: %v", err)
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server-side connection teardown")
	}
}
