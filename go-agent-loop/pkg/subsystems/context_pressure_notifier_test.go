package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// fixedTokenCounter returns a fixed token count regardless of input.
type fixedTokenCounter struct {
	count int
}

func (f *fixedTokenCounter) Count(_ []messages.Message) int {
	return f.count
}

// captureWriter records all messages written to it.
type captureWriter struct {
	written []messages.Message
	err     error
}

func (c *captureWriter) Write(_ context.Context, msg messages.Message) error {
	if c.err != nil {
		return c.err
	}
	c.written = append(c.written, msg)
	return nil
}

func (c *captureWriter) lastMessage() (messages.Message, bool) {
	if len(c.written) == 0 {
		return messages.Message{}, false
	}
	return c.written[len(c.written)-1], true
}

func newTestNotifier(t *testing.T, counter TokenCounter, maxTokens int, threshold float64, msg string, w InterruptWriter) *ContextPressureNotifier {
	t.Helper()
	n, err := NewContextPressureNotifier(counter, maxTokens, threshold, msg, w)
	if err != nil {
		t.Fatalf("NewContextPressureNotifier: unexpected error: %v", err)
	}
	return n
}

func emptyLoopState() *state.LoopState {
	return &state.LoopState{}
}

func loopStateWithTokens(tokenCount int) *state.LoopState {
	ls := emptyLoopState()
	// Populate ConversationBuffer with a message — the fixedTokenCounter will
	// return the configured count regardless of actual content.
	ls.History.ConversationBuffer = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "test message"),
	}
	_ = tokenCount // count is controlled by fixedTokenCounter, not buffer length
	return ls
}

// TestContextPressureNotifier_TickGroup verifies the subsystem runs at TickGroup 45.
func TestContextPressureNotifier_TickGroup(t *testing.T) {
	n := newTestNotifier(t, &fixedTokenCounter{}, 1000, 0.8, "", nil)
	if got := n.TickGroup(); got != TickGroupContextPressureNotifier {
		t.Errorf("TickGroup: got %d, want %d", got, TickGroupContextPressureNotifier)
	}
	if TickGroupContextPressureNotifier <= TickGroupTokenCounter {
		t.Errorf("ContextPressureNotifier must run after TokenCounter: %d <= %d",
			TickGroupContextPressureNotifier, TickGroupTokenCounter)
	}
}

// TestContextPressureNotifier_FiresAtThreshold verifies the notification fires
// when token count reaches exactly the threshold.
func TestContextPressureNotifier_FiresAtThreshold(t *testing.T) {
	// 800/1000 = 80% exactly at threshold
	counter := &fixedTokenCounter{count: 800}
	writer := &captureWriter{}
	n := newTestNotifier(t, counter, 1000, 0.8, "", writer)

	ls := loopStateWithTokens(800)
	if err := n.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}

	if len(writer.written) != 1 {
		t.Fatalf("expected 1 interrupt written, got %d", len(writer.written))
	}
}

// TestContextPressureNotifier_FiresAboveThreshold verifies the notification fires
// when token count is above the threshold.
func TestContextPressureNotifier_FiresAboveThreshold(t *testing.T) {
	counter := &fixedTokenCounter{count: 950}
	writer := &captureWriter{}
	n := newTestNotifier(t, counter, 1000, 0.8, "", writer)

	ls := loopStateWithTokens(950)
	if err := n.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}

	if len(writer.written) != 1 {
		t.Fatalf("expected 1 interrupt written, got %d", len(writer.written))
	}
}

// TestContextPressureNotifier_DoesNotFireBelowThreshold verifies no notification
// fires when token count is below the threshold.
func TestContextPressureNotifier_DoesNotFireBelowThreshold(t *testing.T) {
	// 799/1000 = 79.9%, just below 80%
	counter := &fixedTokenCounter{count: 799}
	writer := &captureWriter{}
	n := newTestNotifier(t, counter, 1000, 0.8, "", writer)

	ls := loopStateWithTokens(799)
	if err := n.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}

	if len(writer.written) != 0 {
		t.Errorf("expected no interrupt below threshold, got %d", len(writer.written))
	}
}

