package room

import (
	"context"
	"errors"
	"testing"
	"time"
)

// assertGatedPairsClosedOnceAndJoinCanceled requires each captured pair to be
// closed exactly once at the Done boundary and the pending Join to observe
// cancellation.
func assertGatedPairsClosedOnceAndJoinCanceled(t *testing.T, connected, pending *gatedClosePair, joinResult <-chan error) {
	t.Helper()
	if got := connected.closeCount.Load(); got != 1 {
		t.Fatalf("connected pair close count at Done boundary = %d, want 1", got)
	}
	if got := pending.closeCount.Load(); got != 1 {
		t.Fatalf("pending pair close count at Done boundary = %d, want 1", got)
	}
	select {
	case err := <-joinResult:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("pending Join error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending Join did not finish after shutdown")
	}
}

// releaseGatedClosesInOrder releases the pair whose Close started first, then
// waits for the other pair's Close to start before releasing it.
func releaseGatedClosesInOrder(t *testing.T, firstClosed, connected, pending *gatedClosePair) {
	t.Helper()
	firstClosed.releaseClose()
	secondClosed := connected
	if firstClosed == connected {
		secondClosed = pending
	}
	awaitClosed(t, secondClosed.closeStarted)
	secondClosed.releaseClose()
}
