package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/logger"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// updateDocumentAnyOfSchema mirrors the concrete case that reproduced the
// outage: a margin-local-docs-style update_document tool whose schema
// expresses "update by id OR update by selector" as a top-level anyOf. The
// OpenAI Realtime provider rejects a top-level anyOf outright
// (invalid_function_parameters on session.tools[N].parameters), and that
// rejection previously killed session.update - and with it every other tool
// in the session, not just this one.
func updateDocumentAnyOfSchema() json.RawMessage {
	return json.RawMessage(`{
		"description": "Update a document by id or by CSS selector.",
		"anyOf": [
			{
				"type": "object",
				"properties": {
					"document_id": {"type": "string", "description": "Target document id."},
					"content": {"type": "string", "description": "New document content."}
				},
				"required": ["document_id", "content"]
			},
			{
				"type": "object",
				"properties": {
					"selector": {"type": "string", "description": "CSS selector for the target element."},
					"content": {"type": "string", "description": "New document content."}
				},
				"required": ["selector", "content"]
			}
		]
	}`)
}

// TestPageToolAnyOfSchemaIsFlattenedToAnAcceptedShape is required test 1: a
// top-level anyOf is normalized into a shape the provider accepts, without
// losing the tool's meaning - every property named across every branch is
// still visible to the model.
func TestPageToolAnyOfSchemaIsFlattenedToAnAcceptedShape(t *testing.T) {
	broker := &recordingBroker{catalog: webmcp.ToolCatalogSnapshot{
		Generation: 1,
		Tools: []webmcp.ToolDescriptor{{
			Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:update-document"),
			Name:        "update_document",
			Description: "Update the document.",
			InputSchema: updateDocumentAnyOfSchema(),
		}},
	}}
	definitions := NewBrokerToolSet(broker).PageToolDefinitions(context.Background())
	if len(definitions) != 1 {
		t.Fatalf("page definitions = %d, want 1", len(definitions))
	}
	definition := definitions[0]

	var normalized map[string]any
	if err := json.Unmarshal(definition.ParameterSchema, &normalized); err != nil {
		t.Fatalf("normalized schema is not valid JSON: %v (%s)", err, definition.ParameterSchema)
	}
	if _, present := normalized["anyOf"]; present {
		t.Fatalf("normalized schema still carries a top-level anyOf, provider will still reject it: %s", definition.ParameterSchema)
	}
	if got, _ := normalized["type"].(string); got != "object" {
		t.Fatalf("normalized schema type = %q, want %q: %s", got, "object", definition.ParameterSchema)
	}
	properties, ok := normalized["properties"].(map[string]any)
	if !ok {
		t.Fatalf("normalized schema has no object properties: %s", definition.ParameterSchema)
	}
	for _, want := range []string{"document_id", "selector", "content"} {
		if _, present := properties[want]; !present {
			t.Fatalf("normalized schema dropped property %q (meaning lost): %s", want, definition.ParameterSchema)
		}
	}
	required, _ := normalized["required"].([]any)
	if len(required) != 1 || required[0] != "content" {
		t.Fatalf("normalized required = %v, want exactly [\"content\"] (the only field common to every anyOf branch)", required)
	}

	// The flat agent-loop Parameters view derives from the same normalized
	// schema, so it must also carry every branch's properties.
	names := make([]string, 0, len(definition.Parameters))
	requiredByName := map[string]bool{}
	for _, parameter := range definition.Parameters {
		names = append(names, parameter.Name)
		requiredByName[parameter.Name] = parameter.Required
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "content,document_id,selector" {
		t.Fatalf("flat parameters = %q, want content,document_id,selector", got)
	}
	if !requiredByName["content"] || requiredByName["document_id"] || requiredByName["selector"] {
		t.Fatalf("flat parameter required flags = %#v, want only content required", requiredByName)
	}
}

// TestPageToolUnnormalizableSchemaIsSkippedSessionSurvives is required test 2,
// the regression guard that matters most: a page tool whose schema cannot be
// made provider-acceptable is skipped, and the session still starts with
// every other tool intact. Before this fix, one bad tool made the whole
// session.update fail and killed the session - zero tools, zero turns.
func TestPageToolUnnormalizableSchemaIsSkippedSessionSurvives(t *testing.T) {
	broker := &recordingBroker{catalog: webmcp.ToolCatalogSnapshot{
		Generation: 1,
		Tools: []webmcp.ToolDescriptor{
			{
				Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:get-cube-state"),
				Name:        "get_cube_state",
				Description: "Read the current cube state.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			},
			{
				// A bare non-object top-level schema. OpenAI function-calling
				// parameters must always describe a JSON object; this can
				// never be normalized into one without fabricating a
				// property name the page never declared.
				Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:broken-tool"),
				Name:        "broken_tool",
				Description: "A page tool whose schema cannot be made provider-acceptable.",
				InputSchema: json.RawMessage(`{"type":"string"}`),
			},
			{
				Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:queue-cube-moves"),
				Name:        "queue_cube_moves",
				Description: "Queue cube rotations.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}},"required":["moves"],"additionalProperties":false}`),
			},
		},
	}}
	set := NewBrokerToolSet(broker)

	definitions, err := set.PageToolDefinitionsWithError(context.Background())
	if err != nil {
		t.Fatalf("PageToolDefinitionsWithError returned an error for one bad tool among three: %v, want the session to survive", err)
	}

	byName := map[string]messages.ToolDefinition{}
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	if _, present := byName["broken_tool"]; present {
		t.Fatalf("unnormalizable tool broken_tool was published anyway: %v", byName)
	}
	if _, present := byName["get_cube_state"]; !present {
		t.Fatalf("healthy tool get_cube_state was dropped along with the bad tool: %v", byName)
	}
	if _, present := byName["queue_cube_moves"]; !present {
		t.Fatalf("healthy tool queue_cube_moves was dropped along with the bad tool: %v", byName)
	}
	if len(definitions) != 2 {
		t.Fatalf("page definitions = %d (%v), want exactly the 2 healthy tools", len(definitions), byName)
	}
}

// TestPageToolSkippedSchemaWarningNamesToolAndReason is required test 3: the
// degradation must be visible, not silent. A skipped tool logs a warning
// naming the tool and the reason it could not be normalized.
func TestPageToolSkippedSchemaWarningNamesToolAndReason(t *testing.T) {
	core, observed := observer.New(zapcore.WarnLevel)
	ctx := logger.WithLogger(context.Background(), zap.New(core))

	broker := &recordingBroker{catalog: webmcp.ToolCatalogSnapshot{
		Generation: 1,
		Tools: []webmcp.ToolDescriptor{{
			Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:broken-tool"),
			Name:        "broken_tool",
			Description: "A page tool whose schema cannot be made provider-acceptable.",
			InputSchema: json.RawMessage(`{"type":"string"}`),
		}},
	}}
	set := NewBrokerToolSet(broker)

	if _, err := set.PageToolDefinitionsWithError(ctx); err != nil {
		t.Fatalf("PageToolDefinitionsWithError: %v", err)
	}

	entries := observed.FilterLevelExact(zapcore.WarnLevel).All()
	if len(entries) != 1 {
		t.Fatalf("warn-level log entries = %d, want exactly 1 naming the skipped tool: %#v", len(entries), entries)
	}
	fields := entries[0].ContextMap()
	if fields["tool"] != "broken_tool" {
		t.Fatalf("warning does not name the skipped tool: %#v", fields)
	}
	reason, _ := fields["reason"].(string)
	if reason == "" {
		t.Fatalf("warning does not carry a reason the tool could not be normalized: %#v", fields)
	}
}

// TestPageToolNormalSchemasUnaffectedByNormalization is required test 4: a
// tool with an ordinary object schema (no top-level combinator) must pass
// through byte-for-byte unchanged - normalization must not over-trigger on
// schemas the provider already accepts.
func TestPageToolNormalSchemasUnaffectedByNormalization(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}},"required":["moves"],"additionalProperties":false}`)
	broker := &recordingBroker{catalog: webmcp.ToolCatalogSnapshot{
		Generation: 1,
		Tools: []webmcp.ToolDescriptor{{
			Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:queue-cube-moves"),
			Name:        "queue_cube_moves",
			Description: "Queue cube rotations.",
			InputSchema: schema,
		}},
	}}
	definitions := NewBrokerToolSet(broker).PageToolDefinitions(context.Background())
	if len(definitions) != 1 {
		t.Fatalf("page definitions = %d, want 1", len(definitions))
	}
	assertJSONValueEqual(t, definitions[0].ParameterSchema, schema)
}
