package browser

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBrowserToolDefinitionsAreFreshAndClosed(t *testing.T) {
	first := BrowserToolDefinitions(true)
	if len(first) != 12 {
		t.Fatalf("browser definitions = %d, want 12", len(first))
	}
	if first[0].Name != GetContextToolName || first[5].Name != CancelToolName {
		t.Fatalf("stable ordering changed: first=%q sixth=%q", first[0].Name, first[5].Name)
	}
	parameters, ok := first[0].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("first parameters = %#v, want properties object", first[0].Parameters)
	}
	first[0].Parameters["additionalProperties"] = true
	parameters["refresh"] = map[string]any{"type": "number"}
	second := BrowserToolDefinitions(true)
	if second[0].Parameters["additionalProperties"] != false {
		t.Fatal("browser definitions share mutable schema state")
	}
	secondProperties, ok := second[0].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("second properties = %#v, want object", second[0].Parameters["properties"])
	}
	refresh, ok := secondProperties["refresh"].(map[string]any)
	if !ok || refresh["type"] != "boolean" {
		t.Fatal("browser definitions share mutable property state")
	}
}

func TestBrowserToolSchemasUseClosedFunctionObjects(t *testing.T) {
	schemas := BrowserToolSchemas()
	if len(schemas) != 9 {
		t.Fatalf("schemas = %d, want 9", len(schemas))
	}
	for index, schema := range schemas {
		if schema["type"] != "function" {
			t.Fatalf("schema %d type = %#v", index, schema["type"])
		}
		function, ok := schema["function"].(map[string]any)
		if !ok {
			t.Fatalf("schema %d function = %#v", index, schema["function"])
		}
		parameters, ok := function["parameters"].(map[string]any)
		if !ok || parameters["type"] != pageJSONSchemaObjectType || parameters["additionalProperties"] != false {
			t.Fatalf("schema %d parameters are not closed objects: %#v", index, function["parameters"])
		}
	}
}

func TestValidatePageToolInputUsesBoundedJSONSchemaPolicy(t *testing.T) {
	tests := []struct {
		name   string
		input  json.RawMessage
		schema json.RawMessage
		limit  int
		want   []ToolResultIssue
	}{
		{
			name:   "valid exact number",
			input:  json.RawMessage(`{"count":90071992547409931234567890}`),
			schema: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"],"additionalProperties":false}`),
		},
		{
			name:   "required property",
			input:  json.RawMessage(`{}`),
			schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
			want:   []ToolResultIssue{{Path: "/name", Code: "required"}},
		},
		{
			name:   "reference and unknown property",
			input:  json.RawMessage(`{"name":12,"secret":"redacted"}`),
			schema: json.RawMessage(`{"$defs":{"name":{"type":"string"}},"type":"object","properties":{"name":{"$ref":"#/$defs/name"}},"additionalProperties":false}`),
			want: []ToolResultIssue{
				{Path: "/name", Code: "invalid_type"},
				{Path: "/secret", Code: "unknown_property"},
			},
		},
		{
			name:   "duplicate and malformed",
			input:  json.RawMessage(`{"a":1,"a":2}`),
			schema: json.RawMessage(`{"type":"object"}`),
			want:   []ToolResultIssue{{Path: "/a", Code: "duplicate_property"}},
		},
		{
			name:   "input limit",
			input:  json.RawMessage(`{"value":"too large"}`),
			schema: json.RawMessage(`{"type":"object"}`),
			limit:  4,
			want:   []ToolResultIssue{{Path: "/", Code: "input_too_large"}},
		},
		{
			name:   "invalid utf8",
			input:  json.RawMessage([]byte{'{', 0xff, '}'}),
			schema: json.RawMessage(`{"type":"object"}`),
			want:   []ToolResultIssue{{Path: "/", Code: "invalid_utf8"}},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := (Contract{}).ValidatePageToolInput(testCase.input, testCase.schema, testCase.limit)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("issues = %#v, want %#v", got, testCase.want)
			}
		})
	}
}
