package integration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const closeAfterOpenRichCallID = "call_close_after_open_rich"

// closeAfterOpenRichSession is a provider-facing session double that exposes
// the complete-message path used by image-bearing tool results. It emits its
// tool call after the prompt reaches the provider and records the local close
// only through the normal RunSession stream observer.
type closeAfterOpenRichSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}

	responseOnce       sync.Once
	doneOnce           sync.Once
	resultAcceptedOnce sync.Once
	continuationOnce   sync.Once

	resultAccepted      chan struct{}
	continuationRelease chan struct{}
	continuationDone    chan struct{}

	mu       sync.Mutex
	complete []messages.Message
}

func newCloseAfterOpenRichSession() *closeAfterOpenRichSession {
	return &closeAfterOpenRichSession{
		recv:                messages.NewTypedBuffer[messages.StreamMessage](32),
		done:                make(chan struct{}),
		resultAccepted:      make(chan struct{}),
		continuationRelease: make(chan struct{}),
		continuationDone:    make(chan struct{}),
	}
}

func (s *closeAfterOpenRichSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *closeAfterOpenRichSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if err := ctx.Err(); err != nil {
		return closeAfterOpenRichContextOutcome(err)
	}
	if msg.Type == messages.StreamTypeTextDelta {
		s.responseOnce.Do(s.emitToolTurn)
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *closeAfterOpenRichSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	s.complete = append(s.complete, msg)
	s.mu.Unlock()
	s.resultAcceptedOnce.Do(func() {
		close(s.resultAccepted)
		s.continuationOnce.Do(func() { go s.emitContinuation() })
	})
	return true
}

func (s *closeAfterOpenRichSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *closeAfterOpenRichSession) Done() <-chan struct{} { return s.done }

func (s *closeAfterOpenRichSession) Close() error {
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}

func (s *closeAfterOpenRichSession) completeMessages() []messages.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.Message(nil), s.complete...)
}

func (s *closeAfterOpenRichSession) releaseContinuation() {
	close(s.continuationRelease)
}

func (s *closeAfterOpenRichSession) emitContinuation() {
	defer close(s.continuationDone)
	select {
	case <-s.continuationRelease:
	case <-s.done:
		return
	}
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("final grounded continuation")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		if !s.recv.Write(context.Background(), msg) {
			return
		}
	}
}

