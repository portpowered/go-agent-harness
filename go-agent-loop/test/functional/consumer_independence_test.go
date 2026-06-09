package functional

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type localConsumerInferencer struct {
	response string
	requests []messages.InferenceRequest
}

func (l *localConsumerInferencer) Infer(_ context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	l.requests = append(l.requests, req)
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, l.response),
	}, nil
}

func (l *localConsumerInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	result, err := l.Infer(ctx, req)
	ch := make(chan messages.StreamMessage, 4)
	go func() {
		defer close(ch)
		if err != nil {
			ch <- messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(err.Error())}
			return
		}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(result.Message.TextContent())}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(result.TokenUsage)}
	}()
	return ch, nil
}

func TestConsumerCanUseLoopWithLocalInferencer(t *testing.T) {
	const (
		userText      = "prove loop consumer independence"
		assistantText = "local inferencer response"
	)

	inf := &localConsumerInferencer{response: assistantText}
	loop, err := agentloop.New(agentloop.WithInferencer(inf), agentloop.WithToolExecutionDisabled())
	if err != nil {
		t.Fatalf("new loop: %v", err)
	}

	result, err := loop.Execute(context.Background(), agentloop.NewExecuteInput(userText))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := result.Text(); got != assistantText {
		t.Fatalf("result text: got %q, want %q", got, assistantText)
	}

	history := loop.GetConversationHistory()
	if len(history) != 2 {
		t.Fatalf("history length: got %d, want 2", len(history))
	}
	if history[0].Role != messages.RoleUser || history[0].TextContent() != userText {
		t.Fatalf("history[0]: got role=%q text=%q", history[0].Role, history[0].TextContent())
	}
	if history[1].Role != messages.RoleAssistant || history[1].TextContent() != assistantText {
		t.Fatalf("history[1]: got role=%q text=%q", history[1].Role, history[1].TextContent())
	}
	if len(inf.requests) != 1 {
		t.Fatalf("inference requests: got %d, want 1", len(inf.requests))
	}

	assertNoGatewayProviderDeps(t)
}

func assertNoGatewayProviderDeps(t *testing.T) {
	t.Helper()

	out, err := exec.Command("go", "list", "-test", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("list proof package dependencies: %v\n%s", err, out)
	}

	const forbidden = "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/"
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, forbidden) {
			t.Fatalf("loop consumer proof depends on forbidden provider package %q", dep)
		}
	}
}
