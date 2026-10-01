package realtime

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestDialerConnectsAndSendsHeaders(t *testing.T) {
	gotAuth := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.created"}`)); err != nil {
			t.Errorf("server write: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Errorf("server close: %v", err)
		}
	}))
	defer server.Close()

	conn, err := NewDialer().Dial("ws"+strings.TrimPrefix(server.URL, "http"), map[string]string{"Authorization": "Bearer k"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	if auth := <-gotAuth; auth != "Bearer k" {
		t.Fatalf("authorization header = %q", auth)
	}
	if _, data, err := conn.ReadMessage(); err != nil || string(data) != `{"type":"session.created"}` {
		t.Fatalf("read = %s, %v", data, err)
	}
}

func TestDialerReportsHandshakeRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer server.Close()
	if _, err := NewDialer().Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil); !errors.Is(err, websocket.ErrBadHandshake) {
		t.Fatalf("dial error = %v, want bad handshake", err)
	}
}

func TestJoinOnFailure(t *testing.T) {
	opErr, cleanupErr := errors.New("op"), errors.New("cleanup")
	if JoinOnFailure(nil, cleanupErr) != nil {
		t.Fatal("cleanup failure turned success into failure")
	}
	if got := JoinOnFailure(opErr, nil); got != opErr { //nolint:errorlint // identity is the contract under test
		t.Fatalf("nil cleanup changed error identity: %v", got)
	}
	if got := JoinOnFailure(opErr, cleanupErr); !errors.Is(got, opErr) || !errors.Is(got, cleanupErr) {
		t.Fatalf("joined = %v", got)
	}
}
