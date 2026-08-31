package gemini

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestToolsToGeminiToolsPreservesCompletePageSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"object","properties":{"face":{"type":"string","enum":["R","U"]},"turns":{"type":"integer","minimum":1}},"required":["face","turns"],"additionalProperties":false}}},"required":["moves"],"additionalProperties":false}`)
	tools := toolsToGeminiTools([]models.ToolDefinition{{Name: "queue_cube_moves", ParameterSchema: schema}})
	if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("Gemini tools = %#v, want one function declaration", tools)
	}
	declaration := tools[0].FunctionDeclarations[0]
	if declaration.Parameters != nil {
		t.Fatalf("Gemini declaration unexpectedly used reduced Parameters schema: %#v", declaration.Parameters)
	}
	got, ok := declaration.ParametersJsonSchema.(map[string]any)
	if !ok {
		t.Fatalf("Gemini ParametersJsonSchema = %T, want map", declaration.ParametersJsonSchema)
	}
	var want map[string]any
	if err := json.Unmarshal(schema, &want); err != nil {
		t.Fatalf("decode expected schema: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Gemini schema = %#v, want %#v", got, want)
	}
}
