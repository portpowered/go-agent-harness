package embedding_test

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

func TestDefaultHostRetainsHistoryAcrossInvocations(t *testing.T) {
	requests := make(chan messages.InferenceRequest, 2)
	model := &hostInferencer{response: "remembered response", requests: requests}
	host := sessionwire.NewService(sessionwire.Dependencies{Inferencer: model, RelaxValidation: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for index, text := range []string{"remember this first turn", "continue the conversation"} {
		_, err := host.Run(ctx, session.Request{
			Input: agentloop.ExecuteInput{Message: text}, ContinueLastSession: index > 0,
		})
		if err != nil {
			t.Fatalf("turn %d: %v", index+1, err)
		}
	}
	readInference(t, ctx, requests)
	second := readInference(t, ctx, requests)
	want := []string{"remember this first turn", "remembered response", "continue the conversation"}
	var got []string
	for _, message := range second.Messages {
		if message.Role == messages.RoleUser || message.Role == messages.RoleAssistant {
			got = append(got, message.TextContent())
		}
	}
	if len(got) != len(want) {
		t.Fatalf("continued history = %q; want %q", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("continued history = %q; want %q", got, want)
		}
	}
}

func readInference(t *testing.T, ctx context.Context, requests <-chan messages.InferenceRequest) messages.InferenceRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-ctx.Done():
		t.Fatal("host returned without the expected inference request")
		return messages.InferenceRequest{}
	}
}
