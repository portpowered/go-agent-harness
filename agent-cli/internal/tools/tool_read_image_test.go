package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
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
	if result.TypedProjection != ReadImageResultTypedProjectionInputImage {
		t.Fatalf("result typed projection = %q, want %q", result.TypedProjection, ReadImageResultTypedProjectionInputImage)
	}
	encoded := msgs[0].TextContent()
	if len(encoded) > 1024 || strings.Contains(strings.ToLower(encoded), "data:") || strings.Contains(strings.ToLower(encoded), "base64") {
		t.Fatalf("result envelope is not compact or contains encoded image data: bytes=%d envelope=%q", len(encoded), encoded)
	}
	part, ok := msgs[0].ContentParts[1].(messages.ImagePart)
	if !ok {
		t.Fatalf("content part = %T, want messages.ImagePart", msgs[0].ContentParts[1])
	}
	if part.MediaType != "image/png" || !bytes.Equal(part.Bytes, wantBytes) {
		t.Fatalf("image part = %#v, want image/png and original bytes", part)
	}
}

func TestReadImageTool_SuccessEnvelopeSizeIsIndependentOfImageSize(t *testing.T) {
	small := minimalPNG()
	large := multiMegabytePNG(t)
	if len(large) <= 1<<20 {
		t.Fatalf("large fixture = %d bytes, want more than one MiB", len(large))
	}

	for name, imageBytes := range map[string][]byte{"small": small, "multi-megabyte": large} {
		t.Run(name, func(t *testing.T) {
			tool := NewReadImageTool(func([]string) ([]messages.ImagePart, error) {
				return []messages.ImagePart{{Bytes: imageBytes, MediaType: "image/png"}}, nil
			})
			got, err := tool.Execute(context.Background(), map[string]any{"path": name + ".png"})
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if len(got) != 1 || len(got[0].ContentParts) != 2 {
				t.Fatalf("messages = %#v, want one envelope and one image part", got)
			}
			envelope := got[0].TextContent()
			if len(envelope) == 0 || len(envelope) > 1024 {
				t.Fatalf("envelope size = %d, want 1..1024 bytes", len(envelope))
			}
			lower := strings.ToLower(envelope)
			if strings.Contains(lower, "data_url") || strings.Contains(lower, "data:") || strings.Contains(lower, "base64") {
				t.Fatalf("envelope contains an image-bearing field: %s", envelope)
			}

			var result ReadImageResult
			if err := json.Unmarshal([]byte(envelope), &result); err != nil {
				t.Fatalf("decode result envelope: %v", err)
			}
			digest := sha256.Sum256(imageBytes)
			if result.Version != ReadImageResultVersion || result.Status != ReadImageResultStatusSuccess ||
				result.MIMEType != "image/png" || result.ByteLength != len(imageBytes) ||
				result.SHA256 != hex.EncodeToString(digest[:]) ||
				result.TypedProjection != ReadImageResultTypedProjectionInputImage {
				t.Fatalf("result = %#v, want metadata for %d bytes", result, len(imageBytes))
			}
			part, ok := got[0].ContentParts[1].(messages.ImagePart)
			if !ok || part.MediaType != result.MIMEType || !bytes.Equal(part.Bytes, imageBytes) {
				t.Fatalf("rich image part = %#v, want the exact prepared snapshot", got[0].ContentParts[1])
			}
		})
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
			if result.TypedProjection != "" || result.MIMEType != "" || result.ByteLength != 0 || result.SHA256 != "" {
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
	if strings.Contains(encoded, "data_url") || strings.Contains(encoded, "byte_length") || strings.Contains(encoded, "sha256") || strings.Contains(encoded, "typed_projection") {
		t.Fatalf("error envelope contains success image fields: %s", encoded)
	}
	return result
}

func multiMegabytePNG(t *testing.T) []byte {
	t.Helper()
	const size = 1024
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	state := uint32(0x6d2b79f5)
	for i := range img.Pix {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		img.Pix[i] = uint8(state >> 24)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xff
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatalf("encode large PNG: %v", err)
	}
	return encoded.Bytes()
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
