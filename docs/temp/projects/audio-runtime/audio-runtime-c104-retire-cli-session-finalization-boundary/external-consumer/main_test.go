package consumer

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization/wire"
)

type intent bool

func (i intent) SIGINTReceived() bool { return bool(i) }

func TestExternalConsumerUsesOnlyPublicSessionFinalizationContract(t *testing.T) {
	service := wire.NewService()
	if service == nil {
		t.Fatal("wire service is nil")
	}
	primary := errors.New("provider stopped")
	cleanupErr := errors.New("capture flush failed")
	var orderMu sync.Mutex
	var order []string
	finalizer := service.NewFinalizer(sessionfinalization.FinalizerRequest{
		CloseSession: func() error { orderMu.Lock(); order = append(order, "session"); orderMu.Unlock(); return nil },
		FlushCapture: func() error { orderMu.Lock(); order = append(order, "flush"); orderMu.Unlock(); return cleanupErr },
		Finalize: func(context.Context, io.Writer) error {
			orderMu.Lock()
			order = append(order, "finalize")
			orderMu.Unlock()
			return nil
		},
	})
	if err := finalizer.Finish(context.Background(), io.Discard, primary); !errors.Is(err, primary) || !errors.Is(err, cleanupErr) {
		t.Fatalf("joined result = %v", err)
	}
	if want := []string{"session", "flush", "finalize"}; len(order) != len(want) || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("cleanup order = %v, want %v", order, want)
	}

	termination := service.NewTerminationBoundary(context.Background(), sessionfinalization.TerminationRequest{
		WaitForStragglers: func(policy sessionfinalization.DrainPolicy) error {
			if policy.QuietPeriod <= 0 || policy.WallSafety <= policy.QuietPeriod {
				t.Fatalf("unbounded drain policy = %#v", policy)
			}
			return nil
		},
	})
	if err := termination.Terminate(nil); err != nil {
		t.Fatalf("external termination = %v", err)
	}

	if service.SIGINTErrorOnly(errors.Join(context.Canceled, sessionfinalization.ErrFinalizationPanic), sessionfinalization.ErrorTreeOptions{CancellationErrors: []error{context.Canceled, sessionfinalization.ErrFinalizationPanic}}) != true {
		t.Fatal("custom public cancellation set rejected")
	}
	if service.SIGINTCancellationOnly(context.Canceled, intent(true), sessionfinalization.ErrorTreeOptions{}) != true {
		t.Fatal("public SIGINT cancellation rejected")
	}
}
