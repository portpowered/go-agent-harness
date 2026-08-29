package openai

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestBuildRealtimeSessionUpdateCanonicalizesToolOrder(t *testing.T) {
	provider := &OpenAIProvider{}
	first, err := provider.buildRealtimeSessionUpdate(models.SessionConfig{
		Tools: []models.ToolDefinition{
			{Name: "zeta", Parameters: []models.ToolParameter{{Name: "z"}, {Name: "a"}}},
			{Name: "alpha"},
		},
	}, "gpt-realtime")
	if err != nil {
		t.Fatalf("build first realtime session.update: %v", err)
	}
	second, err := provider.buildRealtimeSessionUpdate(models.SessionConfig{
		Tools: []models.ToolDefinition{
			{Name: "alpha"},
			{Name: "zeta", Parameters: []models.ToolParameter{{Name: "a"}, {Name: "z"}}},
		},
	}, "gpt-realtime")
	if err != nil {
		t.Fatalf("build second realtime session.update: %v", err)
	}
	if !bytes.Equal(first.Data, second.Data) {
		t.Fatalf("equivalent tool compositions produced different session.update bytes:\nfirst=%s\nsecond=%s", first.Data, second.Data)
	}

	var envelope struct {
		Session struct {
			Tools []struct {
				Name       string `json:"name"`
				Parameters struct {
					Required []string `json:"required"`
				} `json:"parameters"`
			} `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(first.Data, &envelope); err != nil {
		t.Fatalf("decode session.update: %v", err)
	}
	if len(envelope.Session.Tools) != 2 || envelope.Session.Tools[0].Name != "alpha" || envelope.Session.Tools[1].Name != "zeta" {
		t.Fatalf("serialized tool order = %#v, want alpha then zeta", envelope.Session.Tools)
	}
}
