package codexlive_test

import (
	"context"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexlive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// testDeadline bounds each test on the host clock. The route runs a real
// pion peer over a virtual network and an httptest server, neither of which
// can join a synctest bubble; a healthy run takes about a second.
const testDeadline = 30 * time.Second

const (
	testVersion = "1.2.3-test"
	firstSeg    = "live_seg_1"
	secondSeg   = "live_seg_2"
	firstUtt    = "live_utt_1"
	completed   = "completed"
	cancelled   = "cancelled"
)

// rig is one fake ChatGPT backend on a virtual network, and a signed-in
// ChatGPT store.
type rig struct {
	backend *fakecodex.Backend
	server  *httptest.Server
	network *fakecodex.VirtualNetwork
	store   *chatgptauth.FileStore
}

func newRig(t *testing.T, options ...fakecodex.Option) *rig {
	t.Helper()
	network, err := fakecodex.NewVirtualNetwork()
	if err != nil {
		t.Fatal(err)
	}
	backend := fakecodex.New(append([]fakecodex.Option{fakecodex.WithPeerNetwork(network)}, options...)...)
	server := httptest.NewServer(backend)
	t.Cleanup(func() {
		server.Close()
		if err := backend.Close(); err != nil {
			t.Errorf("close backend: %v", err)
		}
		if err := network.Close(); err != nil {
			t.Errorf("close network: %v", err)
		}
	})
	return &rig{backend: backend, server: server, network: network, store: signIn(t, fakecodex.DefaultToken, fakecodex.DefaultAccountID)}
}

// signIn saves a ChatGPT login that needs no refresh in a fresh store.
func signIn(t *testing.T, token, accountID string) *chatgptauth.FileStore {
	t.Helper()
	store := chatgptauth.NewFileStore(filepath.Join(t.TempDir(), "auth", "chatgpt.json"))
	if token == "" {
		return store
	}
	if err := store.Save(chatgptauth.Credential{
		AccessToken: token, RefreshToken: "refresh", AccountID: accountID,
		ExpiresAt: time.Now().Add(24 * time.Hour), LastRefresh: time.Now(),
	}); err != nil {
		t.Fatalf("save the fake login: %v", err)
	}
	return store
}

func (r *rig) transport() codexlive.Transport {
	return codexlive.Transport{
		BackendURL:      r.server.URL + "/backend-api/codex",
		SidebandBaseURL: "ws" + strings.TrimPrefix(r.server.URL, "http") + "/v1/live",
		HTTPClient:      r.server.Client(),
		PeerSettings:    r.network.Client,
	}
}

func (r *rig) provider(options ...codexlive.Option) *codexlive.Provider {
	manager := chatgptauth.NewManager(r.store, chatgptauth.NewClient(chatgptauth.Config{}))
	return codexlive.New(append([]codexlive.Option{
		codexlive.WithCredentials(codexlive.ChatGPTCredentials(manager)),
		codexlive.WithTransport(r.transport()),
		codexlive.WithClientVersion(testVersion),
	}, options...)...)
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testDeadline)
	t.Cleanup(cancel)
	return ctx
}

func codexConfig() models.SessionConfig {
	return models.SessionConfig{Model: quicksilver.ModelCodex, Instructions: "Speak briefly."}
}

// connect opens a session against the rig and closes it when the test ends.
func (r *rig) connect(t *testing.T, ctx context.Context) messages.Session {
	t.Helper()
	session, err := r.provider().ConnectSession(ctx, codexConfig())
	if err != nil {
		t.Fatalf("ConnectSession: %v (backend errors %v)", err, r.backend.Errors())
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close session: %v", err)
		}
	})
	r.awaitSideband(t, ctx)
	return session
}

