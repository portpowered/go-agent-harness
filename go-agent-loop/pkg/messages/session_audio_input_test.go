package messages_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// bareSession is a session with no optional capability.
type bareSession struct{}

func (bareSession) Send(context.Context, messages.StreamMessage) bool { return true }
func (bareSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return nil
}
func (bareSession) Done() <-chan struct{} { return nil }
func (bareSession) Close() error          { return nil }

// fullSession implements every optional capability and records each call.
type fullSession struct {
	bareSession
	calls     []string
	responses bool
	media     audio.MediaEndpoints
}

func (s *fullSession) record(call string)          { s.calls = append(s.calls, call) }
func (s *fullSession) ProviderTurnDetection() bool { return true }
func (s *fullSession) FullDuplex() bool            { return true }
func (s *fullSession) InputAudioSampleRate() int   { return 24000 }
func (s *fullSession) LocalPlayback() messages.LocalPlaybackState {
	return messages.LocalPlaybackState{Active: true, Level: 1234}
}
func (s *fullSession) InterruptLocalPlayback(context.Context) bool {
	s.record("interrupt")
	return true
}
func (s *fullSession) SyncReceive(context.Context) { s.record("sync") }
func (s *fullSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	s.record("request")
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}
func (s *fullSession) SupportsResponseRequests() bool { return s.responses }
func (s *fullSession) SendMessage(context.Context, messages.Message) bool {
	s.record("message")
	return true
}
func (s *fullSession) SendMessageWithoutResponse(context.Context, messages.Message) bool {
	s.record("message without response")
	return true
}
func (s *fullSession) FlushOutbound(context.Context) error { return errors.New("flush") }
func (s *fullSession) TerminalError() error                { return errors.New("terminal") }
func (s *fullSession) InputDrops() int64                   { return 3 }
func (s *fullSession) OutputDrops() int64                  { return 4 }
func (s *fullSession) InitialSessionConfigSent() bool      { return true }
func (s *fullSession) RTCMedia() audio.MediaEndpoints      { return s.media }
func (s *fullSession) RTCMediaWithOptions(audio.MediaSessionOptions) audio.MediaEndpoints {
	s.record("media options")
	return s.media
}

// wrapper is the smallest session wrapper: it changes nothing.
type wrapper struct {
	messages.Session
	messages.SessionCapabilities
}

func wrap(inner messages.Session) *wrapper {
	return &wrapper{Session: inner, SessionCapabilities: messages.SessionCapabilities{Wrapped: inner}}
}

// ownMedia renders media itself instead of relaying the wrapped session's.
type ownMedia struct {
	*wrapper
	media audio.MediaEndpoints
}

func (s ownMedia) RTCMedia() audio.MediaEndpoints { return s.media }
func (s ownMedia) RTCMediaWithOptions(audio.MediaSessionOptions) audio.MediaEndpoints {
	return s.media
}
func (ownMedia) SupportsRTCMedia() bool { return true }

type inbound struct{ audio.InboundMedia }

func TestSessionCapabilitiesRelayEveryCapabilityThroughNestedWrappers(t *testing.T) {
	provider := &fullSession{responses: true, media: audio.MediaEndpoints{Inbound: inbound{}}}
	relay := wrap(wrap(provider))
	var session messages.Session = relay
	var capable messages.BargeInCapableSession = relay
	if !capable.ProviderTurnDetection() || !capable.FullDuplex() || capable.InputAudioSampleRate() != 24000 || capable.LocalPlayback().Level != 1234 {
		t.Fatal("barge-in answers were not relayed")
	}
	ctx := context.Background()
	relayCapabilityCalls(t, ctx, session, capable)
	want := []string{"sync", "interrupt", "request", "message", "message without response", "media options"}
	if !slices.Equal(provider.calls, want) {
		t.Fatalf("provider calls = %v, want %v", provider.calls, want)
	}
	if relay.FlushOutbound(ctx) == nil || relay.TerminalError() == nil || relay.InputDrops() != 3 || relay.OutputDrops() != 4 || !relay.InitialSessionConfigSent() {
		t.Fatal("transport capabilities were not relayed")
	}
}

// relayCapabilityCalls makes every capability call through session.
func relayCapabilityCalls(t *testing.T, ctx context.Context, session messages.Session, capable messages.BargeInCapableSession) {
	t.Helper()
	capable.SyncReceive(ctx)
	if !capable.InterruptLocalPlayback(ctx) ||
		!messages.SupportsSessionResponseRequests(session) || !messages.RequestSessionResponse(ctx, session).OK() ||
		!messages.SupportsSessionMessages(session) || !messages.SendSessionMessage(ctx, session, messages.Message{}) ||
		!messages.SupportsSessionMessagesWithoutResponse(session) || !messages.SendSessionMessageWithoutResponse(ctx, session, messages.Message{}) {
		t.Fatal("capability calls were not relayed")
	}
	if media, ok := messages.SessionMediaWithOptions(session, audio.MediaSessionOptions{InboundContinuous: true}); !ok || media.Inbound == nil {
		t.Fatal("media was not relayed")
	}
}

