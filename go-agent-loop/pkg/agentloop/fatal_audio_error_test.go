package agentloop

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// fatalAudioSession rejects the first provider-bound audio frame while
// retaining the normal Session contract. The initial SESSION.OPEN is queued
// before Run starts, so the test has no scheduler or provider timing race.
type fatalAudioSession struct {
	recv  *messages.TypedBuffer[messages.StreamMessage]
	done  chan struct{}
	once  sync.Once
	cause error
}

func (s *fatalAudioSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *fatalAudioSession) SendWithOutcome(_ context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if msg.Type == messages.StreamTypeAudioDelta {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: s.cause}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *fatalAudioSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *fatalAudioSession) Done() <-chan struct{} { return s.done }

func (s *fatalAudioSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type fatalAudioSessionInferencer struct{ session messages.Session }

func (i fatalAudioSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func TestDuplexSession_FatalAudioSendErrorWakesOuterRun(t *testing.T) {
	cause := errors.New("provider audio send failed")
	session := &fatalAudioSession{
		recv:  messages.NewTypedBuffer[messages.StreamMessage](4),
		done:  make(chan struct{}),
		cause: cause,
	}
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("fatal-audio", "test"),
	}) {
		t.Fatal("session open was not queued")
	}

	al, err := New(
		WithMode(engine.DuplexSession),
		WithSessionInferencer(fatalAudioSessionInferencer{session: session}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Queue the frame before starting the loop. runSession's preflight drains
	// SESSION.OPEN first and then deterministically forwards this frame.
	if err := al.SendAudioInput(context.Background(), []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("SendAudioInput: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()

	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("Run returned nil after fatal audio send failure")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run waited for the outer timeout instead of publishing the audio failure: %v", err)
		}
		var streamErr *engine.StreamDeltaError
		if !errors.As(err, &streamErr) {
			t.Fatalf("Run error = %v, want *engine.StreamDeltaError", err)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("Run error = %v, want original cause %v", err, cause)
		}
		if streamErr.Value == nil || streamErr.Value.Classification != "session_audio_send_failed" {
			t.Fatalf("Run terminal value = %#v, want session_audio_send_failed", streamErr.Value)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after fatal audio send failure")
	}
}
