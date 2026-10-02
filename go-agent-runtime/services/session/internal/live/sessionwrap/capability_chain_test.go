package sessionwrap

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// chainProvider is a provider session with no optional capability.
type chainProvider struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   []string
}

func newChainProvider() *chainProvider {
	return &chainProvider{receive: messages.NewTypedBuffer[messages.StreamMessage](8), done: make(chan struct{})}
}

func (p *chainProvider) record(call string) {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
}

func (p *chainProvider) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *chainProvider) Send(_ context.Context, msg messages.StreamMessage) bool {
	p.record(string(msg.Type))
	return true
}
func (p *chainProvider) Receive() *messages.TypedBuffer[messages.StreamMessage] { return p.receive }
func (p *chainProvider) Done() <-chan struct{}                                  { return p.done }
func (p *chainProvider) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// capableChainProvider adds every optional capability to chainProvider.
type capableChainProvider struct{ *chainProvider }

func (p capableChainProvider) ProviderTurnDetection() bool { return true }
func (p capableChainProvider) FullDuplex() bool            { return true }
func (p capableChainProvider) InputAudioSampleRate() int   { return 24000 }
func (p capableChainProvider) LocalPlayback() messages.LocalPlaybackState {
	return messages.LocalPlaybackState{Active: true, Level: 900}
}
func (p capableChainProvider) InterruptLocalPlayback(context.Context) bool {
	p.record("interrupt playback")
	return true
}
func (p capableChainProvider) SyncReceive(context.Context) { p.record("sync") }
func (p capableChainProvider) RequestResponse(context.Context) messages.SessionSendOutcome {
	p.record("request response")
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}
func (p capableChainProvider) SendMessage(context.Context, messages.Message) bool {
	p.record("complete message")
	return true
}

type chainInferencer struct{ session messages.Session }

func (i chainInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

// connectProductionChain connects provider through the live service's wrapper
// chain: provider recording, terminal drain, media capture and ordered
// admission.
func connectProductionChain(t *testing.T, provider messages.Session) messages.Session {
	t.Helper()
	return connectChain(t, provider, session.LiveRequest{})
}

// connectChain connects provider through the wrapper chain the live service
// builds for request.
func connectChain(t *testing.T, provider messages.Session, request session.LiveRequest) messages.Session {
	t.Helper()
	ctx := context.Background()
	recording := gatewaytesting.NewRecordingSessionInferencer(chainInferencer{session: provider})
	factory := Factory(func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) { return recording, nil }, 8)
	inferencer, err := factory(ctx, request)
	if err != nil {
		t.Fatalf("live factory: %v", err)
	}
	connected, err := NewCapturingInferencer(CapturingInferencerOptions{Inner: inferencer, Media: mediagate.New(nil)}).ConnectSession(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := connected.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return connected
}

// The session runner's barge-in reaches the provider through every production
// wrapper: it reads turn detection, input rate and playback, interrupts local
// playback, cancels the response, syncs the provider relay and requests the
// next response.
func TestProductionWrapperChainRelaysBargeInCapabilities(t *testing.T) {
	provider := newChainProvider()
	outer := connectProductionChain(t, capableChainProvider{provider})
	capable, ok := outer.(messages.BargeInCapableSession)
	if !ok || !capable.ProviderTurnDetection() || !capable.FullDuplex() || capable.InputAudioSampleRate() != 24000 || capable.LocalPlayback().Level != 900 {
		t.Fatal("barge-in answers did not cross the wrapper chain")
	}
	ctx := context.Background()
	if !capable.InterruptLocalPlayback(ctx) {
		t.Fatal("playback interrupt did not cross the wrapper chain")
	}
	if !outer.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeResponseCancel}) {
		t.Fatal("response cancel did not cross the wrapper chain")
	}
	if !provider.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
		t.Fatal("queue provider message")
	}
	requireSyncedMessage(t, ctx, provider, outer, capable)
	if !messages.SupportsSessionResponseRequests(outer) || !messages.RequestSessionResponse(ctx, outer).OK() {
		t.Fatal("response request did not cross the wrapper chain")
	}
	if !messages.SupportsSessionMessages(outer) || !messages.SendSessionMessage(ctx, outer, messages.Message{ToolCallID: "call"}) {
		t.Fatal("complete message did not cross the wrapper chain")
	}
	want := []string{"interrupt playback", string(messages.StreamTypeResponseCancel), "sync", string(messages.StreamTypeResponseCreate), "complete message"}
	if got := provider.recorded(); !slices.Equal(got, want) {
		t.Fatalf("provider calls = %v, want %v", got, want)
	}
}

