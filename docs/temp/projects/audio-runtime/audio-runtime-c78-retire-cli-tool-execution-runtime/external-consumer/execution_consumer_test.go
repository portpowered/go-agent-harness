package consumer_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	tools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	toolswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

type executor struct {
	mode  atomic.Int32
	calls atomic.Int32
}

func (e *executor) Execute(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls.Add(1)
	switch e.mode.Load() {
	case 1:
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	case 2:
		panic("consumer panic")
	case 3:
		return messages.ToolCallResponse{Content: `{"version":"webmcp.tool-result.v1","ok":false}`}, nil
	default:
		return messages.ToolCallResponse{Content: "consumer success"}, nil
	}
}

type observer struct{ calls, results atomic.Int32 }

func (o *observer) ObserveToolCall(messages.ToolCall) { o.calls.Add(1) }
func (o *observer) ObserveToolResult(messages.ToolCall, messages.ToolCallResponse, bool) {
	o.results.Add(1)
}

func TestExternalConsumerUsesRegisteredToolExecutionService(t *testing.T) {
	executor := &executor{}
	observer := &observer{}
	service := toolswire.NewExecutionService()
	controller := service.NewToolExecutionController(tools.ToolExecutionRequest{
		Executor: executor, TimeoutOverride: 20 * time.Millisecond, Observer: observer,
	})
	response, err := controller.Execute(context.Background(), messages.ToolCall{ID: "success", Name: "lookup"})
	if err != nil || response.ToolCallID != "success" || response.Name != "lookup" || response.Content != "consumer success" {
		t.Fatalf("success response=%+v err=%v", response, err)
	}

	executor.mode.Store(1)
	response, err = controller.Execute(context.Background(), messages.ToolCall{ID: "timeout", Name: "slow"})
	if err != nil || !strings.Contains(response.Content, tools.ToolExecutionTimeoutClassification) {
		t.Fatalf("timeout response=%q err=%v", response.Content, err)
	}

	executor.mode.Store(2)
	response, err = controller.Execute(context.Background(), messages.ToolCall{ID: "panic", Name: "panic_tool"})
	if err != nil || !strings.Contains(response.Content, "consumer panic") && !strings.Contains(response.Content, "tool executor panicked") {
		t.Fatalf("panic response=%q err=%v", response.Content, err)
	}

	executor.mode.Store(3)
	response, err = controller.Execute(context.Background(), messages.ToolCall{ID: "failed-result", Name: "remote"})
	if err != nil || !strings.Contains(response.Content, "webmcp.tool-result.v1") {
		t.Fatalf("failed-result response=%q err=%v", response.Content, err)
	}
	if observer.calls.Load() != 4 || observer.results.Load() != 4 || executor.calls.Load() != 4 {
		t.Fatalf("observer calls=%d results=%d executor calls=%d", observer.calls.Load(), observer.results.Load(), executor.calls.Load())
	}

	parent, cancel := context.WithCancel(context.Background())
	executor.mode.Store(1)
	cancel()
	response, err = controller.Execute(parent, messages.ToolCall{ID: "parent", Name: "slow"})
	if err != nil || !strings.Contains(response.Content, "tool execution canceled") {
		// A host display matcher was not supplied, so the generic cancellation
		// projection remains directly inspectable at this public boundary.
		t.Fatalf("parent cancellation response=%q err=%v", response.Content, err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("ordinary parent cancellation escaped as a Go error")
	}
}