// TestContextPressureNotifier_FiresOnlyOnce verifies the notification fires
// at most once even when Execute is called multiple times above threshold.
func TestContextPressureNotifier_FiresOnlyOnce(t *testing.T) {
	counter := &fixedTokenCounter{count: 900}
	writer := &captureWriter{}
	n := newTestNotifier(t, counter, 1000, 0.8, "", writer)

	ls := loopStateWithTokens(900)
	for i := 0; i < 10; i++ {
		if err := n.Execute(context.Background(), ls); err != nil {
			t.Fatalf("Execute iteration %d: unexpected error: %v", i, err)
		}
	}

	if len(writer.written) != 1 {
		t.Errorf("expected exactly 1 interrupt, got %d", len(writer.written))
	}
}

// TestContextPressureNotifier_CustomMessage verifies the custom warning message
// is included in the written interrupt.
func TestContextPressureNotifier_CustomMessage(t *testing.T) {
	const customMsg = "Custom warning: save your work now!"
	counter := &fixedTokenCounter{count: 900}
	writer := &captureWriter{}
	n := newTestNotifier(t, counter, 1000, 0.8, customMsg, writer)

	ls := loopStateWithTokens(900)
	if err := n.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}

	msg, ok := writer.lastMessage()
	if !ok {
		t.Fatal("expected an interrupt message to be written")
	}
	if msg.TextContent() != customMsg {
		t.Errorf("message text: got %q, want %q", msg.TextContent(), customMsg)
	}
}

// TestContextPressureNotifier_DefaultMessage verifies the default warning message
// is used when no custom message is provided.
func TestContextPressureNotifier_DefaultMessage(t *testing.T) {
	counter := &fixedTokenCounter{count: 900}
	writer := &captureWriter{}
	n := newTestNotifier(t, counter, 1000, 0.8, "", writer)

	ls := loopStateWithTokens(900)
	if err := n.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}

	msg, ok := writer.lastMessage()
	if !ok {
		t.Fatal("expected an interrupt message to be written")
	}
	if msg.TextContent() != DefaultContextPressureWarningMessage {
		t.Errorf("message text: got %q, want %q", msg.TextContent(), DefaultContextPressureWarningMessage)
	}
}

// TestContextPressureNotifier_InterruptMessageFormat verifies the interrupt message
// contains a ControlPlanePart of type interrupt.
func TestContextPressureNotifier_InterruptMessageFormat(t *testing.T) {
	counter := &fixedTokenCounter{count: 900}
	writer := &captureWriter{}
	n := newTestNotifier(t, counter, 1000, 0.8, "warning", writer)

	ls := loopStateWithTokens(900)
	if err := n.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}

	msg, ok := writer.lastMessage()
	if !ok {
		t.Fatal("expected an interrupt message to be written")
	}

	// Message must contain a ControlPlanePart with interrupt type.
	hasInterrupt := false
	for _, part := range msg.ContentParts {
		if cp, ok := part.(messages.ControlPlanePart); ok {
			if cp.ControlPlaneMessageType == messages.ControlPlaneMessageTypeInterrupt {
				hasInterrupt = true
			}
		}
	}
	if !hasInterrupt {
		t.Error("expected interrupt message to contain a ControlPlanePart of type interrupt")
	}
}

// TestContextPressureNotifier_FailsWithoutTokenCounter verifies that construction
// fails fast when no TokenCounter is provided.
func TestContextPressureNotifier_FailsWithoutTokenCounter(t *testing.T) {
	_, err := NewContextPressureNotifier(nil, 1000, 0.8, "", nil)
	if err == nil {
		t.Error("expected error when TokenCounter is nil, got nil")
	}
}

// TestContextPressureNotifier_EmptyConversationBuffer verifies no panic or error
// when ConversationBuffer is empty (count will be 0, below threshold).
func TestContextPressureNotifier_EmptyConversationBuffer(t *testing.T) {
	counter := &fixedTokenCounter{count: 0}
	writer := &captureWriter{}
	n := newTestNotifier(t, counter, 1000, 0.8, "", writer)

	ls := emptyLoopState()
	if err := n.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}
	if len(writer.written) != 0 {
		t.Errorf("expected no interrupt for empty buffer, got %d", len(writer.written))
	}
}
