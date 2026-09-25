package sessionwrap

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// TestTurnReplayEndsInboundCleanlyAfterOwnerCancellation pins the clean end
// of a turn replay: the owner cancels the provider's run context when it
// stops the session, and the provider reports that cancellation. The inbound
// stream must still end as a closed stream once the owner closes the session,
// or a graceful playback drain fails a clean session with the cancellation.
func TestTurnReplayEndsInboundCleanlyAfterOwnerCancellation(t *testing.T) {
	errProviderTransport := errors.New("provider transport failed")
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0}
	for _, tc := range []struct {
		name     string
		innerErr error
		want     error
	}{
		{name: "owner cancellation", innerErr: context.Canceled, want: sharedaudio.ErrSessionMediaClosed},
		{name: "provider failure", innerErr: errProviderTransport, want: errProviderTransport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := newEndedReplaySession(t, pcm, tc.innerErr)
			wrapped := newMediaSession(t.Context(), inner, 16000, false)
			<-wrapped.Done()
			if err := wrapped.Close(); err != nil {
				t.Fatalf("close turn replay: %v", err)
			}
			inbound := wrapped.RTCMedia().Inbound
			samples := 0
			for {
				frame, err := inbound.ReadFrame(t.Context())
				if err != nil {
					if !errors.Is(err, tc.want) {
						t.Fatalf("inbound ended with %v, want %v", err, tc.want)
					}
					break
				}
				samples += len(frame.Samples)
			}
			if samples != len(pcm)/2 {
				t.Fatalf("inbound delivered %d samples, want %d", samples, len(pcm)/2)
			}
		})
	}
}

// endedReplaySession is a provider session that already delivered one audio
// response and ended, reporting err as its terminal state.
type endedReplaySession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	err     error
}

func newEndedReplaySession(t *testing.T, pcm []byte, err error) *endedReplaySession {
	t.Helper()
	s := &endedReplaySession{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{}), err: err}
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "r1", Value: messages.NewAudioDeltaValue(pcm)},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: "r1"},
	} {
		if !s.receive.Write(t.Context(), msg) {
			t.Fatalf("queue %s", msg.Type)
		}
	}
	close(s.done)
	return s
}

func (*endedReplaySession) Send(context.Context, messages.StreamMessage) bool { return false }
func (s *endedReplaySession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *endedReplaySession) Done() <-chan struct{} { return s.done }
func (s *endedReplaySession) Err() error            { return s.err }
func (*endedReplaySession) Close() error            { return nil }
