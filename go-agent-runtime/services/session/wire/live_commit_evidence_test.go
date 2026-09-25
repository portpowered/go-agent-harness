package wire

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// TestLiveAudioCommitEvidenceIsStampedWhenCommitted pins the timestamp of a
// caller audio commit. The provider may take time to accept the commit and may
// answer it before the acknowledgement reaches the caller; a peer sharing the
// clock can advance it meanwhile. The commit evidence must carry the time the
// caller committed, so it never follows the turn completion it caused.
func TestLiveAudioCommitEvidenceIsStampedWhenCommitted(t *testing.T) {
	roomClock := clock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	roomClock.AdvanceTo(9)
	committedAt := roomClock.Now()
	evidence := &commitEvidence{completed: make(chan struct{})}
	provider := newScriptedLiveSession(func(s *scriptedLiveSession, msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeMessageEnd {
			// The shared clock moves on while the provider accepts the
			// commit, and the provider answers before Send returns.
			roomClock.Advance()
			s.emitAssistantText("answer")
		}
	})
	service := NewLiveService(LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return scriptedInferencer{session: provider}, nil
		},
		Clock: roomClock.Now, Scheduler: roomClock, Tick: roomClock.Tick,
		RuntimeObserver: sessiontrace.RuntimeObserverFunc(evidence.observe),
	})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{SessionID: "commit-evidence"})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Logf("close live handle: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), liveRuntimeTestTimeout)
	defer cancel()
	if err := handle.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := handle.Send(ctx, session.LiveControl{Kind: session.LiveControlAudioCommit}); err != nil {
		t.Fatalf("send audio commit: %v", err)
	}
	select {
	case <-evidence.completed:
	case <-ctx.Done():
		t.Fatal("the provider answer was not observed as a completed turn")
	}
	commits, completion := evidence.snapshot()
	if len(commits) != 1 || commits[0].Tick != 9 || !commits[0].Timestamp.Equal(committedAt) {
		t.Fatalf("input commit evidence = %+v, want one commit stamped at tick 9, when it was committed", commits)
	}
	if completion.Tick < commits[0].Tick {
		t.Fatalf("turn completion tick=%d precedes its input commit tick=%d", completion.Tick, commits[0].Tick)
	}
}

// commitEvidence records the input commits and the first turn completion.
type commitEvidence struct {
	completed  chan struct{}
	once       sync.Once
	mu         sync.Mutex
	commits    []sessiontrace.SessionRuntimeObservation
	completion sessiontrace.SessionRuntimeObservation
}

func (e *commitEvidence) observe(observation sessiontrace.SessionRuntimeObservation) {
	if observation.Kind == sessiontrace.SessionRuntimeObservationInputCommit {
		e.mu.Lock()
		e.commits = append(e.commits, observation)
		e.mu.Unlock()
	}
	if observation.Kind == sessiontrace.SessionRuntimeObservationTurnCompleted {
		e.once.Do(func() {
			e.mu.Lock()
			e.completion = observation
			e.mu.Unlock()
			close(e.completed)
		})
	}
}

func (e *commitEvidence) snapshot() ([]sessiontrace.SessionRuntimeObservation, sessiontrace.SessionRuntimeObservation) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sessiontrace.SessionRuntimeObservation(nil), e.commits...), e.completion
}
