package mouse

import (
	"context"
	"image"

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
