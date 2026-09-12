package wire

import (
	"context"
	"testing"

	roomcapabilities "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
)

type wireExecutor struct{}

func (wireExecutor) Execute(_ context.Context, call roomcapabilities.ToolCall) (roomcapabilities.ToolCallResponse, error) {
	return roomcapabilities.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: call.Name}, nil
}

func TestNewServiceBuildsFreshPublicService(t *testing.T) {
	service := NewService()
	capability, err := service.Compose(context.Background(), roomcapabilities.Participant{ID: "wire", Tools: []string{"tool"}}, roomcapabilities.ToolCapabilities{
		Executor:    wireExecutor{},
		Definitions: []roomcapabilities.ToolDefinition{{Name: "tool"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(capability.Definitions) != 1 || capability.Definitions[0].Name != "tool" {
		t.Fatalf("wire capability = %#v", capability)
	}
}
