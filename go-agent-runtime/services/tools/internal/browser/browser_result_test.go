package browser

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserResultEnvelopePreservesNestedNulls(t *testing.T) {
	success, err := NewToolResultSuccess(struct {
		Output json.RawMessage `json:"output"`
	}{Output: json.RawMessage(`{"items":[1,2],"value":null}`)})
	if err != nil {
		t.Fatalf("success envelope: %v", err)
	}
	encoded, err := MarshalToolResult(success)
	if err != nil {
		t.Fatalf("marshal success: %v", err)
	}
	want := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"output":{"items":[1,2],"value":null}},"error":null}`
	if string(encoded) != want {
		t.Fatalf("success = %s, want %s", encoded, want)
	}
}

func TestBrowserResultEnvelopeRejectsUnknownAndNullFields(t *testing.T) {
	valid := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"value":1},"error":null}`
	invalid := []string{
		strings.Replace(valid, ToolResultVersion, "webmcp.tool-result.v0", 1),
		strings.Replace(valid, `,"error":null`, `,"error":null,"extra":true`, 1),
		`{"version":"webmcp.tool-result.v1","ok":false,"data":null,"error":{"code":"no_eligible_tab","message":"bad","retryable":null,"details":{}}}`,
	}
	for _, raw := range invalid {
		if _, err := UnmarshalToolResult([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid envelope %s", raw)
		}
	}
}
