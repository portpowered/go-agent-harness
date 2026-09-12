package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestRunJoinsPublishedDeltasBeforeReturningOnEngineError verifies that a
// delta published by the kernel before a later tick error remains readable
// after Run returns. One provider text delta is read from the public buffer
// before the terminal error is queued; a second exact kernel publication then
// runs behind a full public buffer and holds the forwarder inside the join.
func TestRunJoinsPublishedDeltasBeforeReturningOnEngineError(t *testing.T) {
	session := newRecordingToolSession()
	mustWriteDeltaBarrierSession(t, session, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("delta-barrier", "test"),
	}, "write session open")
	logger := newDeltaBarrierLogger()
	al, err := New(
		WithMode(engine.DuplexSession),
		WithKernelBufferCapacity(4),
		WithLogger(logger),
		WithSessionInferencer(recordingSessionInferencer{session: session}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	testCtx := contextWithTestTimeout(t)
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()

	readDeltaBarrierType(t, testCtx, al, messages.StreamTypeSessionOpen, "session open")
	preErrorText := "published before command failure"
	mustWriteDeltaBarrierSession(t, session, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue(preErrorText),
	}, "write pre-error provider text delta")
	readDeltaBarrierText(t, testCtx, al, preErrorText)

	backlog := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("consumer backlog 1")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("consumer backlog 2")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("consumer backlog 3")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("consumer backlog 4")},
	}
	seedDeltaBarrierBacklog(t, al, backlog)

	wantText := "published while terminal error is pending"
	mustWriteDeltaBarrierSession(t, session, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue(wantText),
	}, "write provider text delta")
	// KernelRunner emits this log only after it has sent the exact text delta to
	// its reader channel. This is the causal publication point, not an
	// observation that inspects only the consumer-facing buffer length.
	waitForKernelText(t, testCtx, logger.kernelTextPublished, wantText)
	terminalCause := errors.New("provider failed after publishing text")
	terminalValue := messages.NewErrorValueWithError(terminalCause)
	mustWriteDeltaBarrierSession(t, session, messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: terminalValue,
	}, "write provider terminal error")
	waitForBarrierSignal(t, testCtx, logger.hotLoopError, "engine terminal error")
	// The seeded backlog keeps the forwarder blocked after the engine reports
	// its error. A missing publication join would make Run return here.
	assertRunStillBlocked(t, runErr, "joining the published delta")
	readDeltaBarrierBacklog(t, testCtx, al, backlog)
	runResult := waitForDeltaBarrierRun(t, testCtx, runErr)
	assertDeltaBarrierError(t, runResult, terminalValue, terminalCause)
	if !hasTextDelta(al, wantText) {
		t.Fatal("published provider delta was lost before Run returned")
	}
}

func TestRunCancellationReleasesBlockedDeltaForwarder(t *testing.T) {
	session := newRecordingToolSession()
	mustWriteDeltaBarrierSession(t, session, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("delta-cancel", "test"),
	}, "write session open")
	logger := newDeltaBarrierLogger()
	al, err := New(
		WithMode(engine.DuplexSession),
		WithKernelBufferCapacity(4),
		WithLogger(logger),
		WithSessionInferencer(recordingSessionInferencer{session: session}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	backlog := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("cancel backlog 1")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("cancel backlog 2")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("cancel backlog 3")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("cancel backlog 4")},
	}
	seedDeltaBarrierBacklog(t, al, backlog)

	testCtx := contextWithTestTimeout(t)
	ctx, cancel := context.WithCancel(testCtx)
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()
	mustWriteDeltaBarrierSession(t, session, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("first published delta"),
	}, "write first provider text delta")
	waitForKernelText(t, testCtx, logger.kernelTextPublished, "first published delta")
	mustWriteDeltaBarrierSession(t, session, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("second published delta"),
	}, "write second provider text delta")
	mustWriteDeltaBarrierSession(t, session, messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValue("provider failed while public delta buffer was full"),
	}, "write provider terminal error")
	waitForBarrierSignal(t, testCtx, logger.hotLoopError, "cancellation-test engine terminal error")
	// With a full public delta buffer and no reader, the forwarding worker is
	// blocked by the seeded backlog after the engine reports its error. Caller
	// cancellation must release that write.
	assertRunStillBlocked(t, runErr, "cancellation release")
	cancel()
	select {
	case err := <-runErr:
		var streamErr *engine.StreamDeltaError
		if !errors.As(err, &streamErr) {
			t.Fatalf("Run error = %v, want terminal stream error", err)
		}
	case <-testCtx.Done():
		t.Fatal("Run did not return after cancellation with a full delta buffer")
	}
}

func mustWriteDeltaBarrierSession(t *testing.T, session *recordingToolSession, msg messages.StreamMessage, label string) {
	t.Helper()
	if !session.recv.Write(context.Background(), msg) {
		t.Fatalf("%s: %#v", label, msg)
	}
}

