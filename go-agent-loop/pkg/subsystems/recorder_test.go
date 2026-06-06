package subsystems

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/state"
)

// --- mock StateRecorder ---

type mockRecorder struct {
	calls [][]messages.Message
	err   error
}

func (m *mockRecorder) Record(_ context.Context, msgs []messages.Message) error {
	// Store a copy to avoid aliasing issues.
	cp := make([]messages.Message, len(msgs))
	copy(cp, msgs)
	m.calls = append(m.calls, cp)
	return m.err
}

// --- helpers ---

func newRecorderTestState(msgs ...messages.Message) *state.LoopState {
	return &state.LoopState{
		History: state.History{
			ConversationBuffer: msgs,
		},
	}
}

// --- TickGroup ---

func TestRecorder_TickGroup(t *testing.T) {
	r := NewRecorder(nil, 0)
	if r.TickGroup() != TickGroupRecorder {
		t.Errorf("TickGroup: got %d, want %d", r.TickGroup(), TickGroupRecorder)
	}
}

// --- Nil recorder (no-op) ---

func TestRecorder_NilRecorder_NoError(t *testing.T) {
	r := NewRecorder(nil, 0)
	ls := newRecorderTestState(messages.NewTextMessage(messages.RoleUser, "hi"))

	if err := r.Execute(context.Background(), ls); err != nil {
		t.Fatalf("expected no error with nil recorder, got: %v", err)
	}
}

// --- Snapshot creation on tick ---

func TestRecorder_RecordsEveryTick_IntervalZero(t *testing.T) {
	mock := &mockRecorder{}
	r := NewRecorder(mock, 0)
	ls := newRecorderTestState(messages.NewTextMessage(messages.RoleUser, "hello"))

	for i := 0; i < 3; i++ {
		if err := r.Execute(context.Background(), ls); err != nil {
			t.Fatalf("tick %d: unexpected error: %v", i, err)
		}
	}

	if len(mock.calls) != 3 {
		t.Fatalf("expected 3 Record calls, got %d", len(mock.calls))
	}
}

func TestRecorder_RecordsEveryTick_IntervalOne(t *testing.T) {
	mock := &mockRecorder{}
	r := NewRecorder(mock, 1)
	ls := newRecorderTestState(messages.NewTextMessage(messages.RoleUser, "hello"))

	for i := 0; i < 3; i++ {
		if err := r.Execute(context.Background(), ls); err != nil {
			t.Fatalf("tick %d: unexpected error: %v", i, err)
		}
	}

	// interval=1 means every tick (tickCount%1 == 0 always).
	if len(mock.calls) != 3 {
		t.Fatalf("expected 3 Record calls with interval=1, got %d", len(mock.calls))
	}
}

// --- Tick interval skips ---

func TestRecorder_SkipsTicks_IntervalThree(t *testing.T) {
	mock := &mockRecorder{}
	r := NewRecorder(mock, 3)
	ls := newRecorderTestState(messages.NewTextMessage(messages.RoleUser, "hello"))

	// Ticks 1..6; tickCount%3 == 0 at ticks 3 and 6.
	for i := 0; i < 6; i++ {
		if err := r.Execute(context.Background(), ls); err != nil {
			t.Fatalf("tick %d: unexpected error: %v", i, err)
		}
	}

	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 Record calls with interval=3 over 6 ticks, got %d", len(mock.calls))
	}
}

func TestRecorder_NoRecordBeforeInterval(t *testing.T) {
	mock := &mockRecorder{}
	r := NewRecorder(mock, 5)
	ls := newRecorderTestState(messages.NewTextMessage(messages.RoleUser, "hello"))

	// Execute 4 ticks — none should trigger (first trigger is at tick 5).
	for i := 0; i < 4; i++ {
		if err := r.Execute(context.Background(), ls); err != nil {
			t.Fatalf("tick %d: unexpected error: %v", i, err)
		}
	}

	if len(mock.calls) != 0 {
		t.Fatalf("expected 0 Record calls before reaching interval, got %d", len(mock.calls))
	}
}