// awaitSideband waits until the backend has registered the sideband the
// client attached, so Send, DropSideband and HangUp reach it: the client's
// handshake can finish before the fake's handler records the connection.
func (r *rig) awaitSideband(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := r.backend.WaitSidebands(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

// next reads one message, failing at the deadline.
func next(t *testing.T, ctx context.Context, session messages.Session) messages.StreamMessage {
	t.Helper()
	select {
	case msg, ok := <-session.Receive().Chan():
		if !ok {
			t.Fatal("receive buffer closed")
		}
		if msg.ResponsePurpose != "" {
			t.Fatalf("message %s carries response purpose %q", msg.Type, msg.ResponsePurpose)
		}
		return msg
	case <-ctx.Done():
		t.Fatalf("no message before the deadline: %v", context.Cause(ctx))
		return messages.StreamMessage{}
	}
}

// until reads messages until one of type stop, skipping the peer's audio
// when skipAudio is set, and returns them all.
func until(t *testing.T, ctx context.Context, session messages.Session, stop messages.StreamMessageType) []messages.StreamMessage {
	t.Helper()
	var out []messages.StreamMessage
	for {
		msg := next(t, ctx, session)
		out = append(out, msg)
		if msg.Type == stop {
			return out
		}
	}
}

func skipOpen(t *testing.T, ctx context.Context, session messages.Session) {
	t.Helper()
	for _, want := range []messages.StreamMessageType{messages.StreamTypeSessionOpen, messages.StreamTypeSessionCreated} {
		if got := next(t, ctx, session); got.Type != want {
			t.Fatalf("message = %s, want %s", got.Type, want)
		}
	}
}

func typesOf(msgs []messages.StreamMessage) []messages.StreamMessageType {
	out := make([]messages.StreamMessageType, len(msgs))
	for i, msg := range msgs {
		out[i] = msg.Type
	}
	return out
}

func sendAudio(t *testing.T, ctx context.Context, session messages.Session, audio []byte) {
	t.Helper()
	outcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(audio)})
	if !outcome.OK() {
		t.Fatalf("send audio: %+v", outcome)
	}
}

// tone is n samples of a sine at hz sampled at rate, continuing from offset.
func tone(hz float64, rate, offset, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(8000 * math.Sin(2*math.Pi*hz*float64(offset+i)/float64(rate)))
	}
	return out
}

// crossingsPerSecond counts upward zero crossings of samples at rate, scaled
// to one second: a sine at hz gives about hz.
func crossingsPerSecond(samples []int16, rate int) float64 {
	crossings := 0
	for i := 1; i < len(samples); i++ {
		if samples[i-1] < 0 && samples[i] >= 0 {
			crossings++
		}
	}
	return float64(crossings) * float64(rate) / float64(len(samples))
}

// sendPeerTone writes frames of a 48 kHz sine from the backend's peer.
func (r *rig) sendPeerTone(t *testing.T, ctx context.Context, hz float64, frames int) {
	t.Helper()
	peer := r.backend.Peer()
	if peer == nil {
		t.Fatal("the backend has no answer peer")
	}
	for i := range frames {
		if err := peer.WriteFrame(ctx, tone(hz, codexrtc.SampleRate, i*codexrtc.FrameSamples, codexrtc.FrameSamples)); err != nil {
			t.Fatalf("backend peer write: %v", err)
		}
	}
}

func messageEnd(t *testing.T, msg messages.StreamMessage) *messages.MessageEndValue {
	t.Helper()
	value, ok := msg.Value.(*messages.MessageEndValue)
	if msg.Type != messages.StreamTypeMessageEnd || !ok {
		t.Fatalf("message = %s %T, want MESSAGE.END", msg.Type, msg.Value)
	}
	return value
}

func sessionClose(t *testing.T, msg messages.StreamMessage) *messages.SessionCloseValue {
	t.Helper()
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if msg.Type != messages.StreamTypeSessionClose || !ok {
		t.Fatalf("message = %s %T, want SESSION.CLOSE", msg.Type, msg.Value)
	}
	return value
}

func outputText(text string) quicksilver.OutputTranscriptAdded {
	return quicksilver.OutputTranscriptAdded{Item: quicksilver.TranscriptItem{Text: text}}
}

func inputText(text string) quicksilver.InputTranscriptAdded {
	return quicksilver.InputTranscriptAdded{Item: quicksilver.TranscriptItem{Type: "input_transcript", Text: text}}
}

func turnDone(role, transcript string) quicksilver.TurnDone {
	return quicksilver.TurnDone{Turn: quicksilver.Turn{Role: role, Transcript: transcript}}
}
