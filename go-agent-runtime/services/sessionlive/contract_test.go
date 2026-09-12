package sessionlive

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type responseSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
}

func (s *responseSession) Send(context.Context, messages.StreamMessage) bool      { return true }
func (s *responseSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }
func (s *responseSession) Done() <-chan struct{}                                  { return s.done }
func (s *responseSession) Close() error                                           { return nil }
func (s *responseSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}
func (*responseSession) SupportsResponseRequests() bool { return true }

func TestResponseRequestAndFirstTurnHelpers(t *testing.T) {
	session := &responseSession{recv: messages.NewTypedBuffer[messages.StreamMessage](1), done: make(chan struct{})}
	capabilities := SessionCapabilities{}
	if !capabilities.SupportsResponseRequests(session) {
		t.Fatal("response capability not reported")
	}
	if outcome := capabilities.RequestResponse(context.Background(), session); !outcome.OK() {
		t.Fatalf("response request outcome = %#v, want success", outcome)
	}
	ack := make(chan error, 1)
	ack <- nil
	if err := capabilities.WaitForFirstTurn(context.Background(), ack, platformclock.Real{}, 0); err != nil {
		t.Fatalf("WaitForFirstTurn: %v", err)
	}
}

func TestStreamBuffersFactoryAndFirstTurnTimeout(t *testing.T) {
	if (StreamBuffers{}).New(1) == nil {
		t.Fatal("StreamBuffers.New returned nil")
	}
	clock := platformclock.NewDeterministic(time.Unix(0, 0).UTC(), time.Second)
	result := make(chan error, 1)
	go func() {
		result <- (SessionCapabilities{}).WaitForFirstTurn(context.Background(), make(chan error), clock, time.Second)
	}()
	time.Sleep(time.Millisecond)
	clock.AdvanceBy(time.Second)
	select {
	case err := <-result:
		if err == nil || err.Error() != "timed out awaiting session first user turn acceptance" {
			t.Fatalf("timeout error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first-turn timeout did not fire")
	}
}

func TestFirstTurnWaitPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ack := make(chan error)
	result := make(chan error, 1)
	go func() {
		result <- (SessionCapabilities{}).WaitForFirstTurn(ctx, ack, platformclock.Real{}, time.Minute)
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first-turn cancellation did not return")
	}
}
