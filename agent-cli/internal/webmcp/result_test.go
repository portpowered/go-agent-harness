package webmcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolResultEnvelopeGoldenShapes(t *testing.T) {
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
	wantSuccess := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"output":{"items":[1,2],"value":null}},"error":null}`
	if string(encoded) != wantSuccess {
		t.Fatalf("success = %s, want %s", encoded, wantSuccess)
	}
	if !strings.Contains(string(encoded), `"value":null`) {
		t.Fatalf("success lost nested null: %s", encoded)
	}

	failure := NewToolResultFailure(ToolResultError{
		Code:      string(ErrorInvalidToolInput),
		Message:   "The broker tool input is invalid.",
		Retryable: true,
		Details: map[string]any{
			"tool_ref":     "webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw",
			"input_schema": map[string]any{"type": "object", "additionalProperties": false},
			"issues":       []ToolResultIssue{{Path: "/count", Code: "required"}},
		},
	})
	encoded, err = MarshalToolResult(failure)
	if err != nil {
		t.Fatalf("marshal failure: %v", err)
	}
	wantFailure := `{"version":"webmcp.tool-result.v1","ok":false,"data":null,"error":{"code":"invalid_tool_input","message":"The broker tool input is invalid.","retryable":true,"details":{"input_schema":{"additionalProperties":false,"type":"object"},"issues":[{"path":"/count","code":"required"}],"tool_ref":"webmcp.tool-ref.v1:AAECAwQFBgcICQoLDA0ODw"}}}`
	if string(encoded) != wantFailure {
		t.Fatalf("failure = %s, want %s", encoded, wantFailure)
	}
	if _, err := UnmarshalToolResult(encoded); err != nil {
		t.Fatalf("round trip failure: %v", err)
	}
}

func TestUnmarshalToolResultRejectsUnknownVersionAndFields(t *testing.T) {
	valid := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"value":1},"error":null}`
	for _, raw := range []string{
		strings.Replace(valid, ToolResultVersion, "webmcp.tool-result.v0", 1),
		strings.Replace(valid, `,"error":null`, `,"error":null,"extra":true`, 1),
		strings.Replace(valid, `,"error":null`, `,"error":null,"ok":true`, 1),
	} {
		if _, err := UnmarshalToolResult([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid envelope %s", raw)
		}
	}
}

func TestResultErrorForDoesNotExposeUnknownErrorText(t *testing.T) {
	result := ResultErrorFor(assertionError("credential=secret"), ErrorInvocationFailed, map[string]any{"phase": "invoke"})
	if result.Code != string(ErrorInvocationFailed) || result.Message == "credential=secret" || strings.Contains(result.Message, "secret") {
		t.Fatalf("result error leaked internal text: %#v", result)
	}
}

type assertionError string

func (e assertionError) Error() string { return string(e) }
