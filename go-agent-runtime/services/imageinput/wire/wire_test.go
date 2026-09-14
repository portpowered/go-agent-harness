package wire

import (
	"context"
	"image"
	"image/png"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
)

type loader struct{ bytes []byte }

func (loader loader) Load(context.Context, string) (messages.ContentPart, error) {
	return messages.ImagePart{Bytes: append([]byte(nil), loader.bytes...), MediaType: "image/png"}, nil
}

func TestNewServiceUsesExplicitLoader(t *testing.T) {
	var fixture imageBytes
	if err := png.Encode(&fixture, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	service := NewService(loader{bytes: fixture.data})
	parts, err := service.Prepare(context.Background(), []string{"fixture"}, imageinput.Capabilities{SupportsImageInput: true})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(parts) != 1 || parts[0].MediaType != "image/png" {
		t.Fatalf("parts = %#v, want one PNG", parts)
	}
}

type imageBytes struct{ data []byte }

func (buffer *imageBytes) Write(data []byte) (int, error) {
	buffer.data = append(buffer.data, data...)
	return len(data), nil
}
