package webmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerValidatesNestedPageInputWithoutChangingNumberTokens(t *testing.T) {
	schema := `{
  "type":"object",
  "properties":{
    "profile":{
      "type":"object",
      "properties":{
        "count":{"type":"integer"},
        "ratio":{"type":"number"}
      },
      "required":["count","ratio"],
      "additionalProperties":false
    },
    "tags":{"type":"array","items":{"type":"string"}}
  },
  "required":["profile","tags"],
  "additionalProperties":false
}`
	broker, runtime := newInputValidationBroker(t, schema, 0)
	defer func() {
		if err := broker.Close(); err != nil {
			t.Fatalf("close broker: %v", err)
		}
	}()

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: "browser-a", TargetID: "tab-a"}); err != nil {
		t.Fatalf("select target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(snapshot.Tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", snapshot.Tools)
	}

	runtime.ResetOperations()
	input := json.RawMessage(`{"profile":{"count":90071992547409931234567890,"ratio":1.2345678901234567890123456789},"tags":["nested","unicode-✓"]}`)
	result, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: snapshot.Tools[0].Ref, Input: input})
	if err != nil {
		t.Fatalf("invoke valid nested input: %v", err)
	}
	if result.State != webmcp.InvocationDispatched || result.InvocationID == "" {
		t.Fatalf("invoke result = %#v, want dispatched invocation", result)
	}

	var invokes []testkit.Operation
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationInvoke {
			invokes = append(invokes, operation)
		}
	}
	if len(invokes) != 1 {
		t.Fatalf("invoke operations = %#v, want one", invokes)
	}
	if string(invokes[0].Input) != string(input) || string(invokes[0].Arguments) != string(input) {
		t.Fatalf("dispatched input = (%s, %s), want original bytes %s", invokes[0].Input, invokes[0].Arguments, input)
	}
}

func TestStatefulBrokerRejectsPageInputWithSelectedSchemaAndStableIssues(t *testing.T) {
	schema := `{"type":"object","properties":{"profile":{"type":"object","properties":{"count":{"type":"integer","minimum":1},"mode":{"enum":["fast","safe"]}},"required":["count"],"additionalProperties":false}},"required":["profile"],"additionalProperties":false}`
	broker, runtime := newInputValidationBroker(t, schema, 0)
	defer func() { _ = broker.Close() }()

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: "browser-a", TargetID: "tab-a"}); err != nil {
		t.Fatalf("select target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	ref := snapshot.Tools[0].Ref
	inputCases := []struct {
		name     string
		input    json.RawMessage
		wantPath string
		wantCode string
		noEcho   string
	}{
		{name: "required nested property", input: json.RawMessage(`{"profile":{"mode":"fast"}}`), wantPath: "/profile/count", wantCode: "required"},
		{name: "nested type", input: json.RawMessage(`{"profile":{"count":"not-a-number"}}`), wantPath: "/profile/count", wantCode: "invalid_type"},
		{name: "unknown nested property", input: json.RawMessage(`{"profile":{"count":2,"secret":"do-not-echo"}}`), wantPath: "/profile/secret", wantCode: "unknown_property", noEcho: "do-not-echo"},
		{name: "malformed", input: json.RawMessage(`{"profile":`), wantPath: "/", wantCode: "invalid_json"},
		{name: "non-object root", input: json.RawMessage(`[]`), wantPath: "/", wantCode: "object_required"},
		{name: "multiple values", input: json.RawMessage(`{} {}`), wantPath: "/", wantCode: "multiple_json_values"},
	}

	for _, testCase := range inputCases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime.ResetOperations()
			_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: testCase.input})
			if err == nil {
				t.Fatal("invoke succeeded, want invalid_tool_input")
			}
			var classified *webmcp.ClassifiedError
			if !errors.As(err, &classified) || classified.Code != webmcp.ErrorInvalidToolInput {
				t.Fatalf("error = %v (%T), want %s", err, err, webmcp.ErrorInvalidToolInput)
			}
			if got, ok := classified.Details["tool_ref"].(string); !ok || got != string(ref) {
				t.Fatalf("tool_ref detail = %#v, want %q", classified.Details["tool_ref"], ref)
			}
			if got, ok := classified.Details["input_schema"].(json.RawMessage); !ok || string(got) != string(snapshot.Tools[0].InputSchema) {
				t.Fatalf("input_schema detail = %#v, want complete selected schema %s", classified.Details["input_schema"], snapshot.Tools[0].InputSchema)
			}
			issues, ok := classified.Details["issues"].([]webmcp.ToolResultIssue)
			if !ok {
				t.Fatalf("issues detail = %#v, want typed issues", classified.Details["issues"])
			}
			found := false
			for _, issue := range issues {
				if issue.Path == testCase.wantPath && issue.Code == testCase.wantCode {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues = %#v, want %s/%s", issues, testCase.wantPath, testCase.wantCode)
			}
			if testCase.noEcho != "" {
				encoded, marshalErr := json.Marshal(classified.Details)
				if marshalErr != nil {
					t.Fatalf("marshal safe details: %v", marshalErr)
				}
				if strings.Contains(string(encoded), testCase.noEcho) {
					t.Fatalf("details echoed raw input value %q: %s", testCase.noEcho, encoded)
				}
			}
			assertNoOperation(t, runtime, testkit.OperationInvoke)
		})
	}
}

func TestStatefulBrokerBoundsInvalidUTF8AndOversizedPageInputBeforeDispatch(t *testing.T) {
	schema := `{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":false}`
	broker, runtime := newInputValidationBroker(t, schema, 4)
	defer func() { _ = broker.Close() }()

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: "browser-a", TargetID: "tab-a"}); err != nil {
		t.Fatalf("select target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	for _, testCase := range []struct {
		name  string
		input json.RawMessage
		code  string
	}{
		{name: "oversized", input: json.RawMessage(`{"value":"too large"}`), code: "input_too_large"},
		{name: "invalid utf8", input: json.RawMessage([]byte{'{', 0xff, '}'}), code: "invalid_utf8"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runtime.ResetOperations()
			_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: snapshot.Tools[0].Ref, Input: testCase.input})
			if err == nil {
				t.Fatal("invoke succeeded, want invalid_tool_input")
			}
			var classified *webmcp.ClassifiedError
			if !errors.As(err, &classified) || classified.Code != webmcp.ErrorInvalidToolInput {
				t.Fatalf("error = %v (%T), want %s", err, err, webmcp.ErrorInvalidToolInput)
			}
			issues, ok := classified.Details["issues"].([]webmcp.ToolResultIssue)
			if !ok || len(issues) != 1 || issues[0].Path != "/" || issues[0].Code != testCase.code {
				t.Fatalf("issues = %#v, want /%s", classified.Details["issues"], testCase.code)
			}
			assertNoOperation(t, runtime, testkit.OperationInvoke)
		})
	}
}

func newInputValidationBroker(t *testing.T, schema string, maxInputBytes int) (*webmcp.StatefulBroker, *testkit.ScriptedBrowserRuntime) {
	t.Helper()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page", Title: "Fixture", URL: "https://fixture.test/"},
					testkit.WithInitialCatalog(pageTool("write_state", "frame-1", schema)),
				),
			},
		},
	)
	options := webmcp.BrokerOptions{Runtime: runtime, Discoverer: staticDiscoverer{candidate}}
	if maxInputBytes != 0 {
		options.MaxInputBytes = maxInputBytes
	}
	broker := webmcp.NewBroker(options)
	return broker, runtime
}
