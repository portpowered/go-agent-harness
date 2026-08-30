package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func assertScreenResult(t *testing.T, message messages.Message, mediaType string, width, height int) messages.ImagePart {
	t.Helper()
	if message.Role != messages.RoleTool || len(message.ContentParts) != 2 {
		t.Fatalf("screen result = %#v, want one metadata part and one image part", message)
	}
	var result ScreenResult
	if err := json.Unmarshal([]byte(message.TextContent()), &result); err != nil {
		t.Fatalf("decode screen result: %v", err)
	}
	imagePart, ok := message.ContentParts[1].(messages.ImagePart)
	if !ok {
		t.Fatalf("screen image part = %T, want messages.ImagePart", message.ContentParts[1])
	}
	digest := sha256.Sum256(imagePart.Bytes)
	wantDigest := fmt.Sprintf("%x", digest)
	if result.Version != ScreenResultVersion || result.Status != ScreenResultStatusSuccess || result.Source != ScreenResultSource || result.MIMEType != mediaType || result.ByteLength != len(imagePart.Bytes) || result.Width != width || result.Height != height || result.SHA256 != wantDigest || result.TypedProjection != ScreenResultTypedProjectionInputImage {
		t.Fatalf("screen result = %+v, image = %s/%d bytes; want version/status/source/mime/dimensions/digest projection", result, imagePart.MediaType, len(imagePart.Bytes))
	}
	if imagePart.MediaType != mediaType || len(imagePart.Bytes) == 0 {
		t.Fatalf("screen image part = %#v, want non-empty %s", imagePart, mediaType)
	}
	return imagePart
}
