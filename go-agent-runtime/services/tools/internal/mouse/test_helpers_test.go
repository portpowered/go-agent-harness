package mouse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
)

type portableDisplaySurface struct {
	capability display.DisplayCapability
}

func (s *portableDisplaySurface) Probe(context.Context) (display.DisplayCapability, error) {
	return s.capability, nil
}

func (s *portableDisplaySurface) DisplayCount(context.Context) (int, error) {
	return s.capability.DisplayCount, nil
}

func (*portableDisplaySurface) Bounds(context.Context, int) (image.Rectangle, error) {
	return image.Rect(0, 0, 1, 1), nil
}

func (*portableDisplaySurface) Capture(context.Context, image.Rectangle) (*image.RGBA, error) {
	return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
}

func screenDisplayCount() int {
	count, err := display.NewHostDisplaySurface().DisplayCount(context.Background())
	if err != nil {
		return 0
	}
	return count
}

func screenDisplayBounds(index int) image.Rectangle {
	bounds, err := display.NewHostDisplaySurface().Bounds(context.Background(), index)
	if err != nil {
		return image.Rectangle{}
	}
	return bounds
}

func loadPNGasRGBA(path string) (*image.RGBA, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open screenshot: %w", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("decode screenshot: %w", err)
	}
	result := image.NewRGBA(decoded.Bounds())
	for y := decoded.Bounds().Min.Y; y < decoded.Bounds().Max.Y; y++ {
		for x := decoded.Bounds().Min.X; x < decoded.Bounds().Max.X; x++ {
			result.Set(x, y, decoded.At(x, y))
		}
	}
	return result, nil
}

func assertScreenResult(t *testing.T, message messages.Message, mediaType string, width, height int) messages.ImagePart {
	t.Helper()
	if message.Role != messages.RoleTool || len(message.ContentParts) != 2 {
		t.Fatalf("screen result = %#v, want one metadata part and one image part", message)
	}
	var result display.ScreenResult
	if err := json.Unmarshal([]byte(message.TextContent()), &result); err != nil {
		t.Fatalf("decode screen result: %v", err)
	}
	imagePart, ok := message.ContentParts[1].(messages.ImagePart)
	if !ok {
		t.Fatalf("screen image part = %T, want messages.ImagePart", message.ContentParts[1])
	}
	digest := sha256.Sum256(imagePart.Bytes)
	wantDigest := fmt.Sprintf("%x", digest)
	if result.Version != display.ScreenResultVersion || result.Status != display.ScreenResultStatusSuccess || result.Source != display.ScreenResultSource || result.MIMEType != mediaType || result.ByteLength != len(imagePart.Bytes) || result.Width != width || result.Height != height || result.SHA256 != wantDigest || result.TypedProjection != display.ScreenResultTypedProjectionInputImage {
		t.Fatalf("screen result = %+v, image = %s/%d bytes; want version/status/source/mime/dimensions/digest projection", result, imagePart.MediaType, len(imagePart.Bytes))
	}
	if imagePart.MediaType != mediaType || len(imagePart.Bytes) == 0 {
		t.Fatalf("screen image part = %#v, want non-empty %s", imagePart, mediaType)
	}
	return imagePart
}
