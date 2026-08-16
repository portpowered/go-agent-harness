package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type contractTool struct {
	name    string
	desc    string
	params  map[string]any
	execute func(context.Context, map[string]any) ([]messages.Message, error)
}

func (t *contractTool) Name() string { return t.name }

func (t *contractTool) Description() string { return t.desc }

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

func newEmptyRegistry() *ToolRegistry { return &ToolRegistry{tools: make(map[string]Tool)} }

// RunToolConformance is the shared S11 contract for every live registered tool.
func RunToolConformance(t *testing.T, tool Tool) {
	t.Helper()
	if strings.TrimSpace(tool.Name()) == "" || strings.TrimSpace(tool.Description()) == "" {
		t.Fatal("tool metadata is empty")
	}
	schemaBytes, err := json.Marshal(ToolToSchema(tool))
	var schema map[string]any
	if err != nil || json.Unmarshal(schemaBytes, &schema) != nil || len(schema) == 0 {
		t.Fatal("tool schema did not round-trip through JSON")
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
				if err := validateInvocationOutcome(tool, probe.args); err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	registry := newEmptyRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("initial registration: %v", err)
	}
	wantCount := registry.Count()
	err = registry.Register(tool)
	assertRegistryError(t, err, RegistryErrorDuplicate, `tool "`+tool.Name()+`" is already registered`, ErrDuplicateTool)
	got, ok := registry.Get(tool.Name())
	if !ok || got != tool || registry.Count() != wantCount {
		t.Fatalf("duplicate changed registry: tool=%#v present=%v count=%d", got, ok, registry.Count())
	}

	// Malformed JSON must fail in the adapter before the live tool is invoked,
	// and must retain an identifiable typed error for callers to inspect.
	_, err = NewRegistryExecutor(registry).Execute(context.Background(), messages.ToolCall{
		ID: "s11-invalid-json", Name: tool.Name(), Arguments: "{",
	})
	var argumentErr *ToolArgumentError
	if !errors.As(err, &argumentErr) || argumentErr.Err == nil {
		t.Fatalf("malformed arguments error = %T %v; want ToolArgumentError with cause", err, err)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(argumentErr.Err, &syntaxErr) {
		t.Fatalf("malformed arguments cause = %T %v; want json.SyntaxError", argumentErr.Err, argumentErr.Err)
	}
}

func unsafeInvocationReason(tool Tool) string {
	if _, ok := tool.(*ScreenTool); ok {
		return "existing ScreenTool behavior probes the display and defaults nil/empty action to a screenshot; " +
			"there is no in-lease dry-run seam, so S11 skips this invocation case (see PR #57 blocking review comment)"
	}
	return ""
}

func validateInvocationOutcome(tool Tool, args map[string]any) error {
	msgs, err, recovered := invokeWithoutPanic(tool, args)
	if recovered != nil {
		return fmt.Errorf("tool panicked for invalid arguments: %v", recovered)
	}
	if err != nil {
		if !identifiesRequiredArgument(tool, err.Error()) {
			return fmt.Errorf("invalid invocation error %q does not identify a required argument", err)
		}
		return nil
	}
	if err := validateToolMessages(msgs); err != nil {
		return fmt.Errorf("invalid invocation returned an invalid result: %w", err)
	}
	return nil
}

func invokeWithoutPanic(tool Tool, args map[string]any) (msgs []messages.Message, err error, recovered any) {
	defer func() {
		if value := recover(); value != nil {
			recovered = value
		}
	}()
	msgs, err = tool.Execute(context.Background(), args)
	return msgs, err, nil
}

func identifiesRequiredArgument(tool Tool, message string) bool {
	if !strings.Contains(strings.ToLower(message), "required") {
		return false
	}
	for _, name := range requiredParameterNames(tool.Parameters()) {
		if strings.Contains(strings.ToLower(message), strings.ToLower(name)) {
			return true
		}
	}
	return false
}

func requiredParameterNames(schema map[string]any) []string {
	if required, ok := schema["required"].([]string); ok {
		return required
	}
	return nil
}

func validateToolMessages(msgs []messages.Message) error {
	if len(msgs) != 1 {
		return fmt.Errorf("successful invocation returned %d messages; want exactly one", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != messages.RoleTool {
		return fmt.Errorf("message role = %q; want tool", msg.Role)
	}
	if len(msg.ContentParts) == 0 || strings.TrimSpace(msg.TextContent()) == "" {
		return errors.New("tool result has no non-empty text content")
	}
	return nil
}

func TestLiveToolRegistryConformance(t *testing.T) {
	registry := NewToolRegistry()
	names := registry.List()
	if len(names) != registry.Count() {
		t.Fatalf("List count = %d, Count = %d", len(names), registry.Count())
	}
	resolved := make([]Tool, 0, len(names))
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

func TestToolConformanceRejectsDeadInvocation(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"value": map[string]any{"type": "string"}},
		"required":   []string{"value"},
	}
	for _, tc := range []struct {
		name    string
		execute func(context.Context, map[string]any) ([]messages.Message, error)
	}{
		{
			name: "non-specific error",
			execute: func(context.Context, map[string]any) ([]messages.Message, error) {
				return nil, errors.New("implementation is unavailable")
			},
		},
		{
			name: "empty content",
			execute: func(context.Context, map[string]any) ([]messages.Message, error) {
				return []messages.Message{{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.TextPart{}}}}, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dead := newContractTool("dead")
			dead.params = params
			dead.execute = tc.execute
			if err := validateInvocationOutcome(dead, nil); err == nil {
				t.Fatal("dead invocation passed the S11 result contract")
			}
		})
	}
}

func assertRegistryError(t *testing.T, err error, kind RegistryErrorKind, message string, sentinel error) {
	t.Helper()
	var registryErr *RegistryError
	if err == nil || !errors.As(err, &registryErr) {
		t.Fatalf("error %T is not a RegistryError: %v", err, err)
	}
	if registryErr.Kind != kind || !errors.Is(err, sentinel) || err.Error() != message {
		t.Fatalf("registry error = kind %q, message %q", registryErr.Kind, err)
	}
}

func TestRegistryS4Errors(t *testing.T) {
	original := newContractTool("original")
	duplicateRegistry := newEmptyRegistry()
	if err := duplicateRegistry.Register(original); err != nil {
		t.Fatal(err)
	}
	duplicate := newContractTool("original")
	unknownRegistry := newEmptyRegistry()
	emptyRegistry := newEmptyRegistry()
	nilRegistry := newEmptyRegistry()
	var typedNil *contractTool

	cases := []struct {
		name     string
		wantKind RegistryErrorKind
		message  string
		sentinel error
		errors   func() []error
	}{
		{"duplicate registration", RegistryErrorDuplicate, `tool "original" is already registered`, ErrDuplicateTool, func() []error { return []error{duplicateRegistry.Register(duplicate)} }},
		{
			name: "unknown lookup/execution", wantKind: RegistryErrorNotFound, message: `tool "missing" not found`, sentinel: ErrToolNotFound,
			errors: func() []error {
				_, lookupErr := unknownRegistry.Lookup("missing")
				_, executeErr := unknownRegistry.Execute(context.Background(), "missing", nil)
				return []error{lookupErr, executeErr}
			},
		},
		{"empty-name registration", RegistryErrorEmptyName, "tool name must not be empty", ErrEmptyToolName, func() []error { return []error{emptyRegistry.Register(newContractTool(""))} }},
		{"nil-tool registration", RegistryErrorNilTool, "tool must not be nil", ErrNilTool, func() []error { return []error{nilRegistry.Register(nil), nilRegistry.Register(typedNil)} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range tc.errors() {
				assertRegistryError(t, err, tc.wantKind, tc.message, tc.sentinel)
			}
		})
	}
	got, ok := duplicateRegistry.Get("original")
	if !ok || got != original || duplicateRegistry.Count() != 1 {
		t.Fatalf("duplicate changed registry: tool=%#v present=%v count=%d", got, ok, duplicateRegistry.Count())
	}
}

func TestAdapterContracts(t *testing.T) {
	var seen map[string]any
	tool := &contractTool{name: "adapter", desc: "adapter", params: map[string]any{}, execute: func(_ context.Context, args map[string]any) ([]messages.Message, error) {
		seen = args
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, "ok")}, nil
	}}
	registry := newEmptyRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	if msgs, err := registry.Execute(context.Background(), "adapter", nil); err != nil || len(msgs) != 1 || len(registry.GetDefinitions()) != 1 || len(registry.ToAgentLoopDefs()) != 1 || len(registry.GetSummaries()) != 1 {
		t.Fatalf("registry presentation = %#v, %v", msgs, err)
	}
	executor := NewRegistryExecutor(registry)
	resp, err := executor.Execute(context.Background(), messages.ToolCall{ID: "empty", Name: "adapter"})
	if err != nil || resp.ToolCallID != "empty" || resp.Content != "ok" || len(resp.ContentParts) != 1 {
		t.Fatalf("empty arguments response = %#v, %v", resp, err)
	}
	resp, err = executor.Execute(context.Background(), messages.ToolCall{ID: "valid", Name: "adapter", Arguments: `{"value":7}`})
	if err != nil || resp.ToolCallID != "valid" || seen["value"] != float64(7) {
		t.Fatalf("valid arguments response = %#v, %v, args=%#v", resp, err, seen)
	}
	_, err = executor.Execute(context.Background(), messages.ToolCall{Arguments: "{"})
	var argumentErr *ToolArgumentError
	if !errors.As(err, &argumentErr) || !strings.HasPrefix(err.Error(), "failed to parse tool arguments:") {
		t.Fatalf("malformed arguments error = %T %v", err, err)
	}

	image := messages.ImagePart{Bytes: []byte{1}, MediaType: "image/png"}
	audio := messages.AudioPart{Bytes: []byte{2}, MediaType: "audio/wav"}
	converted := messagesToToolCallResponse("call", []messages.Message{{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("a"), image, messages.NewTextPart("b")}}, {Role: messages.RoleTool, ContentParts: []messages.ContentPart{audio, messages.NewTextPart("c")}}})
	if converted.ToolCallID != "call" || converted.Content != "abc" || len(converted.ContentParts) != 5 {
		t.Fatalf("mixed response = %#v", converted)
	}
	if _, ok := converted.ContentParts[1].(messages.ImagePart); !ok {
		t.Fatalf("mixed response lost image ordering: %#v", converted.ContentParts)
	}
	if got := convertParameters(map[string]any{"properties": map[string]any{
		"a": map[string]any{"type": "string", "description": "A"}, "ignored": "not a property",
	}, "required": []any{"a"}}); len(got) != 1 || !got[0].Required || got[0].Name != "a" {
		t.Fatalf("converted parameters = %#v", got)
	}
	if got := convertParameters(map[string]any{"required": []string{"a"}}); got != nil {
		t.Fatalf("schema without properties = %#v", got)
	}
}

