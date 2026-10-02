package gemini

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

// wireContent is the part of a Gemini request content the resume tests read.
type wireContent struct {
	Role  string `json:"role"`
	Parts []struct {
		Text             string          `json:"text"`
		FunctionResponse json.RawMessage `json:"functionResponse"`
	} `json:"parts"`
}

// TestInterruptResumeRequestEndsOnAUserTurn sends the histories an interrupt
// leaves behind and checks the request Gemini receives never ends on a model
// turn, which would ask Gemini to continue its own partial response.
func TestInterruptResumeRequestEndsOnAUserTurn(t *testing.T) {
	partial := models.NewTextMessage(models.RoleAssistant, resumePartial)
	tests := []struct {
		name         string
		history      []models.Message
		wantText     string
		wantFunction bool
	}{
		{
			name:     "bare interrupt keeps the partial and adds a continuation turn",
			history:  []models.Message{models.NewTextMessage(models.RoleUser, resumePrompt), partial},
			wantText: providers.ContinuationPrompt,
		},
		{
			name:     "interrupt text is the last turn",
			history:  []models.Message{models.NewTextMessage(models.RoleUser, resumePrompt), partial, models.NewTextMessage(models.RoleUser, resumeFollow)},
			wantText: resumeFollow,
		},
		{
			name: "cancelled tool call ends on its result",
			history: []models.Message{
				models.NewTextMessage(models.RoleUser, resumePrompt),
				{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: resumeCallID, Name: testToolGetWeather, Arguments: `{}`}}},
				{Role: models.RoleTool, ToolCallID: resumeCallID, Name: testToolGetWeather, ContentParts: []models.ContentPart{models.TextPart{Text: "cancelled"}}},
			},
			wantFunction: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sent := inferAndCaptureContents(t, tt.history)
			last := sent[len(sent)-1]
			if last.Role != "user" {
				t.Fatalf("last content role = %q, want user; contents %+v", last.Role, sent)
			}
			if len(last.Parts) != 1 || last.Parts[0].Text != tt.wantText || (len(last.Parts[0].FunctionResponse) > 0) != tt.wantFunction {
				t.Fatalf("last content parts = %+v, want text %q (function response %v)", last.Parts, tt.wantText, tt.wantFunction)
			}
			if tt.wantText == providers.ContinuationPrompt && sent[len(sent)-2].Parts[0].Text != resumePartial {
				t.Fatalf("partial response is not kept before the continuation: %+v", sent)
			}
		})
	}
}

func inferAndCaptureContents(t *testing.T, history []models.Message) []wireContent {
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
		Contents []wireContent `json:"contents"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode request body %s: %v", body, err)
	}
	if len(request.Contents) == 0 {
		t.Fatalf("request has no contents: %s", body)
	}
	return request.Contents
}
