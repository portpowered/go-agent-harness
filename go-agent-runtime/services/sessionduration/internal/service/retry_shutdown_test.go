package service

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestRunStopsCleanlyWhenDoneClosesDuringRetryDelay(t *testing.T) {
	scheduler := &triggerScheduler{created: make(chan *triggerTimer, 1)}
	loop := &retryRunLoopProbe{
		deltas: messages.NewTypedBuffer[messages.StreamMessage](1),
		sent:   make(chan messages.StreamMessage, 1),
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- New().Run(sessionduration.RunRequest{
			Context:    context.Background(),
			Inferencer: contractInferencer{session: newContractSession()},
			Clock:      scheduler,
			Retry:      sessionduration.RetryPolicy{Enabled: true, MaxRetries: 1, DefaultDelay: time.Second},
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return loop, nil
			},
			Done: done,
		})
	}()

	select {
	case <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("retry scheduler was not created")
	}
	close(done)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run after clean completion signal = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after its completion signal")
	}
	select {
	case msg := <-loop.sent:
		t.Fatalf("Run dispatched a retry after its completion signal: %+v", msg)
	default:
	}
}
