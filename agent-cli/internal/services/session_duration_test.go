package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunSessionWithMaxDuration_RejectsNegativeBeforePlanning(t *testing.T) {
	inferencer := &durationTestInferencer{}
	err := RunSessionWithMaxDuration(context.Background(), io.Discard, SessionRunOptions{
		SessionInferencer: inferencer,
	}, -time.Millisecond)
	if err == nil {
		t.Fatal("negative max duration returned nil")
	}
	var durationErr *SessionMaxDurationError
	if !errors.As(err, &durationErr) {
		t.Fatalf("error type = %T, want *SessionMaxDurationError: %v", err, err)
	}
	if !errors.Is(err, ErrInvalidSessionMaxDuration) {
		t.Fatalf("error does not preserve ErrInvalidSessionMaxDuration: %v", err)
	}
	if inferencer.connected {
		t.Fatal("negative duration started the injected session")
	}
}

func TestRunSessionWithMaxDuration_ZeroDoesNotCreateTimer(t *testing.T) {
	clock := &durationTestClock{}
	var out bytes.Buffer
	err := RunSessionWithMaxDurationClock(context.Background(), &out, SessionRunOptions{
		ReplayPath:        "synthetic.session.json",
		SessionInferencer: &durationTestInferencer{events: durationNaturalEvents()},
	}, 0, clock)
	if err != nil {
		t.Fatalf("zero max duration: %v", err)
	}
	if clock.calls != 0 {
		t.Fatalf("zero max duration created %d timers, want 0", clock.calls)
	}
	if !strings.Contains(out.String(), "accepted output") || strings.Contains(out.String(), string(SessionMaxDurationReason)) {
		t.Fatalf("zero duration did not preserve natural output/reason: %q", out.String())
	}
}

func TestRunSessionWithMaxDuration_GracefullyClosesAtDeadline(t *testing.T) {
	clock := &durationTestClock{}
	writer := newDurationTestWriter()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runAgentLoopSessionWithDurationClock(
			context.Background(),
			writer,
			&durationTestInferencer{events: durationOutputEvents()},
			sessionLoopOptions{},
			time.Minute,
			clock,
		)
	}()

	writer.waitFor(t, "accepted output")
	clock.fire()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("duration cutoff returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("duration cutoff did not finish")
	}

	got := writer.String()
	for _, want := range []string{
		"accepted output",
		"[session closed: max_duration]",
		"terminal_reason=max_duration",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("duration output missing %q: %q", want, got)
		}
	}
	if clock.calls != 1 || !clock.timer.stopped {
		t.Fatalf("duration timer lifecycle = calls:%d stopped:%v, want one stopped timer", clock.calls, clock.timer.stopped)
	}
}

func TestRunSessionWithMaxDuration_NaturalCompletionKeepsNaturalReason(t *testing.T) {
	clock := &durationTestClock{}
	var out bytes.Buffer
	err := runAgentLoopSessionWithDurationClock(
		context.Background(),
		&out,
		&durationTestInferencer{events: durationNaturalEvents()},
		sessionLoopOptions{},
		time.Hour,
		clock,
	)
	if err != nil {
		t.Fatalf("natural completion: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "terminal_reason=provider_close") {
		t.Fatalf("natural completion lost provider reason: %q", got)
	}
	if strings.Contains(got, string(SessionMaxDurationReason)) {
		t.Fatalf("natural completion was mislabeled as max duration: %q", got)
	}
	if clock.calls != 1 || !clock.timer.stopped {
		t.Fatalf("natural timer lifecycle = calls:%d stopped:%v, want one stopped timer", clock.calls, clock.timer.stopped)
	}
}

func durationOutputEvents() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("duration-session", "test")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("accepted output")},
	}
}

func durationNaturalEvents() []messages.StreamMessage {
	return append(durationOutputEvents(), messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"duration-session",
			"provider_closed",
			string(messages.TerminalReasonProviderClose),
			messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNotApplicable,
		),
	})
}

type durationTestClock struct {
	timer *durationTestTimer
	calls int
}

func (c *durationTestClock) NewTimer(time.Duration) SessionDurationTimer {
	c.calls++
	c.timer = &durationTestTimer{ch: make(chan time.Time, 1)}
	return c.timer
}

func (c *durationTestClock) fire() {
	c.timer.ch <- time.Time{}
}

type durationTestTimer struct {
	ch      chan time.Time
	stopped bool
}

func (t *durationTestTimer) C() <-chan time.Time { return t.ch }

func (t *durationTestTimer) Stop() bool {
	t.stopped = true
	return true
}

type durationTestInferencer struct {
	events    []messages.StreamMessage
	connected bool
}

func (i *durationTestInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connected = true
	session := newDurationTestSession()
	for _, event := range i.events {
		if !session.receive.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	return session, nil
}

type durationTestSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func newDurationTestSession() *durationTestSession {
	return &durationTestSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
	}
}

func (s *durationTestSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *durationTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *durationTestSession) Done() <-chan struct{} { return s.done }

func (s *durationTestSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type durationTestWriter struct {
	mu     sync.Mutex
	output bytes.Buffer
	writes chan string
}

func newDurationTestWriter() *durationTestWriter {
	return &durationTestWriter{writes: make(chan string, 16)}
}

func (w *durationTestWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.output.Write(p)
	w.mu.Unlock()
	select {
	case w.writes <- string(p):
	default:
	}
	return n, err
}

func (w *durationTestWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func (w *durationTestWriter) waitFor(t *testing.T, want string) {
	t.Helper()
	for {
		select {
		case got := <-w.writes:
			if strings.Contains(got, want) {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q; output=%q", want, w.String())
		}
	}
}

var _ messages.SessionInferencer = (*durationTestInferencer)(nil)
var _ messages.Session = (*durationTestSession)(nil)
var _ SessionDurationClock = (*durationTestClock)(nil)
var _ SessionDurationTimer = (*durationTestTimer)(nil)
