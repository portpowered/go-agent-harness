package functional

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

type localGatewayProvider struct {
	captured []providers.InferenceRequest
	response string
}

func (p *localGatewayProvider) Name() string {
	return "local-proof-provider"
}

func (p *localGatewayProvider) Infer(_ context.Context, req providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.captured = append(p.captured, req)
	return providers.InferenceResponse{
		Message: models.NewTextMessage(models.RoleAssistant, p.response),
		Usage:   models.TokenUsage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	}, nil
}

func (p *localGatewayProvider) InferStream(ctx context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	resp, err := p.Infer(ctx, req)
	ch := make(chan messages.StreamMessage, 4)
	go func() {
		defer close(ch)
		if err != nil {
			ch <- messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(err.Error())}
			return
		}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(resp.Message.TextContent())}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(resp.Usage)}
	}()
	return ch, nil
}

func TestGatewayConsumerUsesOnlySharedLoopContract(t *testing.T) {
	const (
		userText      = "prove gateway consumer independence"
		assistantText = "local gateway provider response"
	)

	provider := &localGatewayProvider{response: assistantText}
	gw, err := gateway.NewGateway(gateway.WithProvider(provider))
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	resp, err := gw.Infer(context.Background(), gateway.InferenceRequest{
		Messages: []models.Message{models.NewTextMessage(models.RoleUser, userText)},
		Model:    "local-proof-model",
	})
	if err != nil {
		t.Fatalf("infer: %v", err)
	}
	if got := resp.Message.TextContent(); got != assistantText {
		t.Fatalf("response text: got %q, want %q", got, assistantText)
	}
	if resp.Usage.TotalTokens != 5 {
		t.Fatalf("total tokens: got %d, want 5", resp.Usage.TotalTokens)
	}
	if len(provider.captured) != 1 {
		t.Fatalf("captured requests after infer: got %d, want 1", len(provider.captured))
	}
	if got := provider.captured[0].Messages[0].TextContent(); got != userText {
		t.Fatalf("captured user text: got %q, want %q", got, userText)
	}

	stream, err := gw.InferStream(context.Background(), gateway.InferenceRequest{
		Messages: []models.Message{models.NewTextMessage(models.RoleUser, userText)},
		Model:    "local-proof-model",
	})
	if err != nil {
		t.Fatalf("infer stream: %v", err)
	}
	var streamedText string
	var sawEnd bool
	for msg := range stream {
		switch msg.Type {
		case messages.StreamTypeTextDelta:
			delta, ok := msg.Value.(*messages.TextDeltaValue)
			if !ok {
				t.Fatalf("text delta value: got %T, want *messages.TextDeltaValue", msg.Value)
			}
			streamedText += delta.Content
		case messages.StreamTypeMessageEnd:
			sawEnd = true
		}
	}
	if streamedText != assistantText {
		t.Fatalf("streamed text: got %q, want %q", streamedText, assistantText)
	}
	if !sawEnd {
		t.Fatal("stream did not emit message end")
	}

	assertOnlySharedLoopContractDeps(t)
}

func assertOnlySharedLoopContractDeps(t *testing.T) {
	t.Helper()

	out, err := exec.Command("go", "list", "-test", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("list proof package dependencies: %v\n%s", err, out)
	}

	const (
		loopPkgPrefix       = "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/"
		allowedContractPath = "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	)
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, loopPkgPrefix) && dep != allowedContractPath {
			t.Fatalf("gateway consumer proof depends on forbidden non-contract loop package %q", dep)
		}
	}
}
