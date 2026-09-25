package cli

import (
	"encoding/json"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type queryScopeCase struct {
	name   string
	tool   webmcp.ToolDescriptor
	input  string
	output string
}

// TestWebMCPQueryToolScopeUsesFreshnessGuard keeps the Margin read surface in
// one behavioral matrix. Every read-only page descriptor takes both caller
// paths and must preserve the same non-empty page payload; this is deliberately
// based on the shared invocation/result contract rather than source topology.
func TestWebMCPQueryToolScopeUsesFreshnessGuard(t *testing.T) {
	for _, testCase := range webmcpQueryScopeCases() {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newQueryParityFixtureWithTool(t, testCase.tool)
			live := fixture.runLiveQueryWithInput(t, "live-"+testCase.name, testCase.input, testCase.output)
			direct := fixture.runDirectQueryWithInput(t, "direct-"+testCase.name, testCase.input, testCase.output)

			liveOutput := decodeLiveQueryOutput(t, live, fixture.ref)
			directOutput := decodeDirectQueryOutput(t, direct, fixture.ref)
			assertNonEmptyScopePayload(t, testCase.name, liveOutput, []byte(testCase.output))
			assertNonEmptyScopePayload(t, testCase.name, directOutput, []byte(testCase.output))
			if !jsonEqual(liveOutput, directOutput) {
				t.Fatalf("live/direct decoded %s payloads differ: live=%s direct=%s", testCase.name, liveOutput, directOutput)
			}
			fixture.assertUnchangedSelection(t)
			fixture.assertOneTerminalPerInvocation(t, 2)
		})
	}
}

func webmcpQueryScopeCases() []queryScopeCase {
	return []queryScopeCase{
		{
			name: "get_document",
			tool: queryScopeReadTool(
				"get_document",
				"Read one document from the current Margin page.",
				`{"type":"object","properties":{"document_id":{"type":"string"}},"required":["document_id"],"additionalProperties":false}`,
			),
			input:  `{"document_id":"welcome-to-margin"}`,
			output: `{"id":"welcome-to-margin","title":"Welcome to Margin","content":"A welcome document for the Margin fixture."}`,
		},
		{
			name: "list_documents",
			tool: queryScopeReadTool(
				"list_documents",
				"List documents in the current Margin page.",
				`{"type":"object","properties":{},"additionalProperties":false}`,
			),
			input:  `{}`,
			output: `{"count":1,"documents":[{"id":"welcome-to-margin","title":"Welcome to Margin"}]}`,
		},
		{
			name: "list_comments",
			tool: queryScopeReadTool(
				"list_comments",
				"List comments for a document on the current Margin page.",
				`{"type":"object","properties":{"document_id":{"type":"string"}},"required":["document_id"],"additionalProperties":false}`,
			),
			input:  `{"document_id":"welcome-to-margin"}`,
			output: `{"count":1,"comments":[{"id":"comment-1","document_id":"welcome-to-margin","body":"Looks good."}]}`,
		},
	}
}

func queryScopeReadTool(name, description, inputSchema string) webmcp.ToolDescriptor {
	readOnly := true
	return webmcp.ToolDescriptor{
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(inputSchema),
		Annotations: webmcp.ToolAnnotations{ReadOnly: &readOnly},
		FrameID:     queryParityFrame,
	}
}

func assertNonEmptyScopePayload(t *testing.T, toolName string, got, want []byte) {
	t.Helper()
	if !json.Valid(got) || jsonEqual(got, []byte("null")) || jsonEqual(got, []byte(`{"count":0,"documents":[]}`)) {
		t.Fatalf("%s returned empty or invalid decoded payload: %s", toolName, got)
	}
	if !jsonEqual(got, want) {
		t.Fatalf("%s decoded payload = %s, want %s", toolName, got, want)
	}
}
