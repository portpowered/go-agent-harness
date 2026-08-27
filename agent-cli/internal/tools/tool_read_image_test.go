package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestReadImageTool_ReturnsExactlyOneRichImagePart(t *testing.T) {
	wantBytes := minimalPNG()
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
	if len(msgs) != 1 || msgs[0].Role != messages.RoleTool || len(msgs[0].ContentParts) != 2 {
		t.Fatalf("messages = %#v, want one tool message with envelope and image parts", msgs)
	}
	var result ReadImageResult
	if err := json.Unmarshal([]byte(msgs[0].TextContent()), &result); err != nil {
		t.Fatalf("decode result envelope: %v", err)
	}
	if result.Version != ReadImageResultVersion || result.Status != ReadImageResultStatusSuccess {
		t.Fatalf("result envelope = %#v, want version %d success", result, ReadImageResultVersion)
	}
	if result.MIMEType != "image/png" || result.ByteLength != len(wantBytes) {
		t.Fatalf("result metadata = %#v, want image/png and %d bytes", result, len(wantBytes))
	}
	digest := sha256.Sum256(wantBytes)
	if result.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("result sha256 = %q, want %q", result.SHA256, hex.EncodeToString(digest[:]))
	}
	wantDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(wantBytes)
	if result.DataURL != wantDataURL {
		t.Fatalf("result data URL = %q, want exact data URL", result.DataURL)
	}
	encodedBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(result.DataURL, "data:image/png;base64,"))
	if err != nil || !bytes.Equal(encodedBytes, wantBytes) {
		t.Fatalf("result data URL does not decode to original bytes: err=%v bytes=%d", err, len(encodedBytes))
	}
	part, ok := msgs[0].ContentParts[1].(messages.ImagePart)
	if !ok {
		t.Fatalf("content part = %T, want messages.ImagePart", msgs[0].ContentParts[1])
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
			assertReadImageErrorEnvelope(t, msgs[0].TextContent(), "")
		})
	}

	msgs, err := tool.Execute(context.Background(), map[string]any{"path": "image.png"})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	result := assertReadImageErrorEnvelope(t, msgs[0].TextContent(), ErrReadImagePreparerUnavailable.Error())
	if result.Error != ErrReadImagePreparerUnavailable.Error() {
		t.Fatalf("unconfigured result error = %q, want %q", result.Error, ErrReadImagePreparerUnavailable)
	}
}

func TestReadImageTool_RejectsInvalidPreparerContentWithVersionedError(t *testing.T) {
	for name, part := range map[string]messages.ImagePart{
		"empty":      {MediaType: "image/png"},
		"corrupt":    {Bytes: []byte("not an image"), MediaType: "image/png"},
		"wrong mime": {Bytes: minimalPNG(), MediaType: "text/plain"},
	} {
		t.Run(name, func(t *testing.T) {
			tool := NewReadImageTool(func([]string) ([]messages.ImagePart, error) {
				return []messages.ImagePart{part}, nil
			})
			msgs, err := tool.Execute(context.Background(), map[string]any{"path": "fixture.png"})
			if err != nil {
				t.Fatalf("Execute returned Go error: %v", err)
			}
			result := assertReadImageErrorEnvelope(t, msgs[0].TextContent(), ErrReadImageInvalidResult.Error())
			if result.DataURL != "" || result.MIMEType != "" || result.ByteLength != 0 || result.SHA256 != "" {
				t.Fatalf("invalid result unexpectedly carried image metadata: %#v", result)
			}
		})
	}
}

func assertReadImageErrorEnvelope(t *testing.T, encoded, wantError string) ReadImageResult {
	t.Helper()
	var result ReadImageResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatalf("decode error envelope %q: %v", encoded, err)
	}
	if result.Version != ReadImageResultVersion || result.Status != ReadImageResultStatusError {
		t.Fatalf("error envelope = %#v, want version %d error", result, ReadImageResultVersion)
	}
	if result.Error == "" || (wantError != "" && !strings.Contains(result.Error, wantError)) {
		t.Fatalf("error envelope message = %q, want %q", result.Error, wantError)
	}
	if strings.Contains(encoded, "data_url") || strings.Contains(encoded, "byte_length") || strings.Contains(encoded, "sha256") {
		t.Fatalf("error envelope contains success image fields: %s", encoded)
	}
	return result
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
