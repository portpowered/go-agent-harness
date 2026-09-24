package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

type cancelableTool struct {
	started chan struct{}
	active  atomic.Int32
	calls   atomic.Int32
}

func (t *cancelableTool) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	t.active.Add(1)
	defer t.active.Add(-1)
	t.calls.Add(1)
	close(t.started)
	<-ctx.Done()
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, ctx.Err()
}

func TestToolExecutorCloseCancelsAndJoinsActiveInvocation(t *testing.T) {
	inner := &cancelableTool{started: make(chan struct{})}
	executor := newToolExecutor(inner, nil, time.Hour, nil, nil, nil, nil)
	closer, ok := executor.(interface{ Close() error })
	if !ok {
		t.Fatal("tool executor does not implement Close")
	}
	callDone := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), messages.ToolCall{ID: "held", Name: "slow"})
		callDone <- err
	}()
	select {
	case <-inner.started:
	case <-time.After(time.Second):
		t.Fatal("tool invocation did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- closer.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after the invocation finished")
	}
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool execution did not settle")
	}
	if active := inner.active.Load(); active != 0 {
		t.Fatalf("active tool callbacks after Close = %d, want 0", active)
	}
	if calls := inner.calls.Load(); calls != 1 {
		t.Fatalf("tool callback invocations = %d, want 1", calls)
	}
}

func TestPublicationStopCancelsAndJoinsRefreshWithoutLatePublish(t *testing.T) {
	refreshStarted := make(chan struct{})
	var refreshActive atomic.Int32
	var publishCalls atomic.Int32
	publication, err := startPublication(context.Background(), sessionturn.PublicationRequest{
		Browser: sessionturn.BrowserRequest{
			Watch: func(ctx context.Context, _ func(sessionturn.BrowserEvent) bool) error {
				<-ctx.Done()
				return nil
			},
			Refresh: func(ctx context.Context) ([]messages.ToolDefinition, error) {
				refreshActive.Add(1)
				defer refreshActive.Add(-1)
				close(refreshStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		Publish: func(context.Context, []messages.ToolDefinition) error {
			publishCalls.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start publication: %v", err)
	}
	publication.MarkReady()
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("initial refresh did not start")
	}

	stopDone := make(chan struct{})
	go func() {
		publication.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after the refresh callback finished")
	}
	if calls := publishCalls.Load(); calls != 0 {
		t.Fatalf("publication calls after Stop = %d, want 0", calls)
	}
	if active := refreshActive.Load(); active != 0 {
		t.Fatalf("active refresh callbacks after Stop = %d, want 0", active)
	}
	if err := publication.State().Err; !errors.Is(err, context.Canceled) {
		t.Fatalf("publication terminal error = %v, want cancellation", err)
	}
}
