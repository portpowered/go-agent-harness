package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func pageCatalog() webmcp.ToolCatalogSnapshot {
	return webmcp.ToolCatalogSnapshot{
		Generation: 3,
		Tools: []webmcp.ToolDescriptor{
			{
				Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:cube-state"),
				Name:        "get_cube_state",
				Description: "Read the current cube state.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			},
			{
				Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:cube-moves"),
				Name:        "queue_cube_moves",
				Description: "Queue cube rotations.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","description":"Moves to apply.","items":{"type":"string"}}},"required":["moves"],"additionalProperties":false}`),
			},
			{
				Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:page-exec"),
				Name:        "exec",
				Description: "Page-defined exec.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			},
		},
	}
}

func TestPageToolDefinitionsRegisterCatalogFirstClass(t *testing.T) {
	broker := &recordingBroker{catalog: pageCatalog()}
	set := NewBrokerToolSet(broker)
	set.SetReservedToolNames([]string{"exec", "bash"})

	definitions := set.PageToolDefinitions(context.Background())
	byName := map[string]messages.ToolDefinition{}
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	if len(definitions) != 3 {
		t.Fatalf("page definitions = %d (%v), want 3", len(definitions), byName)
	}
	if _, ok := byName["get_cube_state"]; !ok {
		t.Fatalf("get_cube_state missing from %v", byName)
	}
	moves, ok := byName["queue_cube_moves"]
	if !ok {
		t.Fatalf("queue_cube_moves missing from %v", byName)
	}
	if len(moves.Parameters) != 1 || moves.Parameters[0].Name != "moves" || moves.Parameters[0].Type != "array" || !moves.Parameters[0].Required {
		t.Fatalf("queue_cube_moves parameters = %+v, want required array parameter moves", moves.Parameters)
	}
	if !strings.Contains(moves.Parameters[0].Description, `"type":"string"`) {
		t.Fatalf("moves description %q does not summarize the item schema", moves.Parameters[0].Description)
	}
	if !moves.ParametersClosed {
		t.Fatalf("queue_cube_moves should advertise a closed schema")
	}
	if moves.Description != "Queue cube rotations." {
		t.Fatalf("queue_cube_moves description = %q", moves.Description)
	}
	if _, ok := byName["page_exec"]; !ok {
		t.Fatalf("colliding catalog tool was not prefixed: %v", byName)
	}
	if _, ok := byName["exec"]; ok {
		t.Fatalf("catalog tool shadowed the reserved exec name: %v", byName)
	}
}

func TestPageToolDefinitionsRetainCompleteSchema(t *testing.T) {
	schema := richPageToolSchema()
	broker := &recordingBroker{catalog: webmcp.ToolCatalogSnapshot{
		Generation: 9,
		Tools: []webmcp.ToolDescriptor{{
			Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:rich-schema"),
			Name:        "queue_cube_moves",
			Description: "Queue cube rotations.",
			InputSchema: schema,
		}},
	}}
	definitions := NewBrokerToolSet(broker).PageToolDefinitions(context.Background())
	if len(definitions) != 1 {
		t.Fatalf("page definitions = %d, want one definition", len(definitions))
	}
	assertJSONValueEqual(t, definitions[0].ParameterSchema, schema)
}

func TestPageToolExecutionValidatesRichSchemaBeforeDispatch(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-cube", Product: "fixture", Loopback: true}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-cube",
		Type:      "page",
		Title:     "Cube",
		URL:       "https://cube.fixture/",
		Origin:    "https://cube.fixture",
	}
	tool := webmcp.ToolDescriptor{
		Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:rich-schema"),
		Name:        "queue_cube_moves",
		Description: "Queue cube rotations.",
		InputSchema: richPageToolSchema(),
		FrameID:     "frame-cube",
		Origin:      target.Origin,
	}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(target, testkit.WithInitialCatalog(tool), testkit.WithAutoResponse(json.RawMessage(`{"accepted":true}`))),
	))
	defer func() { _ = runtime.Close() }()
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticToolTestDiscoverer{candidate: candidate},
	})
	defer func() { _ = broker.Close() }()
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}); err != nil {
		t.Fatalf("select cube page: %v", err)
	}

	set := NewBrokerToolSet(broker)
	set.SetReservedToolNames([]string{"exec"})
	definitions := set.PageToolDefinitions(context.Background())
	if len(definitions) != 1 {
		t.Fatalf("page definitions = %d, want one definition", len(definitions))
	}
	assertJSONValueEqual(t, definitions[0].ParameterSchema, tool.InputSchema)
	runtime.ResetOperations()

	valid, err := set.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "call-rich-valid",
		Name:      "queue_cube_moves",
		Arguments: `{"moves":[{"face":"R","turns":1}]}`,
	})
	if err != nil {
		t.Fatalf("execute valid rich page tool: %v", err)
	}
	validEnvelope, err := webmcp.UnmarshalToolResult([]byte(valid.Content))
	if err != nil || !validEnvelope.OK {
		t.Fatalf("valid response = %s (err %v), want successful invocation", valid.Content, err)
	}
	if got := countPageToolInvocations(runtime.Operations()); got != 1 {
		t.Fatalf("valid invocation count = %d, want exactly one", got)
	}

	invalid, err := set.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "call-rich-invalid",
		Name:      "queue_cube_moves",
		Arguments: `{"moves":[{"face":"X","turns":0}]}`,
	})
	if err != nil {
		t.Fatalf("execute invalid rich page tool: %v", err)
	}
	invalidEnvelope, err := webmcp.UnmarshalToolResult([]byte(invalid.Content))
	if err != nil || invalidEnvelope.OK || invalidEnvelope.Error == nil || invalidEnvelope.Error.Code != string(webmcp.ErrorInvalidToolInput) {
		t.Fatalf("invalid response = %s (err %v), want invalid_tool_input envelope", invalid.Content, err)
	}
	if got := countPageToolInvocations(runtime.Operations()); got != 1 {
		t.Fatalf("invalid invocation count = %d, want unchanged after validation rejection", got)
	}
}

func TestPageToolExecutionComposesInvokeWithLiveRefResolution(t *testing.T) {
	broker := &recordingBroker{
		catalog:      pageCatalog(),
		invokeResult: webmcp.InvokeResult{InvocationID: "inv-1", State: webmcp.InvocationCompleted, Output: json.RawMessage(`{"queued":2}`)},
	}
	set := NewBrokerToolSet(broker)
	set.SetReservedToolNames([]string{"exec"})
	if defs := set.PageToolDefinitions(context.Background()); len(defs) == 0 {
		t.Fatalf("no page definitions registered")
	}

	response, err := set.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "call-1",
		Name:      "queue_cube_moves",
		Arguments: `{"moves":["R","U'"]}`,
	})
	if err != nil {
		t.Fatalf("execute page tool: %v", err)
	}
	assertTextualResponse(t, response, "call-1", "queue_cube_moves")
	if broker.lastInvoke.ToolRef != "webmcp.tool-ref.v1:cube-moves" {
		t.Fatalf("invoke ref = %q, want the catalog ref for queue_cube_moves", broker.lastInvoke.ToolRef)
	}
	if string(broker.lastInvoke.Input) != `{"moves":["R","U'"]}` {
		t.Fatalf("invoke input = %s, want the call arguments verbatim", broker.lastInvoke.Input)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil || !envelope.OK {
		t.Fatalf("envelope = %s (err %v), want ok invoke result", response.Content, err)
	}

	// The catalog rotates: same name, new generation, new ref. The next call
	// must resolve the fresh ref by name, not reuse a captured one.
	rotated := pageCatalog()
	rotated.Generation = 4
	rotated.Tools[1].Ref = webmcp.ToolRef("webmcp.tool-ref.v1:cube-moves-gen4")
	broker.catalog = rotated
	if _, err := set.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "call-2",
		Name:      "queue_cube_moves",
		Arguments: `{"moves":["F"]}`,
	}); err != nil {
		t.Fatalf("execute after rotation: %v", err)
	}
	if broker.lastInvoke.ToolRef != "webmcp.tool-ref.v1:cube-moves-gen4" {
		t.Fatalf("post-rotation invoke ref = %q, want the generation-4 ref", broker.lastInvoke.ToolRef)
	}
}

func TestPrefixedPageToolRoutesToCatalogName(t *testing.T) {
	broker := &recordingBroker{
		catalog:      pageCatalog(),
		invokeResult: webmcp.InvokeResult{InvocationID: "inv-2", State: webmcp.InvocationCompleted, Output: json.RawMessage(`{}`)},
	}
	set := NewBrokerToolSet(broker)
	set.SetReservedToolNames([]string{"exec"})
	set.PageToolDefinitions(context.Background())

	response, err := set.Executor().Execute(context.Background(), messages.ToolCall{ID: "call-3", Name: "page_exec", Arguments: `{}`})
	if err != nil {
		t.Fatalf("execute prefixed page tool: %v", err)
	}
	assertTextualResponse(t, response, "call-3", "page_exec")
	if broker.lastInvoke.ToolRef != "webmcp.tool-ref.v1:page-exec" {
		t.Fatalf("prefixed invoke ref = %q, want the exec descriptor ref", broker.lastInvoke.ToolRef)
	}
}

func TestComposedSurfaceNeverDeadEndsOnCatalogNames(t *testing.T) {
	broker := &recordingBroker{
		catalog:      pageCatalog(),
		invokeResult: webmcp.InvokeResult{InvocationID: "inv-3", State: webmcp.InvocationCompleted, Output: json.RawMessage(`{"solved":true}`)},
	}
	set := NewBrokerToolSet(broker)
	staticDefinitions := []messages.ToolDefinition{{Name: "exec", Description: "shell"}}
	set.SetReservedToolNames([]string{"exec"})
	surface, err := cliTools.ComposeToolSurface(staticStub{}, staticDefinitions, set.Executor(), set.Definitions())
	if err != nil {
		t.Fatalf("compose surface: %v", err)
	}

	// A live catalog name that was never in the compose-time definition set
	// must route through the dynamic fallback, not die on composition.
	response, err := surface.Executor.Execute(context.Background(), messages.ToolCall{ID: "call-4", Name: "get_cube_state", Arguments: `{}`})
	if err != nil {
		t.Fatalf("catalog-name call errored: %v", err)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil || !envelope.OK {
		t.Fatalf("catalog-name call = %s (err %v), want ok invoke result", response.Content, err)
	}

	// A genuinely unknown name gets model-visible guidance naming the real
	// catalog and the stable path - never invalid tool composition.
	response, err = surface.Executor.Execute(context.Background(), messages.ToolCall{ID: "call-5", Name: "cube_state", Arguments: `{}`})
	if err != nil {
		t.Fatalf("unknown-name call errored: %v", err)
	}
	if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil || envelope.OK || envelope.Error == nil {
		t.Fatalf("unknown-name call = %s, want guidance failure envelope", response.Content)
	}
	if !strings.Contains(envelope.Error.Message, "get_cube_state") || !strings.Contains(envelope.Error.Message, "webmcp_list_tools") {
		t.Fatalf("guidance message %q lacks close matches or the stable path", envelope.Error.Message)
	}
}

func richPageToolSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"object","properties":{"face":{"type":"string","enum":["R","U"]},"turns":{"type":"integer","minimum":1}},"required":["face","turns"],"additionalProperties":false}}},"required":["moves"],"additionalProperties":false}`)
}

func assertJSONValueEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON value = %#v, want %#v", gotValue, wantValue)
	}
}

func countPageToolInvocations(operations []testkit.Operation) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == testkit.OperationInvoke {
			count++
		}
	}
	return count
}

type staticStub struct{}

func (staticStub) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "static"}, nil
}
