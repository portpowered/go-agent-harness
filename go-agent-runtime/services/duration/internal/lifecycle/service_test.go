package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"
	clockpkg "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestRunBoundedAdmitsOutputAndPreservesProviderTerminal(t *testing.T) {
	inner := newTestSession()
	runner := &testRunner{session: inner, started: make(chan struct{})}
	clock := newTestClock()
	capture := &testCapture{seen: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- New(clock).Run(context.Background(), duration.RunRequest{
			MaxDuration: 10 * time.Second,
			Inferencer:  &testInferencer{session: inner},
			Runner:      runner,
			Clock:       clock,
			DrainPeriod: time.Second,
			Handle:      capture.handle,
		})
	}()
	inner.emit(testText("accepted"))
	select {
	case <-capture.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("output was not admitted")
	}
	clock.next(t).fire()
	clock.next(t).fire()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("bounded duration: %v", err)
	}
	if !capture.hasText("accepted") {
		t.Fatalf("accepted output missing: %+v", capture.messages)
	}
	if !capture.hasTerminal(messages.TerminalProvenanceProvider) {
		t.Fatalf("provider terminal missing: %+v", capture.messages)
	}
	if runner.admitted == nil || !messages.SupportsSessionResponseRequests(runner.admitted) {
		t.Fatal("response-request capability was not forwarded")
	}
}

func TestRunRejectsInvalidDurationAndMissingClockBeforeStart(t *testing.T) {
	runner := &testRunner{session: newTestSession()}
	request := duration.RunRequest{Inferencer: &testInferencer{session: runner.session}, Runner: runner}
	err := New(newTestClock()).Run(context.Background(), duration.RunRequest{
		MaxDuration: -time.Millisecond,
		Inferencer:  request.Inferencer,
		Runner:      request.Runner,
	})
	var invalid *duration.InvalidDurationError
	if !errors.As(err, &invalid) || !errors.Is(err, duration.ErrInvalidMaxDuration) || runner.starts != 0 {
		t.Fatalf("invalid duration: err=%v starts=%d", err, runner.starts)
	}
	err = New(nil).Run(context.Background(), requestWithDuration(request, time.Second))
	if !errors.Is(err, duration.ErrClockRequired) || runner.starts != 0 {
		t.Fatalf("missing clock: err=%v starts=%d", err, runner.starts)
	}
}

func TestRunCancellationPreservesIdentityAndStopsRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &testRunner{session: newTestSession(), started: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- New(newTestClock()).Run(ctx, duration.RunRequest{
			MaxDuration: time.Hour,
			Inferencer:  &testInferencer{session: runner.session},
			Runner:      runner,
			Clock:       newTestClock(),
		})
	}()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}
	cancel()
	if err := waitRun(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity: %v", err)
	}
	if runner.stopCount == 0 {
		t.Fatal("runner was not stopped")
	}
}

func TestRunJoinsArtifactFinalizationFailures(t *testing.T) {
	flushErr := errors.New("flush identity")
	closeErr := errors.New("close identity")
	artifacts := &testArtifacts{flushErr: flushErr, closeErr: closeErr}
	inner := newTestSession()
	runner := &testRunner{session: inner}
	done := make(chan error, 1)
	go func() {
		done <- New(nil).Run(context.Background(), duration.RunRequest{
			Inferencer: &testInferencer{session: inner},
			Runner:     runner,
			Artifacts:  artifacts,
		})
	}()
	inner.end(testProviderTerminal())
	err := waitRun(t, done)
	if !errors.Is(err, flushErr) || !errors.Is(err, closeErr) {
		t.Fatalf("artifact failures lost identity: %v", err)
	}
	if artifacts.flushes != 1 || artifacts.closes != 1 {
		t.Fatalf("artifact finalization calls=%d/%d", artifacts.flushes, artifacts.closes)
	}
}

func requestWithDuration(request duration.RunRequest, max time.Duration) duration.RunRequest {
	request.MaxDuration = max
	return request
}

func waitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("duration run did not finish")
		return nil
	}
}

type testClock struct {
	mu      sync.Mutex
	timers  []*testTimer
	created chan *testTimer
}

func newTestClock() *testClock { return &testClock{created: make(chan *testTimer, 8)} }

func (c *testClock) NewTimer(d time.Duration) clockpkg.Timer {
	t := &testTimer{duration: d, fired: make(chan time.Time, 1)}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	c.created <- t
	return t
}