// --- Snapshot content accuracy ---

func TestRecorder_SnapshotMatchesConversationBuffer(t *testing.T) {
	mock := &mockRecorder{}
	r := NewRecorder(mock, 0)

	msgs := []messages.Message{
		messages.NewTextMessage(messages.RoleSystem, "you are helpful"),
		messages.NewTextMessage(messages.RoleUser, "hello"),
		messages.NewTextMessage(messages.RoleAssistant, "hi there"),
	}
	ls := newRecorderTestState(msgs...)

	if err := r.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 Record call, got %d", len(mock.calls))
	}

	recorded := mock.calls[0]
	if len(recorded) != len(msgs) {
		t.Fatalf("recorded %d messages, want %d", len(recorded), len(msgs))
	}

	for i, m := range msgs {
		if recorded[i].Role != m.Role {
			t.Errorf("message %d: role got %q, want %q", i, recorded[i].Role, m.Role)
		}
	}
}

func TestRecorder_SnapshotReflectsBufferChanges(t *testing.T) {
	mock := &mockRecorder{}
	r := NewRecorder(mock, 0)
	ls := newRecorderTestState(messages.NewTextMessage(messages.RoleUser, "first"))

	if err := r.Execute(context.Background(), ls); err != nil {
		t.Fatalf("tick 1: unexpected error: %v", err)
	}

	// Mutate the conversation buffer before the next tick.
	ls.History.ConversationBuffer = append(ls.History.ConversationBuffer,
		messages.NewTextMessage(messages.RoleAssistant, "response"),
	)

	if err := r.Execute(context.Background(), ls); err != nil {
		t.Fatalf("tick 2: unexpected error: %v", err)
	}

	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 Record calls, got %d", len(mock.calls))
	}
	if len(mock.calls[0]) != 1 {
		t.Errorf("first snapshot: got %d messages, want 1", len(mock.calls[0]))
	}
	if len(mock.calls[1]) != 2 {
		t.Errorf("second snapshot: got %d messages, want 2", len(mock.calls[1]))
	}
}

// --- No snapshot when no changes ---

func TestRecorder_EmptyConversationBuffer(t *testing.T) {
	mock := &mockRecorder{}
	r := NewRecorder(mock, 0)
	ls := newRecorderTestState() // empty buffer

	if err := r.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 Record call even with empty buffer, got %d", len(mock.calls))
	}
	if len(mock.calls[0]) != 0 {
		t.Errorf("expected empty message slice, got %d messages", len(mock.calls[0]))
	}
}

// --- Error propagation ---

func TestRecorder_PropagatesRecorderError(t *testing.T) {
	wantErr := errors.New("storage failure")
	mock := &mockRecorder{err: wantErr}
	r := NewRecorder(mock, 0)
	ls := newRecorderTestState(messages.NewTextMessage(messages.RoleUser, "hello"))

	err := r.Execute(context.Background(), ls)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v, got %v", wantErr, err)
	}
}

// --- Tick counter accumulation across calls ---

func TestRecorder_TickCounterAccumulates(t *testing.T) {
	mock := &mockRecorder{}
	r := NewRecorder(mock, 3)
	ls := newRecorderTestState(messages.NewTextMessage(messages.RoleUser, "hello"))

	// 3 ticks → 1 record (at tick 3)
	for i := 0; i < 3; i++ {
		_ = r.Execute(context.Background(), ls)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("after 3 ticks: expected 1 call, got %d", len(mock.calls))
	}

	// 3 more ticks → 1 more record (at tick 6)
	for i := 0; i < 3; i++ {
		_ = r.Execute(context.Background(), ls)
	}
	if len(mock.calls) != 2 {
		t.Fatalf("after 6 ticks: expected 2 calls, got %d", len(mock.calls))
	}
}
