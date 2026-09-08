package session

import (
	"testing"
	"time"
)

// newTestStorage returns a Storage rooted at a temp directory.
func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	return NewStorage(t.TempDir())
}

func TestSaveAndLoadTrace(t *testing.T) {
	st := newTestStorage(t)

	trace := TraceRecord{
		TraceID: "trace-001",
		Status:  TraceStatusRunning,
		Config: TraceConfig{
			MaxIterations: 5,
			StopWord:      "COMPLETE",
			Prompt:        "Do the thing.",
		},
		CurrentIteration: 1,
		Iterations:       []IterationTrace{},
	}

	if err := st.SaveTrace(trace); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	loaded, err := st.LoadTrace("trace-001")
	if err != nil {
		t.Fatalf("LoadTrace: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadTrace returned nil for existing trace")
	}
	if loaded.TraceID != trace.TraceID {
		t.Errorf("TraceID: got %q, want %q", loaded.TraceID, trace.TraceID)
	}
	if loaded.Status != TraceStatusRunning {
		t.Errorf("Status: got %q, want %q", loaded.Status, TraceStatusRunning)
	}
	if loaded.Config.MaxIterations != 5 {
		t.Errorf("Config.MaxIterations: got %d, want 5", loaded.Config.MaxIterations)
	}
	if loaded.Config.StopWord != "COMPLETE" {
		t.Errorf("Config.StopWord: got %q, want %q", loaded.Config.StopWord, "COMPLETE")
	}
	if loaded.Config.Prompt != "Do the thing." {
		t.Errorf("Config.Prompt: got %q, want %q", loaded.Config.Prompt, "Do the thing.")
	}
	if loaded.CurrentIteration != 1 {
		t.Errorf("CurrentIteration: got %d, want 1", loaded.CurrentIteration)
	}
}

func TestLoadTrace_NotFound(t *testing.T) {
	st := newTestStorage(t)

	loaded, err := st.LoadTrace("nonexistent")
	if err != nil {
		t.Fatalf("expected nil error for missing trace, got: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil result for missing trace, got: %+v", loaded)
	}
}

func TestUpdateTraceAfterIteration(t *testing.T) {
	st := newTestStorage(t)

	trace := TraceRecord{
		TraceID:          "trace-update",
		Status:           TraceStatusRunning,
		Config:           TraceConfig{MaxIterations: 3},
		CurrentIteration: 0,
		Iterations:       []IterationTrace{},
	}
	if err := st.SaveTrace(trace); err != nil {
		t.Fatalf("initial SaveTrace: %v", err)
	}

	// Simulate completing iteration 1.
	trace.CurrentIteration = 1
	trace.Iterations = append(trace.Iterations, IterationTrace{
		Iteration:          1,
		SessionID:          "session-abc",
		SubAgentSessionIDs: []string{},
		Status:             IterationStatusCompleted,
	})
	if err := st.SaveTrace(trace); err != nil {
		t.Fatalf("SaveTrace after iter 1: %v", err)
	}

	// Simulate completing iteration 2 and marking as completed.
	trace.CurrentIteration = 2
	trace.Status = TraceStatusCompleted
	trace.Iterations = append(trace.Iterations, IterationTrace{
		Iteration:          2,
		SessionID:          "session-def",
		SubAgentSessionIDs: []string{},
		Status:             IterationStatusCompleted,
	})
	if err := st.SaveTrace(trace); err != nil {
		t.Fatalf("SaveTrace after iter 2: %v", err)
	}

	loaded, err := st.LoadTrace("trace-update")
	if err != nil {
		t.Fatalf("LoadTrace: %v", err)
	}
	if loaded.Status != TraceStatusCompleted {
		t.Errorf("Status: got %q, want completed", loaded.Status)
	}
	if loaded.CurrentIteration != 2 {
		t.Errorf("CurrentIteration: got %d, want 2", loaded.CurrentIteration)
	}
	if len(loaded.Iterations) != 2 {
		t.Errorf("Iterations len: got %d, want 2", len(loaded.Iterations))
	}
	if loaded.Iterations[0].SessionID != "session-abc" {
		t.Errorf("Iterations[0].SessionID: got %q", loaded.Iterations[0].SessionID)
	}
}

