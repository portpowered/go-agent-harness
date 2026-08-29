package messages

import (
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
		},
		{Name: "alpha", Description: "first"},
	}
	wantInput := append([]ToolDefinition(nil), input...)
	wantInput[0].Parameters = append([]ToolParameter(nil), input[0].Parameters...)

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
	if input[0].Name != "zeta" || input[0].Parameters[1].Name != "a" {
		t.Fatalf("canonical result aliases caller-owned input: %#v", input)
	}
}
