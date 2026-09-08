package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
)

type contractTool struct {
	name    string
	desc    string
	params  map[string]any
	execute func(context.Context, map[string]any) ([]messages.Message, error)
}

func (t *contractTool) Name() string               { return t.name }
func (t *contractTool) Description() string        { return t.desc }
func (t *contractTool) Parameters() map[string]any { return t.params }
func (t *contractTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	if t.execute != nil {
		return t.execute(ctx, args)
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, "ok")}, nil
}

func newContractTool(name string) *contractTool {
	return &contractTool{name: name, desc: "contract test tool", params: map[string]any{"type": "object", "properties": map[string]any{}}}
}

func newEmptyRegistry() *ToolRegistry { return &ToolRegistry{tools: make(map[string]core.Tool)} }

// RunToolConformance is the shared S11 contract for every live registered tool.
func RunToolConformance(t *testing.T, tool core.Tool) {
	t.Helper()
	if strings.TrimSpace(tool.Name()) == "" || strings.TrimSpace(tool.Description()) == "" {
		t.Fatal("tool metadata is empty")
	}
	encoded, err := json.Marshal(core.ToolToSchema(tool))
	var decoded map[string]any
	if err != nil || json.Unmarshal(encoded, &decoded) != nil || len(decoded) == 0 {
		t.Fatal("tool schema did not round-trip through JSON")
	}
	registry := newEmptyRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("initial registration: %v", err)
	}
	t.Run("invalid argument invocation", func(t *testing.T) {
		if reason := unsafeInvocationReason(tool); reason != "" {
			t.Skip(reason)
		}
		for _, probe := range []struct {
			name string
			args map[string]any
		}{
			{name: "nil arguments", args: nil},
			{name: "empty arguments", args: map[string]any{}},
		} {
			t.Run(probe.name, func(t *testing.T) {
				if err := validateInvocationOutcome(func() ([]messages.Message, error) {
					return registry.Execute(context.Background(), tool.Name(), probe.args)
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	wantCount := registry.Count()
	err = registry.Register(tool)
	assertRegistryError(t, err, RegistryErrorDuplicate, `tool "`+tool.Name()+`" is already registered`, ErrDuplicateTool)
	got, ok := registry.Get(tool.Name())
	if !ok || got != tool || registry.Count() != wantCount {
		t.Fatalf("duplicate changed registry: tool=%#v present=%v count=%d", got, ok, registry.Count())
	}
}

func unsafeInvocationReason(tool core.Tool) string {
	if _, ok := tool.(*display.ScreenTool); ok {
		return "existing display.ScreenTool defaults nil/empty action to host screen capture; no in-lease dry-run seam exists, so S11 skips this probe (see PR #57 review)"
	}
	return ""
}

func validateInvocationOutcome(invoke func() ([]messages.Message, error)) error {
	msgs, recovered, err := invokeWithoutPanic(invoke)
	if recovered != nil {
		return fmt.Errorf("tool panicked for invalid arguments: %v", recovered)
	}
	if err != nil {
		var invocationErr *ToolInvocationError
		if !errors.As(err, &invocationErr) || invocationErr.Err == nil || strings.TrimSpace(invocationErr.Error()) == "" || !errors.Is(err, invocationErr.Err) {
			return fmt.Errorf("invalid invocation error %T lacks ToolInvocationError identity", err)
		}
		return nil
	}
	return validateToolMessages(msgs)
}

func invokeWithoutPanic(invoke func() ([]messages.Message, error)) (msgs []messages.Message, recovered any, err error) {
	defer func() {
		if value := recover(); value != nil {
			recovered = value
		}
	}()
	msgs, err = invoke()
	return
}

func validateToolMessages(msgs []messages.Message) error {
	if len(msgs) != 1 {
		return fmt.Errorf("successful invocation returned %d messages; want exactly one", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != messages.RoleTool || len(msg.ContentParts) == 0 || strings.TrimSpace(msg.TextContent()) == "" {
		return fmt.Errorf("tool result must have one tool-role message with non-empty text")
	}
	return nil
}

func TestLiveToolRegistryConformance(t *testing.T) {
	registry := NewToolRegistry()
	names := registry.List()
	if len(names) != registry.Count() {
		t.Fatalf("List count = %d, Count = %d", len(names), registry.Count())
	}
	resolved := make([]core.Tool, 0, len(names))
	for _, name := range names {
		tool, ok := registry.Get(name)
		if !ok || tool == nil {
			t.Fatalf("live registry name %q did not resolve", name)
		}
		resolved = append(resolved, tool)
	}
	if len(resolved) != len(names) || len(resolved) != registry.Count() {
		t.Fatalf("discovered %d names and resolved %d tools for registry count %d", len(names), len(resolved), registry.Count())
	}
	for _, tool := range resolved {
		t.Run(tool.Name(), func(t *testing.T) { RunToolConformance(t, tool) })
	}
}

func assertDeadInvocationRejected(t *testing.T, invoke func() ([]messages.Message, error)) {
	t.Helper()
	if err := validateInvocationOutcome(invoke); err == nil {
		t.Fatal("dead invocation passed the S11 result contract")
	}
}

func TestToolConformanceRejectsDeadInvocation(t *testing.T) {
	params := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}}
	dead := newContractTool("dead")
	dead.params = params
	t.Run("untyped required error", func(t *testing.T) {
		dead.execute = func(context.Context, map[string]any) ([]messages.Message, error) {
			return nil, errors.New("value is required")
		}
		assertDeadInvocationRejected(t, func() ([]messages.Message, error) { return dead.Execute(context.Background(), nil) })
	})
	t.Run("empty content in scratch registry", func(t *testing.T) {
		dead.execute = func(context.Context, map[string]any) ([]messages.Message, error) {
			return []messages.Message{{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.TextPart{}}}}, nil
		}
		registry := newEmptyRegistry()
		if err := registry.Register(dead); err != nil {
			t.Fatal(err)
		}
		assertDeadInvocationRejected(t, func() ([]messages.Message, error) { return registry.Execute(context.Background(), dead.Name(), nil) })
	})
}

func assertRegistryError(t *testing.T, err error, kind RegistryErrorKind, message string, sentinel error) {
	t.Helper()
	var registryErr *RegistryError
	if err == nil || !errors.As(err, &registryErr) || registryErr.Kind != kind || !errors.Is(err, sentinel) || err.Error() != message {
		t.Fatalf("registry error = %T %#v, want kind %q and message %q", err, err, kind, message)
	}
}

func TestRegistryS4Errors(t *testing.T) {
	original := newContractTool("original")
	duplicateRegistry := newEmptyRegistry()
	if err := duplicateRegistry.Register(original); err != nil {
		t.Fatal(err)
	}
	duplicate := newContractTool("original")
	unknownRegistry, emptyRegistry, nilRegistry := newEmptyRegistry(), newEmptyRegistry(), newEmptyRegistry()
	var typedNil *contractTool
	cases := []struct {
		name string
		kind RegistryErrorKind
		msg  string
		sent error
		errs func() []error
	}{
		{"duplicate registration", RegistryErrorDuplicate, `tool "original" is already registered`, ErrDuplicateTool, func() []error { return []error{duplicateRegistry.Register(duplicate)} }},
		{"unknown lookup/execution", RegistryErrorNotFound, `tool "missing" not found`, ErrToolNotFound, func() []error {
			_, lookupErr := unknownRegistry.Lookup("missing")
			_, executeErr := unknownRegistry.Execute(context.Background(), "missing", nil)
			return []error{lookupErr, executeErr}
		}},
		{"empty-name registration", RegistryErrorEmptyName, "tool name must not be empty", ErrEmptyToolName, func() []error { return []error{emptyRegistry.Register(newContractTool(""))} }},
		{"nil-tool registration", RegistryErrorNilTool, "tool must not be nil", ErrNilTool, func() []error { return []error{nilRegistry.Register(nil), nilRegistry.Register(typedNil)} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range tc.errs() {
				assertRegistryError(t, err, tc.kind, tc.msg, tc.sent)
			}
		})
	}
	got, ok := duplicateRegistry.Get("original")
	if !ok || got != original || duplicateRegistry.Count() != 1 {
		t.Fatalf("duplicate changed registry: tool=%#v present=%v count=%d", got, ok, duplicateRegistry.Count())
	}
}

func TestAdapterContracts(t *testing.T) {
	registry, seen := newAdapterRegistry(t)
	executor := NewRegistryExecutor(registry)
	assertAdapterPresentation(t, registry)
	assertAdapterEmptyArguments(t, executor)
	assertAdapterValidArguments(t, executor, seen)
	assertAdapterMalformedArguments(t, executor)
	assertAdapterMixedResponse(t)
	assertAdapterParameterConversion(t)
}

func newAdapterRegistry(t *testing.T) (*ToolRegistry, *map[string]any) {
	t.Helper()
	var seen map[string]any
	tool := &contractTool{name: "adapter", desc: "adapter", params: map[string]any{}, execute: func(_ context.Context, args map[string]any) ([]messages.Message, error) {
		seen = args
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, "ok")}, nil
	}}
	registry := newEmptyRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	return registry, &seen
}

func assertAdapterPresentation(t *testing.T, registry *ToolRegistry) {
	t.Helper()
	msgs, err := registry.Execute(context.Background(), "adapter", nil)
	if err != nil || len(msgs) != 1 || len(registry.GetDefinitions()) != 1 || len(registry.ToAgentLoopDefs()) != 1 || len(registry.GetSummaries()) != 1 {
		t.Fatalf("registry presentation = %#v, %v", msgs, err)
	}
}

func assertAdapterEmptyArguments(t *testing.T, executor *RegistryExecutor) {
	t.Helper()
	resp, err := executor.Execute(context.Background(), messages.ToolCall{ID: "empty", Name: "adapter"})
	if err != nil || resp.ToolCallID != "empty" || resp.Content != "ok" || len(resp.ContentParts) != 1 {
		t.Fatalf("empty arguments response = %#v, %v", resp, err)
	}
}

func assertAdapterValidArguments(t *testing.T, executor *RegistryExecutor, seen *map[string]any) {
	t.Helper()
	resp, err := executor.Execute(context.Background(), messages.ToolCall{ID: "valid", Name: "adapter", Arguments: `{"value":7}`})
	if err != nil || resp.ToolCallID != "valid" || (*seen)["value"] != float64(7) {
		t.Fatalf("valid arguments response = %#v, %v, args=%#v", resp, err, *seen)
	}
}

func assertAdapterMalformedArguments(t *testing.T, executor *RegistryExecutor) {
	t.Helper()
	_, err := executor.Execute(context.Background(), messages.ToolCall{Arguments: "{"})
	var argumentErr *ToolArgumentError
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &argumentErr) || !errors.As(argumentErr.Err, &syntaxErr) || !strings.HasPrefix(err.Error(), "failed to parse tool arguments:") {
		t.Fatalf("malformed arguments error = %T %v", err, err)
	}
}

func assertAdapterMixedResponse(t *testing.T) {
	t.Helper()
	image := messages.ImagePart{Bytes: []byte{1}, MediaType: "image/png"}
	audio := messages.AudioPart{Bytes: []byte{2}, MediaType: "audio/wav"}
	converted := messagesToToolCallResponse("call", []messages.Message{{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("a"), image, messages.NewTextPart("b")}}, {Role: messages.RoleTool, ContentParts: []messages.ContentPart{audio, messages.NewTextPart("c")}}})
	if converted.ToolCallID != "call" || converted.Content != "abc" || len(converted.ContentParts) != 5 {
		t.Fatalf("mixed response = %#v", converted)
	}
	if _, ok := converted.ContentParts[1].(messages.ImagePart); !ok {
		t.Fatalf("mixed response lost image ordering: %#v", converted.ContentParts)
	}
	if _, ok := converted.ContentParts[3].(messages.AudioPart); !ok {
		t.Fatalf("mixed response lost audio ordering: %#v", converted.ContentParts)
	}
}

func assertAdapterParameterConversion(t *testing.T) {
	t.Helper()
	if got := convertParameters(map[string]any{"properties": map[string]any{"a": map[string]any{"type": "string", "description": "A"}, "ignored": "not a property"}, "required": []any{"a"}}); len(got) != 1 || !got[0].Required || got[0].Name != "a" {
		t.Fatalf("converted parameters = %#v", got)
	}
	if got := convertParameters(map[string]any{"required": []string{"a"}}); got != nil {
		t.Fatalf("schema without properties = %#v", got)
	}
}

func TestBaseContracts(t *testing.T) {
	msgs, err := core.ErrorAsToolMessage(nil)
	if err != nil || msgs != nil {
		t.Fatalf("nil error conversion = %#v, %v", msgs, err)
	}
	want := errors.New("bad input")
	msgs, err = core.ErrorAsToolMessage(want)
	if err != nil || len(msgs) != 1 || msgs[0].Role != messages.RoleTool || msgs[0].TextContent() != want.Error() {
		t.Fatalf("error conversion = %#v, %v", msgs, err)
	}
	tool := newContractTool("exact")
	tool.desc = "exact description"
	schema := core.ToolToSchema(tool)
	function, ok := schema["function"].(map[string]any)
	if schema["type"] != "function" || !ok || function["name"] != tool.Name() || function["description"] != tool.Description() || !reflect.DeepEqual(function["parameters"], tool.Parameters()) {
		t.Fatalf("tool schema = %#v", schema)
	}
}

func TestToolResultContracts(t *testing.T) {
	for _, tc := range []struct {
		name, llm, user string
		got             *core.ToolResult
		silent, isError bool
		async           bool
	}{
		{"basic", "basic", "", core.NewToolResult("basic"), false, false, false},
		{"silent", "silent", "", core.SilentResult("silent"), true, false, false},
		{"async", "async", "", core.AsyncResult("async"), false, false, true},
		{"error", "failure", "", core.ErrorResult("failure"), false, true, false},
		{"user", "visible", "visible", core.UserResult("visible"), false, false, false},
	} {
		if tc.got.ForLLM != tc.llm || tc.got.ForUser != tc.user || tc.got.Silent != tc.silent || tc.got.IsError != tc.isError || tc.got.Async != tc.async {
			t.Errorf("%s = %#v", tc.name, tc.got)
		}
	}
	wantErr := errors.New("underlying")
	result := core.UserResult("content").WithError(wantErr)
	if !errors.Is(result.Err, wantErr) || result.ForLLM == "" || result.ForUser == "" {
		t.Fatalf("WithError result = %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "underlying") || strings.Contains(string(encoded), `"Err"`) || !strings.Contains(string(encoded), `"for_llm":"content"`) || !strings.Contains(string(encoded), `"for_user":"content"`) {
		t.Fatalf("core.ToolResult JSON = %s, %v", encoded, err)
	}
}
