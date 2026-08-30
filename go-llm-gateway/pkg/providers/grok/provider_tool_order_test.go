package grok

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestBuildSessionUpdatePreservesCompletePageSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"object","properties":{"face":{"type":"string","enum":["R","U"]},"turns":{"type":"integer","minimum":1}},"required":["face","turns"],"additionalProperties":false}}},"required":["moves"],"additionalProperties":false}`)
	event, err := buildSessionUpdate(models.SessionConfig{
		Tools: []models.ToolDefinition{{Name: "queue_cube_moves", ParameterSchema: schema}},
	})
	if err != nil {
		t.Fatalf("build session.update: %v", err)
	}
	var envelope struct {
		Session struct {
			Tools []struct {
				Parameters json.RawMessage `json:"parameters"`
			} `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(event.Data, &envelope); err != nil {
		t.Fatalf("decode session.update: %v", err)
	}
	if len(envelope.Session.Tools) != 1 {
		t.Fatalf("serialized tools = %#v, want one page tool", envelope.Session.Tools)
	}
	var got, want any
	if err := json.Unmarshal(envelope.Session.Tools[0].Parameters, &got); err != nil {
		t.Fatalf("decode serialized parameters: %v", err)
	}
	if err := json.Unmarshal(schema, &want); err != nil {
		t.Fatalf("decode expected schema: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serialized Grok schema = %#v, want %#v", got, want)
	}
}

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
