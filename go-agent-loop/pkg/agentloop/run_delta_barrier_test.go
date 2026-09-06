package agentloop

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestRunJoinsPublishedDeltasBeforeReturningOnEngineError verifies that a
// delta published by the kernel before a later tick error remains readable
// after Run returns. The provider's terminal error is deterministic and follows
// the text delta in the same receive stream, so the test does not depend on
// scheduler ordering or a wall-clock drain.
func TestRunJoinsPublishedDeltasBeforeReturningOnEngineError(t *testing.T) {
	session := newRecordingToolSession()
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("delta-barrier", "test"),
	}) {
		t.Fatal("write session open")
	}
	al, err := New(
		WithMode(engine.DuplexSession),
		WithSessionInferencer(recordingSessionInferencer{session: session}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()

	wantText := "published before command failure"
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue(wantText),
	}) {
		t.Fatal("write provider text delta")
	}
	// Wait until the kernel has actually published the text into the public
	// buffer. This is the barrier under test: the following provider error may
	// stop the engine before another tick can run.
	select {
	case <-waitForDeltaBuffer(al.Deltas(), 1):
	case <-time.After(2 * time.Second):
		t.Fatal("provider delta was not published before terminal error")
	}
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValue("provider failed after publishing text"),
	}) {
		t.Fatal("write provider terminal error")
	}

	select {
	case err := <-runErr:
		var streamErr *engine.StreamDeltaError
		if !errors.As(err, &streamErr) {
			t.Fatalf("Run error = %v, want terminal stream error", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after terminal stream error")
	}

	found := false
	for {
		select {
		case delta := <-al.Deltas().Chan():
			if value, ok := delta.Value.(*messages.TextDeltaValue); ok && value.Content == wantText {
				found = true
			}
		default:
			if !found {
				t.Fatal("published provider delta was lost before Run returned")
			}
			return
		}
	}
}

func TestRunCancellationReleasesBlockedDeltaForwarder(t *testing.T) {
	session := newRecordingToolSession()
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("delta-cancel", "test"),
	}) {
		t.Fatal("write session open")
	}
	al, err := New(
		WithMode(engine.DuplexSession),
		WithBufferCapacity(1),
		WithSessionInferencer(recordingSessionInferencer{session: session}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("first published delta"),
	}) {
		t.Fatal("write first provider text delta")
	}

	select {
	case <-waitForDeltaBuffer(al.Deltas(), 1):
	case <-time.After(2 * time.Second):
		t.Fatal("provider delta was not published before cancellation")
	}
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("second published delta"),
	}) {
		t.Fatal("write second provider text delta")
	}
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValue("provider failed while public delta buffer was full"),
	}) {
		t.Fatal("write provider terminal error")
	}
	// With a one-element public delta buffer and no reader, the forwarding
	// worker blocks on the second event after the engine reports its error.
	// Cancellation must release that write even though Run is already inside
	// its natural-error finish path.
	select {
	case err := <-runErr:
		t.Fatalf("Run returned before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-runErr:
		var streamErr *engine.StreamDeltaError
		if !errors.As(err, &streamErr) {
			t.Fatalf("Run error = %v, want terminal stream error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation with a full delta buffer")
	}
}

func waitForDeltaBuffer(buffer *messages.TypedBuffer[messages.StreamMessage], want int) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for buffer.Len() < want {
			runtime.Gosched()
		}
		close(done)
	}()
	return done
}