// A wrapper always has every forwarding method, so it must not advertise a
// capability its wrapped session lacks.
func TestSessionCapabilitiesDoNotAdvertiseMissingCapabilities(t *testing.T) {
	ctx := context.Background()
	for name, relay := range map[string]*wrapper{"bare": wrap(wrap(bareSession{})), "nil": wrap(nil)} {
		var session messages.Session = relay
		if messages.SupportsSessionResponseRequests(session) || messages.RequestSessionResponse(ctx, session).OK() {
			t.Fatalf("%s: response requests advertised", name)
		}
		if messages.SupportsSessionMessages(session) || messages.SendSessionMessage(ctx, session, messages.Message{}) ||
			messages.SupportsSessionMessagesWithoutResponse(session) || messages.SendSessionMessageWithoutResponse(ctx, session, messages.Message{}) {
			t.Fatalf("%s: complete messages advertised", name)
		}
		if _, ok := messages.SessionMedia(session); ok || messages.SupportsSessionMedia(session) {
			t.Fatalf("%s: media advertised", name)
		}
		var capable messages.BargeInCapableSession = relay
		capable.SyncReceive(ctx)
		if capable.ProviderTurnDetection() || capable.FullDuplex() || capable.InputAudioSampleRate() != 0 || capable.LocalPlayback().Active || capable.InterruptLocalPlayback(ctx) {
			t.Fatalf("%s: barge-in capability advertised", name)
		}
		if relay.FlushOutbound(ctx) != nil || relay.TerminalError() != nil || relay.InputDrops() != 0 || relay.InitialSessionConfigSent() {
			t.Fatalf("%s: transport capability advertised", name)
		}
	}
}

// A provider that has the method but reports the capability unsupported keeps
// it unsupported through every wrapper.
func TestSessionCapabilitiesRelayTheWrappedCapabilityAnswer(t *testing.T) {
	provider := &fullSession{responses: false}
	session := wrap(wrap(provider))
	if messages.SupportsSessionResponseRequests(session) || messages.RequestSessionResponse(context.Background(), session).OK() || len(provider.calls) != 0 {
		t.Fatal("an unsupported response request was advertised or sent")
	}
}

// A wrapper that renders its own media advertises it although the wrapped
// session has none.
func TestSessionCapabilitiesOverrideAnswersForTheWrapper(t *testing.T) {
	own := ownMedia{wrapper: wrap(bareSession{}), media: audio.MediaEndpoints{Inbound: inbound{}}}
	session := wrap(own)
	if media, ok := messages.SessionMediaWithOptions(session, audio.MediaSessionOptions{}); !ok || media.Inbound == nil {
		t.Fatal("the wrapper's own media was not advertised")
	}
}

func TestSessionAudioInputPolicyInterruptsResponseDefaultsSafely(t *testing.T) {
	tests := []struct {
		name   string
		policy messages.SessionAudioInputPolicy
		want   bool
	}{
		{name: "default", policy: messages.SessionAudioInputPolicyDefault, want: true},
		{name: "explicit interrupt", policy: messages.SessionAudioInputPolicyInterrupt, want: true},
		{name: "peer agent", policy: messages.SessionAudioInputPolicyDoNotInterrupt, want: false},
		{name: "unknown", policy: messages.SessionAudioInputPolicy("future-policy"), want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.InterruptsResponse(); got != test.want {
				t.Fatalf("InterruptsResponse(%q) = %t, want %t", test.policy, got, test.want)
			}
		})
	}
}

func TestTypedBufferTryWriteDropsWhenFull(t *testing.T) {
	buf := messages.NewTypedBuffer[int](1)
	var dropped []int
	buf.SetOnDrop(func(value int) { dropped = append(dropped, value) })
	if outcome := buf.TryWrite(1); !outcome.OK() {
		t.Fatalf("first TryWrite = %+v, want success", outcome)
	}
	if outcome := buf.TryWrite(2); outcome.Status != messages.BufferWriteBufferFull {
		t.Fatalf("full TryWrite = %+v, want buffer full", outcome)
	}
	if buf.Drops() != 1 || len(dropped) != 1 || dropped[0] != 2 {
		t.Fatalf("drops = %d, observed %v, want the second value dropped once", buf.Drops(), dropped)
	}
}