func seedDeltaBarrierBacklog(t *testing.T, al *AgentLoop, backlog []messages.StreamMessage) {
	t.Helper()
	for _, msg := range backlog {
		if !al.Deltas().Write(context.Background(), msg) {
			t.Fatalf("seed public delta backlog with %#v", msg)
		}
	}
}

func readDeltaBarrierBacklog(t *testing.T, ctx context.Context, al *AgentLoop, backlog []messages.StreamMessage) {
	t.Helper()
	for i, want := range backlog {
		got, err := al.Deltas().ReadContext(ctx)
		if err != nil {
			t.Fatalf("read seeded public delta %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("seeded public delta %d = %#v, want %#v", i, got, want)
		}
	}
}

func assertRunStillBlocked(t *testing.T, runErr <-chan error, phase string) {
	t.Helper()
	select {
	case err := <-runErr:
		t.Fatalf("Run returned before %s: %v", phase, err)
	default:
	}
}

func waitForDeltaBarrierRun(t *testing.T, ctx context.Context, runErr <-chan error) error {
	t.Helper()
	select {
	case err := <-runErr:
		return err
	case <-ctx.Done():
		t.Fatalf("Run did not return after terminal stream error")
		return nil
	}
}

func assertDeltaBarrierError(t *testing.T, runErr error, wantValue *messages.ErrorValue, wantCause error) {
	t.Helper()
	var streamErr *engine.StreamDeltaError
	if !errors.As(runErr, &streamErr) {
		t.Fatalf("Run error = %v, want terminal stream error", runErr)
	}
	if streamErr.Value != wantValue {
		t.Fatalf("StreamDeltaError.Value = %#v, want original terminal value %#v", streamErr.Value, wantValue)
	}
	if !errors.Is(runErr, wantCause) {
		t.Fatalf("Run error = %v, want original cause %v", runErr, wantCause)
	}
}

func hasTextDelta(al *AgentLoop, want string) bool {
	for {
		delta, ok := al.Deltas().Read()
		if !ok {
			return false
		}
		if value, ok := delta.Value.(*messages.TextDeltaValue); ok && value.Content == want {
			return true
		}
	}
}

type deltaBarrierLogger struct {
	kernelTextPublished chan string
	hotLoopError        chan struct{}
}

func newDeltaBarrierLogger() *deltaBarrierLogger {
	return &deltaBarrierLogger{
		kernelTextPublished: make(chan string, 16),
		hotLoopError:        make(chan struct{}, 1),
	}
}

func (l *deltaBarrierLogger) Debug(string, ...logging.Field) {}

func (l *deltaBarrierLogger) Info(msg string, fields ...logging.Field) {
	if msg != "KernelRunner: sending text delta" {
		return
	}
	// The kernel log carries the text content after the reader-channel send;
	// retain that identity so a prior text publication cannot satisfy a later
	// wait accidentally.
	for _, field := range fields {
		if field.Key == "delta" {
			if text, ok := field.Value.(string); ok {
				signalKernelText(l.kernelTextPublished, text)
			}
			return
		}
	}
}

func (l *deltaBarrierLogger) Warn(string, ...logging.Field) {}

func (l *deltaBarrierLogger) Error(msg string, _ ...logging.Field) {
	if msg == "agentloop: hot loop exited with error (turn-taking)" {
		signalDeltaBarrier(l.hotLoopError)
	}
}

func (l *deltaBarrierLogger) Fatal(string, ...logging.Field) {}

func (l *deltaBarrierLogger) Panic(string, ...logging.Field) {
	panic("unexpected panic log in delta barrier test")
}

func signalDeltaBarrier(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func signalKernelText(ch chan<- string, text string) {
	ch <- text
}

func waitForKernelText(t *testing.T, ctx context.Context, published <-chan string, want string) {
	t.Helper()
	for {
		select {
		case got := <-published:
			if got == want {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for kernel text publication %q: %v", want, ctx.Err())
		}
	}
}

func readDeltaBarrierType(t *testing.T, ctx context.Context, al *AgentLoop, want messages.StreamMessageType, label string) messages.StreamMessage {
	t.Helper()
	got, err := al.Deltas().ReadContext(ctx)
	if err != nil {
		t.Fatalf("read %s: %v", label, err)
	}
	if got.Type != want {
		t.Fatalf("read %s type = %s, want %s", label, got.Type, want)
	}
	return got
}

func readDeltaBarrierText(t *testing.T, ctx context.Context, al *AgentLoop, want string) {
	t.Helper()
	got := readDeltaBarrierType(t, ctx, al, messages.StreamTypeTextDelta, "pre-error text delta")
	value, ok := got.Value.(*messages.TextDeltaValue)
	if !ok || value.Content != want {
		t.Fatalf("pre-error text delta = %#v, want content %q", got.Value, want)
	}
}

func waitForBarrierSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s: %v", name, ctx.Err())
	}
}

var _ logging.Logger = (*deltaBarrierLogger)(nil)