func TestBaseContracts(t *testing.T) {
	msgs, err := ErrorAsToolMessage(nil)
	if err != nil || msgs != nil {
		t.Fatalf("nil error conversion = %#v, %v", msgs, err)
	}
	want := errors.New("bad input")
	msgs, err = ErrorAsToolMessage(want)
	if err != nil || len(msgs) != 1 || msgs[0].Role != messages.RoleTool || msgs[0].TextContent() != want.Error() {
		t.Fatalf("error conversion = %#v, %v", msgs, err)
	}
	tool := newContractTool("exact")
	tool.desc = "exact description"
	schema := ToolToSchema(tool)
	function, ok := schema["function"].(map[string]any)
	if schema["type"] != "function" || !ok || function["name"] != tool.Name() || function["description"] != tool.Description() || !reflect.DeepEqual(function["parameters"], tool.Parameters()) {
		t.Fatalf("tool schema = %#v", schema)
	}
}

func TestToolResultContracts(t *testing.T) {
	for _, tc := range []struct {
		name, llm, user string
		got             *ToolResult
		silent, isError bool
		async           bool
	}{
		{"basic", "basic", "", NewToolResult("basic"), false, false, false},
		{"silent", "silent", "", SilentResult("silent"), true, false, false},
		{"async", "async", "", AsyncResult("async"), false, false, true},
		{"error", "failure", "", ErrorResult("failure"), false, true, false},
		{"user", "visible", "visible", UserResult("visible"), false, false, false},
	} {
		if tc.got.ForLLM != tc.llm || tc.got.ForUser != tc.user || tc.got.Silent != tc.silent || tc.got.IsError != tc.isError || tc.got.Async != tc.async {
			t.Errorf("%s = %#v", tc.name, tc.got)
		}
	}
	wantErr := errors.New("underlying")
	result := UserResult("content").WithError(wantErr)
	if result.Err != wantErr || result.ForLLM == "" || result.ForUser == "" {
		t.Fatalf("WithError result = %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "underlying") || strings.Contains(string(encoded), `"Err"`) || !strings.Contains(string(encoded), `"for_llm":"content"`) || !strings.Contains(string(encoded), `"for_user":"content"`) {
		t.Fatalf("ToolResult JSON = %s, %v", encoded, err)
	}
}