func (c *testClock) next(t *testing.T) *testTimer {
	t.Helper()
	select {
	case timer := <-c.created:
		return timer
	case <-time.After(2 * time.Second):
		t.Fatal("fake clock timer was not created")
		return nil
	}
}

type testTimer struct {
	duration time.Duration
	fired    chan time.Time
	mu       sync.Mutex
	stopped  int
}

func (t *testTimer) C() <-chan time.Time { return t.fired }
func (t *testTimer) Stop() bool {
	t.mu.Lock()
	t.stopped++
	t.mu.Unlock()
	return true
}
func (t *testTimer) fire() { t.fired <- time.Unix(1, 0) }

type testInferencer struct{ session *testSession }

func (i *testInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type testRunner struct {
	session   *testSession
	admitted  messages.Session
	started   chan struct{}
	startOnce sync.Once
	starts    int
	stopCount int
}

func (r *testRunner) Start(ctx context.Context, inferencer messages.SessionInferencer) (duration.Handle, error) {
	r.starts++
	admitted, err := inferencer.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	r.admitted = admitted
	r.startOnce.Do(func() {
		if r.started != nil {
			close(r.started)
		}
	})
	h := &testHandle{session: admitted, deltas: messages.NewTypedBuffer[messages.StreamMessage](64), result: make(chan error, 1), runner: r}
	go h.relay(ctx)
	return h, nil
}

type testHandle struct {
	session messages.Session
	deltas  *messages.TypedBuffer[messages.StreamMessage]
	result  chan error
	runner  *testRunner
	once    sync.Once
	stopErr error
}

func (h *testHandle) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return h.deltas }
func (h *testHandle) Result() <-chan error                                  { return h.result }
func (h *testHandle) SendClose(context.Context) error                       { return h.session.Close() }
func (h *testHandle) Stop(context.Context) error {
	h.once.Do(func() {
		h.runner.stopCount++
		h.stopErr = h.session.Close()
	})
	return h.stopErr
}
func (h *testHandle) relay(ctx context.Context) {
	for {
		select {
		case msg := <-h.session.Receive().Chan():
			h.deltas.Write(ctx, msg)
		case <-h.session.Done():
			for {
				msg, ok := h.session.Receive().Read()
				if !ok {
					break
				}
				h.deltas.Write(ctx, msg)
			}
			h.result <- nil
			return
		}
	}
}

type testSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func newTestSession() *testSession {
	return &testSession{receive: messages.NewTypedBuffer[messages.StreamMessage](64), done: make(chan struct{})}
}
func (s *testSession) Send(context.Context, messages.StreamMessage) bool {
	return true
}
func (s *testSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *testSession) Done() <-chan struct{}                                  { return s.done }
func (s *testSession) Close() error {
	s.end(testProviderTerminal())
	return nil
}
func (s *testSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}
func (s *testSession) SupportsResponseRequests() bool  { return true }
func (s *testSession) emit(msg messages.StreamMessage) { s.receive.Write(context.Background(), msg) }
func (s *testSession) end(msg messages.StreamMessage) {
	s.once.Do(func() {
		s.receive.WriteTerminal(msg)
		close(s.done)
	})
}

type testCapture struct {
	mu       sync.Mutex
	messages []messages.StreamMessage
	seen     chan struct{}
	once     sync.Once
}

func (c *testCapture) handle(_ context.Context, msg messages.StreamMessage, _ duration.MessageState) (duration.MessageResult, error) {
	c.mu.Lock()
	c.messages = append(c.messages, msg)
	c.mu.Unlock()
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value.Content == "accepted" {
		c.once.Do(func() { close(c.seen) })
	}
	return duration.MessageResult{}, nil
}
func (c *testCapture) hasText(want string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, msg := range c.messages {
		if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value.Content == want {
			return true
		}
	}
	return false
}
func (c *testCapture) hasTerminal(want messages.TerminalProvenance) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, msg := range c.messages {
		if value, ok := msg.Value.(*messages.SessionCloseValue); ok && value.TerminalProvenance == want {
			return true
		}
	}
	return false
}

type testArtifacts struct {
	flushErr, closeErr error
	flushes, closes    int
}

func (*testArtifacts) Accept(messages.StreamMessage) error { return nil }
func (a *testArtifacts) Flush() error {
	a.flushes++
	return a.flushErr
}
func (a *testArtifacts) Close() error {
	a.closes++
	return a.closeErr
}

func testText(content string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(content)}
}
func testProviderTerminal() messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"lifecycle", "provider-close", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputPartial,
	)}
}