// requireSyncedMessage queues a provider message and requires it to be
// readable from outer as soon as SyncReceive returns.
func requireSyncedMessage(t *testing.T, ctx context.Context, provider *chainProvider, outer messages.Session, capable messages.BargeInCapableSession) {
	t.Helper()
	if !provider.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
		t.Fatal("queue provider message")
	}
	capable.SyncReceive(ctx)
	msg, ok := outer.Receive().Read()
	if !ok || msg.Type != messages.StreamTypeTextDelta {
		t.Fatalf("after SyncReceive read (%s, %v), want the queued provider message", msg.Type, ok)
	}
}

// A turn replay renders provider audio through its own media, so through the
// replay chain playback and media are answered by the replay while every
// other capability still reaches the provider.
func TestTurnReplayWrapperChainRelaysCapabilitiesAndOwnsPlayback(t *testing.T) {
	provider := newChainProvider()
	outer := connectChain(t, capableChainProvider{provider}, session.LiveRequest{Replay: session.LiveReplayPolicy{Kind: session.LiveReplayKindTurn}})
	capable, ok := outer.(messages.BargeInCapableSession)
	if !ok || !capable.ProviderTurnDetection() || !capable.FullDuplex() || capable.InputAudioSampleRate() != 24000 {
		t.Fatal("barge-in answers did not cross the turn replay chain")
	}
	if capable.LocalPlayback().Level == 900 {
		t.Fatal("the provider's playback leaked through the turn replay, which owns playback")
	}
	if media, ok := messages.SessionMedia(outer); !ok || media.Inbound == nil {
		t.Fatal("the turn replay's own media was not advertised")
	}
	ctx := context.Background()
	capable.InterruptLocalPlayback(ctx)
	requireSyncedMessage(t, ctx, provider, outer, capable)
	if !messages.RequestSessionResponse(ctx, outer).OK() || !messages.SendSessionMessage(ctx, outer, messages.Message{ToolCallID: "call"}) {
		t.Fatal("response request or complete message did not cross the turn replay chain")
	}
	want := []string{"sync", string(messages.StreamTypeResponseCreate), "complete message"}
	if got := provider.recorded(); !slices.Equal(got, want) {
		t.Fatalf("provider calls = %v, want %v", got, want)
	}
}

// Every production wrapper has every forwarding method, so none may advertise
// a capability the provider lacks.
func TestProductionWrapperChainDoesNotAdvertiseMissingCapabilities(t *testing.T) {
	provider := newChainProvider()
	outer := connectProductionChain(t, provider)
	ctx := context.Background()
	capable, ok := outer.(messages.BargeInCapableSession)
	if !ok {
		t.Fatal("the wrapper chain does not expose the barge-in capabilities")
	}
	capable.SyncReceive(ctx)
	if capable.ProviderTurnDetection() || capable.FullDuplex() || capable.InputAudioSampleRate() != 0 || capable.LocalPlayback().Active || capable.InterruptLocalPlayback(ctx) {
		t.Fatal("a barge-in capability the provider lacks was advertised")
	}
	if messages.SupportsSessionResponseRequests(outer) || messages.RequestSessionResponse(ctx, outer).OK() {
		t.Fatal("response requests were advertised")
	}
	if messages.SupportsSessionMessages(outer) || messages.SendSessionMessage(ctx, outer, messages.Message{}) ||
		messages.SupportsSessionMessagesWithoutResponse(outer) {
		t.Fatal("complete messages were advertised")
	}
	if _, ok := messages.SessionMedia(outer); ok {
		t.Fatal("media was advertised")
	}
	if got := provider.recorded(); len(got) != 0 {
		t.Fatalf("provider received %v for capabilities it lacks", got)
	}
}
