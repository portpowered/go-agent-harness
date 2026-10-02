package codexrtc_test

import (
	"context"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// testDeadline bounds each network-backed test; a healthy run takes well
// under a second.
const testDeadline = 20 * time.Second

// answerSDP is a minimal answer for tests that do not negotiate media.
const answerSDP = "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n"

func testIDs() codexrtc.RequestIDs {
	return codexrtc.RequestIDs{SessionID: "session-test", ThreadID: "thread-test", RealtimeSessionID: "realtime-test"}
}

func fixedCredential() codexrtc.CredentialSource {
	return codexrtc.CredentialFunc(func(context.Context) (codexrtc.Credential, error) {
		return codexrtc.Credential{AccessToken: fakecodex.DefaultToken, AccountID: fakecodex.DefaultAccountID}, nil
	})
}

type harness struct {
	backend *fakecodex.Backend
	server  *httptest.Server
	client  *codexrtc.CallClient
}

func newHarness(t *testing.T, credential codexrtc.CredentialSource, options ...fakecodex.Option) *harness {
	t.Helper()
	backend := fakecodex.New(options...)
	server := httptest.NewServer(backend)
	t.Cleanup(func() {
		server.Close()
		if err := backend.Close(); err != nil {
			t.Errorf("close backend: %v", err)
		}
	})
	client, err := codexrtc.NewCallClient(codexrtc.CallConfig{
		Credential:      credential,
		BackendURL:      server.URL + "/backend-api/codex",
		SidebandBaseURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/live",
		HTTPClient:      server.Client(),
		Version:         "test-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{backend: backend, server: server, client: client}
}

func (h *harness) sidebandBase() string {
	return "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/live"
}

func codexSession(t *testing.T) quicksilver.SessionConfig {
	t.Helper()
	session, err := quicksilver.BuildSession(models.SessionConfig{Model: quicksilver.ModelCodex, Instructions: "Speak briefly."})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testDeadline)
	t.Cleanup(cancel)
	return ctx
}

func (h *harness) create(t *testing.T, ctx context.Context, offer string) codexrtc.Call {
	t.Helper()
	call, err := h.client.Create(ctx, codexrtc.CallRequest{OfferSDP: offer, Session: codexSession(t), IDs: testIDs()})
	if err != nil {
		t.Fatalf("create call: %v (backend errors %v)", err, h.backend.Errors())
	}
	return call
}

// tone returns one 20 ms frame of a sine at hz, continuing from frame index.
func tone(hz float64, index int) []int16 {
	frame := make([]int16, codexrtc.FrameSamples)
	for i := range frame {
		at := float64(index*codexrtc.FrameSamples+i) / codexrtc.SampleRate
		frame[i] = int16(8000 * math.Sin(2*math.Pi*hz*at))
	}
	return frame
}
