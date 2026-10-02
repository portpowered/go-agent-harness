package gemini

import (
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestWithModelOverridesTheDefaultModel(t *testing.T) {
	if got := New(WithModel("gemini-test")).model; got != "gemini-test" {
		t.Fatalf("model = %q, want gemini-test", got)
	}
}

func TestStreamChunksToMessageBuildsTheAssistantMessage(t *testing.T) {
	calls := []models.ToolCall{{ID: "call-1"}}
	message := StreamChunksToMessage("hello", calls)
	want := models.Message{Role: models.RoleAssistant, ContentParts: []models.ContentPart{models.TextPart{Text: "hello"}}, ToolCalls: calls}
	if !reflect.DeepEqual(message, want) {
		t.Fatalf("StreamChunksToMessage() = %#v, want %#v", message, want)
	}
	if empty := StreamChunksToMessage("", nil); empty.ContentParts != nil || empty.Role != models.RoleAssistant {
		t.Fatalf("StreamChunksToMessage(empty) = %#v", empty)
	}
}
