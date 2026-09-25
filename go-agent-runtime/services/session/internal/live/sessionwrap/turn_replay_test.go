package sessionwrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// TestTurnReplayEndsInboundCleanlyOnlyAfterOwnerStop pins how a turn replay
// ends its inbound stream. When the owner stops the session, by closing it or
// cancelling the run context, the provider reports that cancellation; it is
// not a media failure, so the stream ends as a closed stream and a graceful
// playback drain completes. A provider error, including one that wraps
// context.Canceled without an owner stop, still fails the stream.
func TestTurnReplayEndsInboundCleanlyOnlyAfterOwnerStop(t *testing.T) {
	transportErr := errors.New("provider transport failed")
	unrequested := fmt.Errorf("provider transport: %w", context.Canceled)
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0}
	for _, tc := range []struct {
		name string
		stop func(*mediaSession, context.CancelFunc) <-chan error
		pcm  []byte
		err  error
		want error
	}{
		// Audio is only asserted where the provider has ended before the
		// wrapper can observe a stop, so no stop can race message forwarding.
		{name: "owner close", stop: closeTurnReplay, pcm: pcm, err: context.Canceled, want: sharedaudio.ErrSessionMediaClosed},
		{name: "owner context cancellation", stop: cancelTurnReplay, err: context.Canceled, want: sharedaudio.ErrSessionMediaClosed},
		{name: "unrequested cancellation", pcm: pcm, err: unrequested, want: unrequested},
		{name: "provider failure", pcm: pcm, err: transportErr, want: transportErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inner := newReplayProvider(t, ctx, tc.pcm, tc.err, tc.stop == nil)
			wrapped := newMediaSession(ctx, inner, 16000, false)
			endTurnReplay(t, wrapped, tc.stop, cancel)
			samples, err := drainTurnReplayInbound(t, wrapped)
			if !errors.Is(err, tc.want) {
				t.Fatalf("inbound ended with %v, want %v", err, tc.want)
			}
			if samples != len(tc.pcm)/2 {
				t.Fatalf("inbound delivered %d samples, want %d", samples, len(tc.pcm)/2)
			}
		})
	}
}

// endTurnReplay applies the owner stop, if any, and closes the session once
// its forwarder has finished, as the live owner does.
func endTurnReplay(t *testing.T, s *mediaSession, stop func(*mediaSession, context.CancelFunc) <-chan error, cancel context.CancelFunc) {
	t.Helper()
	var stopped <-chan error
	if stop != nil {
		stopped = stop(s, cancel)
	}
	<-s.Done()
	if err := s.Close(); err != nil {
		t.Fatalf("close turn replay: %v", err)
	}
	if stopped == nil {
		return
	}
	if err := <-stopped; err != nil {
		t.Fatalf("owner stop: %v", err)
	}
}

func closeTurnReplay(s *mediaSession, _ context.CancelFunc) <-chan error {
	// Close joins the forwarder, so run it beside the test's Done wait.
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	return closed
}

func cancelTurnReplay(_ *mediaSession, cancel context.CancelFunc) <-chan error {
	cancel()
	return nil
}

func drainTurnReplayInbound(t *testing.T, s *mediaSession) (int, error) {
	t.Helper()
	inbound := s.RTCMedia().Inbound
	samples := 0
	for {
		frame, err := inbound.ReadFrame(t.Context())
		if err != nil {
			return samples, err
		}
		samples += len(frame.Samples)
	}
}

// replayProvider is a provider session holding one queued audio response. It
// ends immediately when ended is set, otherwise when the owner closes it or
// cancels its run context; it then reports err as its terminal state.
type replayProvider struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
	err     error
}

func newReplayProvider(t *testing.T, ctx context.Context, pcm []byte, err error, ended bool) *replayProvider {
	t.Helper()
	p := &replayProvider{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{}), err: err}
	if len(pcm) > 0 {
		for _, msg := range []messages.StreamMessage{
			{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "r1", Value: messages.NewAudioDeltaValue(pcm)},
			{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: "r1"},
		} {
			if !p.receive.Write(ctx, msg) {
				t.Fatalf("queue %s", msg.Type)
			}
		}
	}
	if ended {
		p.end()
	} else {
		context.AfterFunc(ctx, p.end)
	}
	return p
}

func (p *replayProvider) end() { p.once.Do(func() { close(p.done) }) }

func (*replayProvider) Send(context.Context, messages.StreamMessage) bool { return false }
func (p *replayProvider) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return p.receive
}
func (p *replayProvider) Done() <-chan struct{} { return p.done }

// Err reports the terminal state only once the session has ended.
func (p *replayProvider) Err() error {
	select {
	case <-p.done:
		return p.err
	default:
		return nil
	}
}

func (p *replayProvider) Close() error {
	p.end()
	return nil
}
