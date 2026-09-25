package clock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeterministicContextParentCancellationClosesDoneImmediately(t *testing.T) {
	clock := NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	parent, cancelParent := context.WithCancelCause(context.Background())
	ctx, cancelChild := clock.WithTimeout(parent, time.Hour)
	defer cancelChild()
	waitForDeterministicTimers(t, clock, 1)

	parentCause := errors.New("parent stopped")
	cancelParent(parentCause)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("parent cancellation did not close the child synchronously")
	}
	if !errors.Is(ctx.Err(), parentCause) || !errors.Is(context.Cause(ctx), parentCause) {
		t.Fatalf("parent cause: err=%v cause=%v, want %v", ctx.Err(), context.Cause(ctx), parentCause)
	}
	waitForDeterministicTimerCount(t, clock, 0)
}

func TestDeterministicContextParentCancellationReportsCauseBeforeDone(t *testing.T) {
	clock := NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	parent, cancelParent := context.WithCancelCause(context.Background())
	ctx, cancelChild := clock.WithTimeout(parent, time.Hour)
	defer cancelChild()
	waitForDeterministicTimers(t, clock, 1)

	parentCause := errors.New("parent stopped before done was observed")
	cancelParent(parentCause)
	if err := ctx.Err(); !errors.Is(err, parentCause) {
		t.Fatalf("parent cancellation error before Done: got %v, want %v", err, parentCause)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, parentCause) {
		t.Fatalf("parent cancellation cause before Done: got %v, want %v", cause, parentCause)
	}
}

func waitForDeterministicTimerCount(t *testing.T, clock *Deterministic, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWaitBound)
	defer cancel()
	if err := clock.waitForPendingTimers(ctx, func(pending int) bool { return pending == count }); err != nil {
		t.Fatalf("deterministic timer count did not settle at %d: %v", count, err)
	}
}