func (s *closeAfterOpenRichSession) emitToolTurn() {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(closeAfterOpenRichCallID, "read_image")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(closeAfterOpenRichCallID, "read_image", `{"path":"screen.png"}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		_ = s.recv.Write(context.Background(), msg)
	}
}

func closeAfterOpenRichContextOutcome(err error) messages.SessionSendOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

type closeAfterOpenRichInferencer struct {
	session *closeAfterOpenRichSession
}

func (i *closeAfterOpenRichInferencer) ConnectSession(context.Context) (messages.Session, error) {
	_ = i.session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("close-after-open-rich", "test"),
	})
	return i.session, nil
}

type closeAfterOpenRichExecutor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *closeAfterOpenRichExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return messages.ToolCallResponse{
			ToolCallID: call.ID,
			Name:       call.Name,
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "image result"},
				messages.ImagePart{Bytes: []byte("png-bytes"), MediaType: "image/png"},
			},
		}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

// TestCloseAfterOpenWaitsForAcceptedRichToolResult drives the ordinary CLI
// composition boundary with a prompt, which selects CloseAfterOpen. The
// provider response completes while a rich tool is blocked; local close must
// remain absent until the correlated complete message is accepted and its
// terminal continuation is observed.
func TestCloseAfterOpenWaitsForAcceptedRichToolResult(t *testing.T) {
	session := newCloseAfterOpenRichSession()
	executor := &closeAfterOpenRichExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	localClose := make(chan struct{})
	var localCloseOnce sync.Once

	ctx, cancel := context.WithTimeout(context.Background(), sessionLifecycleSafetyTimeout)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		var out bytes.Buffer
		runErr <- services.RunSession(ctx, &out, services.SessionRunOptions{
			RecordPath:        filepath.Join(t.TempDir(), "close-after-open-rich.json"),
			Provider:          "grok",
			Model:             "grok-realtime",
			APIKey:            "test-key",
			Prompt:            "inspect the screen",
			SessionInferencer: &closeAfterOpenRichInferencer{session: session},
			ToolExecutor:      executor,
			StreamObserver: func(msg messages.StreamMessage) {
				if msg.Type == messages.StreamTypeSessionClose {
					localCloseOnce.Do(func() { close(localClose) })
				}
			},
		})
	}()

	waitForCloseAfterOpenSignal(t, executor.started, "rich tool executor to start")
	select {
	case <-localClose:
		t.Fatal("CloseAfterOpen sent SESSION.CLOSE before rich result acceptance")
	default:
	}

	close(executor.release)
	waitForCloseAfterOpenSignal(t, session.resultAccepted, "provider acceptance of rich tool result")
	select {
	case <-localClose:
		t.Fatal("CloseAfterOpen sent SESSION.CLOSE after result acceptance but before continuation")
	default:
	}
	session.releaseContinuation()
	waitForCloseAfterOpenSignal(t, session.continuationDone, "terminal rich continuation")
	waitForCloseAfterOpenSignal(t, localClose, "SESSION.CLOSE after rich result acceptance")

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("CloseAfterOpen session returned an error: %v", err)
		}
	case <-time.After(sessionLifecycleSafetyTimeout):
		t.Fatalf("CloseAfterOpen session did not finish within %s", sessionLifecycleSafetyTimeout)
	}

	complete := session.completeMessages()
	if len(complete) != 1 {
		t.Fatalf("complete-message sends = %d, want exactly one", len(complete))
	}
	if complete[0].Role != messages.RoleTool || complete[0].ToolCallID != closeAfterOpenRichCallID {
		t.Fatalf("complete message identity = %#v, want tool call %s", complete[0], closeAfterOpenRichCallID)
	}
	if got := complete[0].TextContent(); got != "image result" {
		t.Fatalf("complete result text = %q, want image result", got)
	}
}

type closeAfterOpenDurationClock struct {
	mu    sync.Mutex
	timer *closeAfterOpenDurationTimer
}

func (c *closeAfterOpenDurationClock) NewTimer(time.Duration) services.SessionDurationTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer = &closeAfterOpenDurationTimer{ch: make(chan time.Time)}
	return c.timer
}

type closeAfterOpenDurationTimer struct {
	ch      chan time.Time
	mu      sync.Mutex
	stopped bool
}

func (t *closeAfterOpenDurationTimer) C() <-chan time.Time { return t.ch }

func (t *closeAfterOpenDurationTimer) Stop() bool {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	return true
}

func (c *closeAfterOpenDurationClock) stopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer == nil {
		return false
	}
	c.timer.mu.Lock()
	defer c.timer.mu.Unlock()
	return c.timer.stopped
}

// TestDurationAdmissionCloseAfterOpenWaitsForAcceptedRichToolResult drives
// the same replay through RunSessionWithMaxDurationClock. The bounded
// controller must retain the observer and wake its close predicate when the
// rich result crosses the complete-message provider boundary.
func TestDurationAdmissionCloseAfterOpenWaitsForAcceptedRichToolResult(t *testing.T) {
	session := newCloseAfterOpenRichSession()
	executor := &closeAfterOpenRichExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	clock := &closeAfterOpenDurationClock{}
	localClose := make(chan struct{})
	var localCloseOnce sync.Once

	ctx, cancel := context.WithTimeout(context.Background(), sessionLifecycleSafetyTimeout)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- services.RunSessionWithMaxDurationClock(ctx, io.Discard, services.SessionRunOptions{
			RecordPath:        filepath.Join(t.TempDir(), "duration-close-after-open-rich.json"),
			Provider:          "grok",
			Model:             "grok-realtime",
			APIKey:            "test-key",
			Prompt:            "inspect the screen",
			SessionInferencer: &closeAfterOpenRichInferencer{session: session},
			ToolExecutor:      executor,
			StreamObserver: func(msg messages.StreamMessage) {
				if msg.Type == messages.StreamTypeSessionClose {
					localCloseOnce.Do(func() { close(localClose) })
				}
			},
		}, time.Hour, clock)
	}()

	waitForCloseAfterOpenSignal(t, executor.started, "duration rich tool executor to start")
	select {
	case <-localClose:
		t.Fatal("duration CloseAfterOpen sent SESSION.CLOSE before rich result acceptance")
	default:
	}

	close(executor.release)
	waitForCloseAfterOpenSignal(t, session.resultAccepted, "duration provider acceptance of rich tool result")
	select {
	case <-localClose:
		t.Fatal("duration CloseAfterOpen sent SESSION.CLOSE after result acceptance but before continuation")
	default:
	}
	session.releaseContinuation()
	waitForCloseAfterOpenSignal(t, session.continuationDone, "duration terminal rich continuation")
	waitForCloseAfterOpenSignal(t, localClose, "duration SESSION.CLOSE after rich result acceptance")

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("duration CloseAfterOpen session returned an error: %v", err)
		}
	case <-time.After(sessionLifecycleSafetyTimeout):
		t.Fatalf("duration CloseAfterOpen session did not finish within %s", sessionLifecycleSafetyTimeout)
	}
	if !clock.stopped() {
		t.Fatal("duration controller did not stop its timer")
	}
}

func waitForCloseAfterOpenSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(sessionLifecycleSafetyTimeout):
		t.Fatalf("timed out waiting for %s after %s", name, sessionLifecycleSafetyTimeout)
	}
}

var _ messages.SessionInferencer = (*closeAfterOpenRichInferencer)(nil)
var _ messages.ToolExecutor = (*closeAfterOpenRichExecutor)(nil)
