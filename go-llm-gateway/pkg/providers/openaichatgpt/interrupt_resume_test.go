package openaichatgpt_test

import (
	"encoding/json"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt/fakechatgpt"
)

const (
	resumePrompt  = "Tell me a story."
	resumePartial = "Once upon a"
	resumeFollow  = "Make it short."
	resumeCallID  = "call_1"
	resumeTool    = "get_weather"
)

// wireInputItem is the part of a Responses input item the resume tests read.
type wireInputItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Output  string `json:"output"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func (item wireInputItem) text() string {
	if len(item.Content) == 0 {
		return item.Output
	}
	return item.Content[0].Text
}

// TestInterruptResumeRequestEndsOnAUserTurn sends the histories an interrupt
// leaves behind and checks the Responses input never ends on an assistant
// message.
func TestInterruptResumeRequestEndsOnAUserTurn(t *testing.T) {
	partial := models.NewTextMessage(models.RoleAssistant, resumePartial)
	tests := []struct {
		name     string
		history  []models.Message
		wantType string
		wantRole string
		wantText string
	}{
		{
			name:     "bare interrupt keeps the partial and adds a continuation turn",
			history:  []models.Message{models.NewTextMessage(models.RoleUser, resumePrompt), partial},
			wantType: "message",
			wantRole: "user",
			wantText: providers.ContinuationPrompt,
		},
		{
			name:     "interrupt text is the last turn",
			history:  []models.Message{models.NewTextMessage(models.RoleUser, resumePrompt), partial, models.NewTextMessage(models.RoleUser, resumeFollow)},
			wantType: "message",
			wantRole: "user",
			wantText: resumeFollow,
		},
		{
			name: "cancelled tool call ends on its result",
			history: []models.Message{
				models.NewTextMessage(models.RoleUser, resumePrompt),
				{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: resumeCallID, Name: resumeTool, Arguments: `{}`}}},
				{Role: models.RoleTool, ToolCallID: resumeCallID, Name: resumeTool, ContentParts: []models.ContentPart{models.TextPart{Text: "cancelled"}}},
			},
			wantType: "function_call_output",
			wantText: "cancelled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := inferAndCaptureInput(t, tt.history)
			last := input[len(input)-1]
			if last.Type != tt.wantType || last.Role != tt.wantRole || last.text() != tt.wantText {
				t.Fatalf("last input item = %+v, want %s %q %q", last, tt.wantType, tt.wantRole, tt.wantText)
			}
			if tt.wantText == providers.ContinuationPrompt {
				kept := input[len(input)-2]
				if kept.Role != "assistant" || kept.text() != resumePartial {
					t.Fatalf("partial response is not kept before the continuation: %+v", input)
				}
			}
		})
	}
}

func inferAndCaptureInput(t *testing.T, history []models.Message) []wireInputItem {
	t.Helper()
	h := newHarness(t)
	h.fake.Enqueue(fakechatgpt.TextReply("ok"))
	if _, err := h.provider.Infer(t.Context(), providers.InferenceRequest{Messages: history}); err != nil {
		t.Fatalf("Infer: %v", err)
	}
	requests := h.fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	var request struct {
		Input []wireInputItem `json:"input"`
	}
	if err := json.Unmarshal(requests[0].Body, &request); err != nil {
		t.Fatalf("decode request body %s: %v", requests[0].Body, err)
	}
	if len(request.Input) < 2 {
		t.Fatalf("request has too few input items: %s", requests[0].Body)
	}
	return request.Input
}
