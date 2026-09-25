//go:build linux || darwin

package mouse

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"

	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
)

// Helpers for the command-based desktop platforms (linux, darwin).

func screenDisplayCount() int {
	count, err := display.NewHostDisplaySurface().DisplayCount(context.Background())
	if err != nil {
		return 0
	}
	return count
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
