package codexlive

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// A real sideband whose peer stops reading: the client's writes time out
// and gorilla fails every later write, while reads stay healthy and never
// error. The transport aborts that sideband, so the read loop ends as a loss
// and a new sideband is dialed; the held append is delivered on it, and
// events from the new sideband reach the transport. It runs on the host
// clock (an httptest server cannot join a synctest bubble); nothing waits
// longer than the 100 ms write timeout and the 200 ms reconnect backoff.
func TestASidebandWithABrokenWriteSideIsAbortedAndReconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	backend := fakecodex.New(fakecodex.WithAnswer("v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n"), fakecodex.WithStalledSidebands(1))
	server := httptest.NewServer(backend)
	t.Cleanup(func() {
		server.Close()
		if err := backend.Close(); err != nil {
			t.Errorf("close backend: %v", err)
		}
	})
	client, err := codexrtc.NewCallClient(codexrtc.CallConfig{
		Credential: codexrtc.CredentialFunc(func(context.Context) (codexrtc.Credential, error) {
			return codexrtc.Credential{AccessToken: fakecodex.DefaultToken, AccountID: fakecodex.DefaultAccountID}, nil
		}),
		BackendURL: server.URL + "/backend-api/codex", SidebandBaseURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/live",
		HTTPClient: server.Client(), SidebandWriteTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := quicksilver.BuildSession(models.SessionConfig{Model: quicksilver.ModelCodex})
	if err != nil {
		t.Fatal(err)
	}
	call, err := client.Create(ctx, codexrtc.CallRequest{OfferSDP: "v=offer\r\n", Session: session, IDs: codexrtc.RequestIDs{SessionID: "s", ThreadID: "t", RealtimeSessionID: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	dial := func(ctx context.Context) (control, error) { return client.DialSideband(ctx, call) }
	first, err := dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newConn(ctx, newFakePeer(), first, dial, connConfig{rate: 24000, clock: clock.Real{}, logger: logging.DummyLogger(), policy: defaultReconnectPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := transport.Close(); err != nil {
			t.Errorf("close transport: %v", err)
		}
	})
	// Registered last so it runs first: if the abort regressed, the first
	// sideband's reader would still be blocked and hold transport.Close.
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Logf("close the first sideband: %v", err)
		}
	})
	if err := backend.WaitSidebands(ctx, 1); err != nil {
		t.Fatal(err)
	}

	// Fill the stalled peer's socket buffers until a write times out: the
	// transport then drops the sideband and holds the event.
	big := contextFrame(strings.Repeat("x", 1<<20))
	for sent := 0; transport.currentSideband() != nil; sent++ {
		if sent == 64 {
			t.Fatal("no write failed on the stalled sideband")
		}
		if err := transport.WriteMessage(wireTextMessage, big); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
	}
	if err := backend.WaitSidebands(ctx, 2); err != nil {
		t.Fatalf("the transport never redialed after the failed write: %v", err)
	}
	marker := contextFrame("after the reconnect")
	if err := transport.WriteMessage(wireTextMessage, marker); err != nil {
		t.Fatal(err)
	}
	if !receivedAppend(ctx, backend, "after the reconnect") {
		t.Fatalf("the new sideband never received the append; client events %d, errors %v", len(backend.ClientEvents()), backend.Errors())
	}
	if err := backend.Send(quicksilver.SessionUpdated{}); err != nil {
		t.Fatal(err)
	}
	if updated := awaitFrame(ctx, transport, quicksilver.TypeSessionUpdated); updated != quicksilver.TypeSessionUpdated {
		t.Fatalf("the transport delivered %q, want the new sideband's session.updated", updated)
	}
}

// currentSideband is the sideband writes go to, nil while one is redialed.
func (c *conn) currentSideband() control {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.side
}

// awaitFrame reads frames until one of type want or the transport's end,
// and returns its type; "" when ctx ends first.
func awaitFrame(ctx context.Context, transport *conn, want string) string {
	found := make(chan string, 1)
	go func() {
		for {
			_, frame, err := transport.ReadMessage()
			if err != nil {
				found <- ""
				return
			}
			var head struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(frame, &head) == nil && (head.Type == want || head.Type == typeEnded) {
				found <- head.Type
				return
			}
		}
	}()
	select {
	case got := <-found:
		return got
	case <-ctx.Done():
		return ""
	}
}

// receivedAppend reports whether the backend receives a context append with
// text before ctx ends.
func receivedAppend(ctx context.Context, backend *fakecodex.Backend, text string) bool {
	for n := 1; ; n++ {
		events, err := backend.WaitClientEvents(ctx, n)
		if err != nil {
			return false
		}
		if appended, ok := events[n-1].(quicksilver.SessionContextAppend); ok && len(appended.Content) > 0 && appended.Content[0].Text == text {
			return true
		}
	}
}
