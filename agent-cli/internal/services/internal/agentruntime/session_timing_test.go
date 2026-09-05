package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type timestampOnlyClock struct{}

func (timestampOnlyClock) Now() time.Time { return time.Unix(0, 0).UTC() }

func TestSessionTimerUsesElapsedVirtualTimeWithoutLoopTick(t *testing.T) {
	base := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	virtual := platformclock.NewDeterministic(base, time.Second)
	timer, err := newSessionTimer(virtual, 1500*time.Microsecond)
	if err != nil {
		t.Fatal(err)
	}
	defer timer.Stop()
	virtual.AdvanceBy(1499 * time.Microsecond)
	select {
	case <-timer.C():
		t.Fatal("session timer fired before elapsed deadline")
	default:
	}
	if got := virtual.Tick(); got != 0 {
		t.Fatalf("elapsed advance changed loop tick: got %d", got)
	}
	virtual.AdvanceBy(time.Microsecond)
	select {
	case got := <-timer.C():
		if want := base.Add(1500 * time.Microsecond); !got.Equal(want) {
			t.Fatalf("timer timestamp=%v, want %v", got, want)
		}
	default:
		t.Fatal("session timer did not fire at elapsed deadline")
	}
}

func TestSessionTimerRejectsTimestampOnlySource(t *testing.T) {
	if _, err := newSessionTimer(timestampOnlyClock{}, time.Second); !errors.Is(err, platformclock.ErrTimerSourceUnavailable) {
		t.Fatalf("newSessionTimer error=%v, want ErrTimerSourceUnavailable", err)
	}
}

func TestAwaitSessionFirstTurnUsesVirtualTimerAndParentCancellation(t *testing.T) {
	virtual := platformclock.NewDeterministic(time.Unix(0, 0).UTC(), time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ack := make(chan error)
	result := make(chan error, 1)
	go func() { result <- awaitSessionFirstTurnWithClock(ctx, ack, virtual) }()
	virtual.AdvanceBy(sessionFirstTurnAckTimeout - time.Nanosecond)
	select {
	case err := <-result:
		t.Fatalf("ack wait ended early: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ack wait error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ack wait did not observe parent cancellation")
	}
}

func TestEffectiveSessionDurationClockUsesPlanSource(t *testing.T) {
	virtual := platformclock.NewDeterministic(time.Unix(0, 0).UTC(), time.Second)
	clock, err := effectiveSessionDurationClock(sessionRuntimePlan{clockSource: virtual}, realSessionDurationClock{})
	if err != nil {
		t.Fatal(err)
	}
	if clock != virtual {
		t.Fatalf("duration clock=%T, want shared virtual clock", clock)
	}
	custom := &durationTestClock{}
	clock, err = effectiveSessionDurationClock(sessionRuntimePlan{clockSource: virtual}, custom)
	if err != nil {
		t.Fatal(err)
	}
	if clock != custom {
		t.Fatal("explicit test duration clock was replaced")
	}
}