func TestTraceRecordsSubAgentSessions(t *testing.T) {
	st := newTestStorage(t)

	subIDs := []string{"sub-session-1", "sub-session-2", "sub-session-3"}
	trace := TraceRecord{
		TraceID: "trace-subagent",
		Status:  TraceStatusCompleted,
		Config:  TraceConfig{MaxIterations: 1},
		Iterations: []IterationTrace{
			{
				Iteration:          1,
				SessionID:          "session-parent",
				SubAgentSessionIDs: subIDs,
				Status:             IterationStatusCompleted,
			},
		},
		CurrentIteration: 1,
	}

	if err := st.SaveTrace(trace); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	loaded, err := st.LoadTrace("trace-subagent")
	if err != nil {
		t.Fatalf("LoadTrace: %v", err)
	}
	if len(loaded.Iterations) != 1 {
		t.Fatalf("expected 1 iteration, got %d", len(loaded.Iterations))
	}
	iter := loaded.Iterations[0]
	if iter.SessionID != "session-parent" {
		t.Errorf("SessionID: got %q, want session-parent", iter.SessionID)
	}
	if len(iter.SubAgentSessionIDs) != 3 {
		t.Fatalf("SubAgentSessionIDs len: got %d, want 3", len(iter.SubAgentSessionIDs))
	}
	for i, id := range subIDs {
		if iter.SubAgentSessionIDs[i] != id {
			t.Errorf("SubAgentSessionIDs[%d]: got %q, want %q", i, iter.SubAgentSessionIDs[i], id)
		}
	}
}

func TestListTraces(t *testing.T) {
	st := newTestStorage(t)

	// Save three traces.
	ids := []string{"trace-a", "trace-b", "trace-c"}
	for _, id := range ids {
		trace := TraceRecord{
			TraceID: id,
			Status:  TraceStatusCompleted,
			Config:  TraceConfig{MaxIterations: 1},
		}
		if err := st.SaveTrace(trace); err != nil {
			t.Fatalf("SaveTrace %s: %v", id, err)
		}
		// Small sleep to ensure distinct mod times.
		time.Sleep(2 * time.Millisecond)
	}

	infos, err := st.ListTraces()
	if err != nil {
		t.Fatalf("ListTraces: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("ListTraces: got %d, want 3", len(infos))
	}
	// Most recent first: trace-c should be first.
	if infos[0].ID != "trace-c" {
		t.Errorf("first trace: got %q, want trace-c", infos[0].ID)
	}
	if infos[2].ID != "trace-a" {
		t.Errorf("last trace: got %q, want trace-a", infos[2].ID)
	}
}

func TestListTraces_Empty(t *testing.T) {
	st := newTestStorage(t)

	infos, err := st.ListTraces()
	if err != nil {
		t.Fatalf("ListTraces on empty dir: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected 0 traces, got %d", len(infos))
	}
}

func TestListTraces_DoesNotIncludeSessions(t *testing.T) {
	st := newTestStorage(t)

	// Save a session and a trace.
	if err := st.Save("session-xyz", nil); err != nil {
		t.Fatalf("Save session: %v", err)
	}
	if err := st.SaveTrace(TraceRecord{TraceID: "trace-xyz", Status: TraceStatusRunning}); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	infos, err := st.ListTraces()
	if err != nil {
		t.Fatalf("ListTraces: %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("expected 1 trace (not 2), got %d", len(infos))
	}
	if infos[0].ID != "trace-xyz" {
		t.Errorf("trace ID: got %q, want trace-xyz", infos[0].ID)
	}
}

func TestNewTraceID_Unique(t *testing.T) {
	st := newTestStorage(t)

	id1 := st.NewTraceID()
	time.Sleep(2 * time.Millisecond)
	id2 := st.NewTraceID()
	if id1 == id2 {
		t.Errorf("expected unique IDs, got same: %q", id1)
	}
}
