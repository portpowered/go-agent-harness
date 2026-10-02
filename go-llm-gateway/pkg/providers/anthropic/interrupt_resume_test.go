package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const (
	resumePrompt  = "Tell me a story."
	resumePartial = "Once upon a"
	resumeFollow  = "Make it short."
	resumeCallID  = "call_1"
)

// wireMessage is the part of an Anthropic request message the resume tests read.
type wireMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// TestInterruptResumeRequestEndsOnAUserTurn sends the histories an interrupt
// leaves behind and checks the request Anthropic receives never ends on an
// assistant message, which Claude 4.6 and later reject as a prefill.
func TestInterruptResumeRequestEndsOnAUserTurn(t *testing.T) {
	partial := models.NewTextMessage(models.RoleAssistant, resumePartial)
	tests := []struct {
		name     string
		history  []models.Message
		wantType string
		wantText string
	}{
		{
			name:     "bare interrupt keeps the partial and adds a continuation turn",
			history:  []models.Message{models.NewTextMessage(models.RoleUser, resumePrompt), partial},
			wantType: "text",
			wantText: providers.ContinuationPrompt,
		},
		{
			name:     "interrupt text is the last turn",
			history:  []models.Message{models.NewTextMessage(models.RoleUser, resumePrompt), partial, models.NewTextMessage(models.RoleUser, resumeFollow)},
			wantType: "text",
			wantText: resumeFollow,
		},
		{
			name: "cancelled tool call ends on its result",
			history: []models.Message{
				models.NewTextMessage(models.RoleUser, resumePrompt),
				{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: resumeCallID, Name: testToolGetWeather, Arguments: `{}`}}},
				{Role: models.RoleTool, ToolCallID: resumeCallID, Name: testToolGetWeather, ContentParts: []models.ContentPart{models.TextPart{Text: "cancelled"}}},
			},
			wantType: "tool_result",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sent := inferAndCaptureMessages(t, tt.history)
			last := sent[len(sent)-1]
			if last.Role != "user" {
				t.Fatalf("last message role = %q, want user; messages %+v", last.Role, sent)
			}
			if len(last.Content) != 1 || last.Content[0].Type != tt.wantType || last.Content[0].Text != tt.wantText {
				t.Fatalf("last message content = %+v, want one %s block %q", last.Content, tt.wantType, tt.wantText)
			}
			if tt.wantText == providers.ContinuationPrompt && sent[len(sent)-2].Content[0].Text != resumePartial {
				t.Fatalf("partial response is not kept before the continuation: %+v", sent)
			}
		})
	}
}

func inferAndCaptureMessages(t *testing.T, history []models.Message) []wireMessage {
	t.Helper()
	response := loadFixture(t, "simple_text.json")
	var body []byte
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(response)),
			Request:    req,
		}, nil
	})
	if _, err := newTestProvider(transport).Infer(context.Background(), providers.InferenceRequest{Messages: history}); err != nil {
		t.Fatalf("Infer: %v", err)
	}
	var request struct {
		Messages []wireMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode request body %s: %v", body, err)
	}
	if len(request.Messages) == 0 {
		t.Fatalf("request has no messages: %s", body)
	}
	return request.Messages
}
