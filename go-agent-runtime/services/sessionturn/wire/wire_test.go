package wire

import (
	"context"
	"errors"
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

func TestNewServiceReturnsUsablePublicContract(t *testing.T) {
	service := NewService(nil, nil, nil)
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	turns := service.NewTurns(sessionturn.TurnsOptions{})
	if _, err := turns.RunTurn(context.Background(), sessionturn.TurnInput{Text: "hi"}, sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, sessionturn.ErrMissingTurnInferencer) {
		t.Fatalf("turn without provider = %v", err)
	}
	if !strings.HasPrefix(service.NextWirePrompt(), sessionturn.TextSeedWirePrefix) {
		t.Fatal("wire prompt lost its sentinel prefix")
	}
	if publication := service.StartPublication(context.Background(), sessionturn.PublicationRequest{}); publication == nil || publication.Errors() != nil {
		t.Fatal("publication without a watch was not inert")
	}
}

func TestServiceToolExecutorCorrelatesTimeouts(t *testing.T) {
	executor := NewService(nil, nil, nil).NewToolExecutor(sessionturn.ToolExecutorRequest{
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
