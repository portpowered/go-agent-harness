package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestReadImageTool_ReturnsExactlyOneRichImagePart(t *testing.T) {
	wantBytes := []byte{0x89, 'P', 'N', 'G', 0x00, 0x01}
	var gotPaths []string
	tool := NewReadImageTool(func(paths []string) ([]messages.ImagePart, error) {
		gotPaths = append([]string(nil), paths...)
		return []messages.ImagePart{{Bytes: append([]byte(nil), wantBytes...), MediaType: "image/png"}}, nil
	})

	msgs, err := tool.Execute(context.Background(), map[string]any{"path": "fixtures/known.png"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "fixtures/known.png" {
		t.Fatalf("preparer paths = %#v, want the exact path argument", gotPaths)
	}
	if len(msgs) != 1 || msgs[0].Role != messages.RoleTool || len(msgs[0].ContentParts) != 1 {
		t.Fatalf("messages = %#v, want one tool message with one content part", msgs)
	}
	if msgs[0].TextContent() != "" {
		t.Fatalf("tool result unexpectedly encoded image as text: %q", msgs[0].TextContent())
	}
	part, ok := msgs[0].ContentParts[0].(messages.ImagePart)
	if !ok {
		t.Fatalf("content part = %T, want messages.ImagePart", msgs[0].ContentParts[0])
	}
	if part.MediaType != "image/png" || !bytes.Equal(part.Bytes, wantBytes) {
		t.Fatalf("image part = %#v, want image/png and original bytes", part)
	}
}

func TestReadImageTool_RequiresPathAndSessionPreparation(t *testing.T) {
	tool := NewReadImageTool(nil)
	for name, args := range map[string]map[string]any{
		"missing": nil,
		"empty":   {"path": "  "},
	} {
		t.Run(name, func(t *testing.T) {
			msgs, err := tool.Execute(context.Background(), args)
			if err != nil {
				t.Fatalf("Execute returned Go error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Role != messages.RoleTool || msgs[0].TextContent() == "" {
				t.Fatalf("messages = %#v, want one textual tool failure", msgs)
			}
		})
	}

	msgs, err := tool.Execute(context.Background(), map[string]any{"path": "image.png"})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if msgs[0].TextContent() != ErrReadImagePreparerUnavailable.Error() {
		t.Fatalf("unconfigured result = %q, want %q", msgs[0].TextContent(), ErrReadImagePreparerUnavailable)
	}
}

func TestReadImageTool_SchemaHasRequiredStringPath(t *testing.T) {
	schema := ToolToSchema(NewReadImageTool(nil))
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	function, ok := decoded["function"].(map[string]any)
	if !ok {
		t.Fatalf("schema function = %#v", decoded["function"])
	}
	parameters, ok := function["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("schema parameters = %#v", function["parameters"])
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", parameters["properties"])
	}
	path, ok := properties["path"].(map[string]any)
	if !ok || path["type"] != "string" {
		t.Fatalf("path schema = %#v, want required string property", properties["path"])
	}
	required, ok := parameters["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "path" {
		t.Fatalf("required = %#v, want [path]", parameters["required"])
	}
}
