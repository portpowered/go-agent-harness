package grok

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestBuildSessionUpdateCanonicalizesToolOrder(t *testing.T) {
	first, err := buildSessionUpdate(models.SessionConfig{
		Tools: []models.ToolDefinition{
			{Name: "zeta"},
			{Name: "alpha"},
		},
	})
	if err != nil {
		t.Fatalf("build first session.update: %v", err)
	}
	second, err := buildSessionUpdate(models.SessionConfig{
		Tools: []models.ToolDefinition{
			{Name: "alpha"},
			{Name: "zeta"},
		},
	})
	if err != nil {
		t.Fatalf("build second session.update: %v", err)
	}
	if !bytes.Equal(first.Data, second.Data) {
		t.Fatalf("equivalent tool compositions produced different session.update bytes:\nfirst=%s\nsecond=%s", first.Data, second.Data)
	}

	var envelope struct {
		Session struct {
			Tools []struct {
				Name string `json:"name"`
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
