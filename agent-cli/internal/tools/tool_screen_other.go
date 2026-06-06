//go:build !windows && !linux && !darwin

package tools

import (
	"fmt"
	"image"
)

func screenDisplayCount() int { return 1 }

func screenDisplayBounds(_ int) image.Rectangle {
	return image.Rect(0, 0, 1920, 1080)
}

func screenCapture(_ image.Rectangle) (*image.RGBA, error) {
	return nil, fmt.Errorf("screen capture is not yet supported on this platform")
}
