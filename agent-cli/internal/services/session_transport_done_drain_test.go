package services

// Regression proof for the transport-done output contract. A session that
// terminates through opts.Done must still render provider output it had
// already accepted, including when the transport also reports an error. The
// error path is exactly the one that must not lose output: it is the run whose
// transcript a caller most needs.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const transportDoneDrainTranscript = "grounded reply that must survive a failed transport"

// transportDoneDrainSession is a minimal provider session. It owns nothing
// beyond the receive buffer the test publishes into, so the assertion below
// can only be satisfied by the session loop's own drain behavior.
type transportDoneDrainSession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
}

var _ messages.Session = (*transportDoneDrainSession)(nil)

func newTransportDoneDrainSession() *transportDoneDrainSession {
	return &transportDoneDrainSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](16),
		done: make(chan struct{}),
	}
}

func (s *transportDoneDrainSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *transportDoneDrainSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *transportDoneDrainSession) Done() <-chan struct{} { return s.done }

func (s *transportDoneDrainSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

type transportDoneDrainInferencer struct {
	session *transportDoneDrainSession
}

func (i *transportDoneDrainInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("transport-done-drain", "test"),
	}) {
		return nil, ctx.Err()
	}
	return i.session, nil
}

// TestSessionTransportDoneDrainsAcceptedOutputWhenTransportErrored pins the
// defect rather than its timing. The provider message is published from inside
// DoneErr, which the session loop calls only after it has selected the
// transport-done branch and before it decides whether to drain. That ordering
// is exact: the message provably cannot already be queued on the loop's delta
// buffer, so rendered output can only come from a drain performed on this
// branch. A loop that cancels first discards it.
func TestSessionTransportDoneDrainsAcceptedOutputWhenTransportErrored(t *testing.T) {
	session := newTransportDoneDrainSession()
	observer := newSessionProgressObserver(nil, nil, "test", "test")

	opened := make(chan struct{})
	var openOnce sync.Once
	observer.streamObserver = func(msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeSessionOpen {
			openOnce.Do(func() { close(opened) })
		}
	}

	transportDone := make(chan struct{})
	transportErr := errors.New("transport reported an incomplete session")
	var publishOnce sync.Once
	doneErr := func() error {
		publishOnce.Do(func() {
			session.recv.Write(context.Background(), messages.StreamMessage{
				Type:  messages.StreamTypeTranscriptDelta,
				Role:  messages.RoleAssistant,
				Value: messages.NewTranscriptDeltaValue(transportDoneDrainTranscript),
			})
		})
		return transportErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Release the transport terminal only once the loop has consumed
	// SESSION.OPEN, so the model runner is connected and the transport-done
	// branch is the only ready case.
	go func() {
		select {
		case <-opened:
		case <-ctx.Done():
		}
		close(transportDone)
	}()

	out := &bytes.Buffer{}
	runErr := runAgentLoopSessionStream(ctx, out, &transportDoneDrainInferencer{session: session}, sessionLoopOptions{
		Done:     transportDone,
		DoneErr:  doneErr,
		observer: observer,
	})

	if !errors.Is(runErr, transportErr) {
		t.Fatalf("transport-done run error = %v, want the reported transport failure %v", runErr, transportErr)
	}
	if !strings.Contains(out.String(), transportDoneDrainTranscript) {
		t.Fatalf("transport-done shutdown discarded accepted provider output; want %q in session output:\n%s",
			transportDoneDrainTranscript, out.String())
	}
}

// TestSessionDurationTransportDoneDrainsAcceptedOutputWhenTransportErrored is
// the same invariant for the duration-bounded session loop, which is a second
// implementation of the same lifecycle with its own drain decision. It is
// reached by `agent session --replay --audio-out --max-duration` without
// --audio-in, and by room runs, both of which supply a live Done/DoneErr pair.
func TestSessionDurationTransportDoneDrainsAcceptedOutputWhenTransportErrored(t *testing.T) {
	session := newTransportDoneDrainSession()
	observer := newSessionProgressObserver(nil, nil, "test", "test")

	opened := make(chan struct{})
	var openOnce sync.Once
	observer.streamObserver = func(msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeSessionOpen {
			openOnce.Do(func() { close(opened) })
		}
	}

	transportDone := make(chan struct{})
	transportErr := errors.New("transport reported an incomplete session")
	var publishOnce sync.Once
	doneErr := func() error {
		publishOnce.Do(func() {
			session.recv.Write(context.Background(), messages.StreamMessage{
				Type:  messages.StreamTypeTranscriptDelta,
				Role:  messages.RoleAssistant,
				Value: messages.NewTranscriptDeltaValue(transportDoneDrainTranscript),
			})
		})
		return transportErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		select {
		case <-opened:
		case <-ctx.Done():
		}
		close(transportDone)
	}()

	out := &bytes.Buffer{}
	runErr := runAgentLoopSessionWithDurationClock(ctx, out, &transportDoneDrainInferencer{session: session}, sessionLoopOptions{
		Done:     transportDone,
		DoneErr:  doneErr,
		observer: observer,
	}, 30*time.Second, realSessionDurationClock{})

	if !errors.Is(runErr, transportErr) {
		t.Fatalf("duration transport-done run error = %v, want the reported transport failure %v", runErr, transportErr)
	}
	if !strings.Contains(out.String(), transportDoneDrainTranscript) {
		t.Fatalf("duration transport-done shutdown discarded accepted provider output; want %q in session output:\n%s",
			transportDoneDrainTranscript, out.String())
	}
}
