package fakelive_test

import (
	"testing"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	testKey    = "sk-test"
	bearer     = "Bearer " + testKey
	authHeader = "Authorization"
)

func ptr[T any](value T) *T { return &value }

// liveConn is a client connection with protocol helpers.
type liveConn struct {
	t    *testing.T
	conn transport.Conn
}

func dial(t *testing.T, server *fakelive.Server) *liveConn {
	t.Helper()
	conn, err := server.Dialer().Dial(live.DefaultEndpoint, map[string]string{authHeader: bearer})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return &liveConn{t: t, conn: conn}
}

func (c *liveConn) send(event live.Event) {
	c.t.Helper()
	frame, err := live.EncodeEvent(event)
	if err != nil {
		c.t.Fatal(err)
	}
	c.sendRaw(string(frame))
}

func (c *liveConn) sendRaw(frame string) {
	c.t.Helper()
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *liveConn) next() live.Event {
	c.t.Helper()
	_, payload, err := c.conn.ReadMessage()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	event, err := live.DecodeServerEvent(payload)
	if err != nil {
		c.t.Fatalf("decode %s: %v", payload, err)
	}
	return event
}

// start sends a built session.start for cfg and returns session.started.
func (c *liveConn) start(cfg models.SessionConfig) live.SessionStarted {
	c.t.Helper()
	start, err := live.BuildSessionStart("evt_start", cfg)
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(start)
	return expect[live.SessionStarted](c)
}

func expect[T live.Event](c *liveConn) T {
	c.t.Helper()
	event := c.next()
	typed, ok := event.(T)
	if !ok {
		var want T
		c.t.Fatalf("got %#v, want %T", event, want)
	}
	return typed
}

// expectError reads one error event and checks its code, param and client
// event id.
func (c *liveConn) expectError(code, param, clientEventID string) {
	c.t.Helper()
	event := expect[live.ErrorEvent](c)
	got := event.Error
	gotParam := ""
	if got.Param != nil {
		gotParam = *got.Param
	}
	if got.Code != code || gotParam != param || got.ClientEventID != clientEventID || got.Type != live.ErrorTypeInvalidRequest {
		c.t.Fatalf("error = %+v (param %q), want code %q param %q client event %q", got, gotParam, code, param, clientEventID)
	}
}

func (c *liveConn) expectClosedSocket() {
	c.t.Helper()
	if _, payload, err := c.conn.ReadMessage(); err == nil {
		c.t.Fatalf("read %s, want the socket closed", payload)
	}
}

func liveConfig() models.SessionConfig {
	return models.SessionConfig{Model: live.Model1, Instructions: "Be concise."}
}
