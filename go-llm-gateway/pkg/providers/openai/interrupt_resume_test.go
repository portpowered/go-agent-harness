package openai

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

// wireChatMessage is the part of a Chat Completions request message the
// resume tests read. Content is a string or an array of typed parts.
type wireChatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// text returns the message text from either content form.
func (m wireChatMessage) text(t *testing.T) string {
	t.Helper()
	var plain string
	if json.Unmarshal(m.Content, &plain) == nil {
		return plain
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		t.Fatalf("decode content %s: %v", m.Content, err)
	}
	var out string
	for _, part := range parts {
		out += part.Text
	}
	return out
}

// TestInterruptResumeRequestEndsOnAUserTurn sends the histories an interrupt
// leaves behind and checks the Chat Completions request never ends on an
// assistant message, which compatible APIs routing to Claude reject as a
// prefill.
func TestInterruptResumeRequestEndsOnAUserTurn(t *testing.T) {
	partial := models.NewTextMessage(models.RoleAssistant, resumePartial)
	tests := []struct {
		name     string
		history  []models.Message
		wantRole string
		wantText string
	}{
		{
			name:     "bare interrupt keeps the partial and adds a continuation turn",
			history:  []models.Message{models.NewTextMessage(models.RoleUser, resumePrompt), partial},
			wantRole: requestRoleUser,
			wantText: providers.ContinuationPrompt,
		},
		{
			name:     "interrupt text is the last turn",
			history:  []models.Message{models.NewTextMessage(models.RoleUser, resumePrompt), partial, models.NewTextMessage(models.RoleUser, resumeFollow)},
			wantRole: requestRoleUser,
			wantText: resumeFollow,
		},
		{
			name: "cancelled tool call ends on its result",
			history: []models.Message{
				models.NewTextMessage(models.RoleUser, resumePrompt),
				{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: resumeCallID, Name: testToolGetWeather, Arguments: `{}`}}},
				{Role: models.RoleTool, ToolCallID: resumeCallID, Name: testToolGetWeather, ContentParts: []models.ContentPart{models.TextPart{Text: "cancelled"}}},
			},
			wantRole: "tool",
			wantText: "cancelled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sent := inferAndCaptureChatMessages(t, tt.history)
			last := sent[len(sent)-1]
			if last.Role != tt.wantRole || last.text(t) != tt.wantText {
				t.Fatalf("last message = %s %q, want %s %q", last.Role, last.text(t), tt.wantRole, tt.wantText)
			}
			if tt.wantText == providers.ContinuationPrompt && sent[len(sent)-2].text(t) != resumePartial {
				t.Fatalf("partial response is not kept before the continuation: %+v", sent)
			}
		})
	}
}

func inferAndCaptureChatMessages(t *testing.T, history []models.Message) []wireChatMessage {
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
		Messages []wireChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode request body %s: %v", body, err)
	}
	if len(request.Messages) < 2 {
		t.Fatalf("request has too few messages: %s", body)
	}
	return request.Messages
}
