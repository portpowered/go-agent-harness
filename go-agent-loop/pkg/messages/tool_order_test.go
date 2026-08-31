package messages

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCanonicalToolDefinitionsSortsOwnedSnapshotWithoutMutatingInput(t *testing.T) {
	input := []ToolDefinition{
		{
			Name:        "zeta",
			Description: "last",
			Parameters: []ToolParameter{
				{Name: "z", Type: "string"},
				{Name: "a", Type: "boolean", Required: true},
			},
			ParameterSchema: json.RawMessage(`{"type":"object","properties":{"nested":{"type":"array","items":{"type":"string"}}}}`),
		},
		{Name: "alpha", Description: "first"},
	}
	wantInput := append([]ToolDefinition(nil), input...)
	wantInput[0].Parameters = append([]ToolParameter(nil), input[0].Parameters...)
	wantInput[0].ParameterSchema = append(json.RawMessage(nil), input[0].ParameterSchema...)

	got := CanonicalToolDefinitions(input)
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("canonical tool order = %#v, want alpha then zeta", got)
	}
	if got[1].Parameters[0].Name != "a" || got[1].Parameters[1].Name != "z" {
		t.Fatalf("canonical parameter order = %#v, want a then z", got[1].Parameters)
	}
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("canonicalization mutated input:\n got: %#v\nwant: %#v", input, wantInput)
	}

	got[1].Name = "mutated"
	got[1].Parameters[0].Name = "mutated-parameter"
	got[1].ParameterSchema = json.RawMessage(`{"type":"object","properties":{"mutated":{"type":"boolean"}}}`)
	if input[0].Name != "zeta" || input[0].Parameters[1].Name != "a" {
		t.Fatalf("canonical result aliases caller-owned input: %#v", input)
	}
	if string(input[0].ParameterSchema) != `{"type":"object","properties":{"nested":{"type":"array","items":{"type":"string"}}}}` {
		t.Fatalf("canonical result aliases caller-owned parameter schema: %s", input[0].ParameterSchema)
	}
}
