package browser

import (
	"encoding/json"
	"testing"
)

func TestNormalizeBrowserParameterSchemaFlattensAlternatives(t *testing.T) {
	raw := json.RawMessage(`{"description":"update","anyOf":[{"type":"object","properties":{"document_id":{"type":"string"},"content":{"type":"string"}},"required":["document_id","content"]},{"type":"object","properties":{"selector":{"type":"string"},"content":{"type":"string"}},"required":["selector","content"]}]}`)
	normalized, reason, ok := NormalizeBrowserParameterSchema(raw)
	if !ok {
		t.Fatalf("normalize failed: %s", reason)
	}
	var object map[string]any
	if err := json.Unmarshal(normalized, &object); err != nil {
		t.Fatalf("normalized schema is invalid JSON: %v", err)
	}
	if object["type"] != pageJSONSchemaObjectType {
		t.Fatalf("normalized type = %#v", object["type"])
	}
	if _, exists := object["anyOf"]; exists {
		t.Fatalf("normalized schema retained anyOf: %s", normalized)
	}
	required, ok := object["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "content" {
		t.Fatalf("normalized required = %#v", object["required"])
	}
}

func TestNormalizeBrowserParameterSchemaRejectsScalar(t *testing.T) {
	if _, reason, ok := NormalizeBrowserParameterSchema(json.RawMessage(`{"type":"string"}`)); ok || reason == "" {
		t.Fatalf("scalar schema accepted: reason=%q", reason)
	}
}
