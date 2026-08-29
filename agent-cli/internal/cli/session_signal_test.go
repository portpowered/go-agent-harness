package cli

import (
	"context"
	"testing"
	"time"
)

func TestNewSessionSignalContextParentCancellationDoesNotMarkSIGINT(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, stop, intent := newSessionSignalContext(parent)
	cancelParent()
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not stop session context")
	}
	if intent.SIGINTReceived() {
		t.Fatal("parent cancellation was recorded as SIGINT")
	}
}
