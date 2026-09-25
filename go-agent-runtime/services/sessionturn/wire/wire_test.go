package wire

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const callTimeout = 10 * time.Millisecond

type executorFunc func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)

func (f executorFunc) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return f(ctx, call)
}

func TestServiceToolExecutorCorrelatesTimeouts(t *testing.T) {
	executor := NewService(nil).NewToolExecutor(sessionturn.ToolExecutorRequest{
		Timeout: callTimeout,
		Inner: executorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
			<-ctx.Done()
			return messages.ToolCallResponse{}, ctx.Err()
		}),
	})
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call", Name: "slow"})
	if err != nil || response.ToolCallID != "call" || !strings.Contains(response.Content, "classification="+sessionturn.ToolTimeoutClassification) {
		t.Fatalf("timeout result = %#v, %v", response, err)
	}
}
