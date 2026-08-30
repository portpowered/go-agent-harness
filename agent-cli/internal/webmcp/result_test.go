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

func TestResultErrorForAddsSafeAmbiguityRecoveryAndChoices(t *testing.T) {
	details := map[string]any{
		"browser_id":           "browser-a",
		"candidate_target_ids": []string{"target-b", "target-a", "not/a-public-id"},
		"candidate_choices": []map[string]any{
			{"browser_id": "browser-a", "target_id": "target-b", "title": "Billing", "origin": "https://billing.example.test/invoices?token=secret#fragment", "url": "https://billing.example.test/invoices?token=secret"},
			{"browser_id": "browser-a", "target_id": "target-a", "title": "https://orders.example.test/private", "origin": "https://user:pass@orders.example.test/private"},
		},
	}
	classified := NewClassifiedError(ErrorAmbiguousTab, "multiple browser tabs matched", details)
	result := ResultErrorFor(classified, ErrorTargetAttachFailed, nil)
	if result.Code != string(ErrorAmbiguousTab) || !result.Retryable {
		t.Fatalf("ambiguity result = %#v", result)
	}
	if _, exists := details["recovery"]; exists {
		t.Fatal("error construction mutated caller details")
	}
	ids, ok := result.Details["candidate_target_ids"].([]string)
	if !ok || len(ids) != 2 || ids[0] != "target-a" || ids[1] != "target-b" {
		t.Fatalf("candidate IDs = %#v", result.Details["candidate_target_ids"])
	}
	choices, ok := result.Details["candidate_choices"].([]map[string]any)
	if !ok || len(choices) != 2 || choices[0]["target_id"] != "target-a" || choices[1]["target_id"] != "target-b" {
		t.Fatalf("candidate choices = %#v", result.Details["candidate_choices"])
	}
	if choices[0]["title"] != "redacted" {
		t.Fatalf("unsafe title = %#v", choices[0]["title"])
	}
	if choices[0]["origin"] != nil || choices[1]["origin"] != "https://billing.example.test" {
		t.Fatalf("candidate origins = %#v", choices)
	}
	for _, choice := range choices {
		if _, exists := choice["url"]; exists {
			t.Fatalf("candidate choice exposed URL: %#v", choice)
		}
	}
	recovery, ok := result.Details["recovery"].(map[string]any)
	if !ok || recovery["action"] != "ask_customer" || recovery["retry_after"] != "customer_input" || !strings.Contains(recovery["instruction"].(string), "do not repeat") {
		t.Fatalf("recovery = %#v", result.Details["recovery"])
	}
}

type assertionError string

func (e assertionError) Error() string { return string(e) }
