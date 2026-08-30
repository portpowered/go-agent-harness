package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ScreenTool captures the screen as a screenshot or a timed recording.
// The display surface is injected so capture, permission, and cancellation
// behavior can be tested without depending on the host desktop.
type ScreenTool struct {
	surface DisplaySurface
}

func NewScreenTool() *ScreenTool {
	return NewScreenToolWithDisplaySurface(NewHostDisplaySurface())
}

// NewScreenToolWithDisplaySurface injects the platform boundary used for
// display admission, geometry, and image capture.
func NewScreenToolWithDisplaySurface(surface DisplaySurface) *ScreenTool {
	if surface == nil {
		surface = NewHostDisplaySurface()
	}
	return &ScreenTool{surface: surface}
}

func (t *ScreenTool) Name() string { return "show" }

func (t *ScreenTool) Description() string {
	return "Capture a screenshot of the current screen or record it for a duration. " +
		"Use 'screenshot' to get the current screen state. " +
		"Use 'record' to capture a short screen recording as an animated image."
}

func (t *ScreenTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action to perform: 'screenshot' captures one frame, 'record' captures multiple frames over a duration",
				"enum":        []string{"screenshot", "record"},
			},
			"display": map[string]any{
				"type":        "integer",
				"description": "Display index to capture (0 = primary display). Defaults to 0.",
				"minimum":     0.0,
			},
			"duration": map[string]any{
				"type":        "number",
				"description": "Recording duration in seconds (1–30). Only used with 'record'. Defaults to 3.",
				"minimum":     1.0,
				"maximum":     30.0,
			},
			"fps": map[string]any{
				"type":        "number",
				"description": "Frames per second for recording (1–5). Only used with 'record'. Defaults to 2.",
				"minimum":     1.0,
				"maximum":     5.0,
			},
		},
		"required": []string{"action"},
	}
}

func (t *ScreenTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	action, _ := args["action"].(string)
	if action == "" {
		action = "screenshot"
	}
	switch action {
	case "screenshot", "record":
	default:
		return nil, fmt.Errorf("unknown action %q: use 'screenshot' or 'record'", action)
	}

	display := 0
	if d, ok := args["display"].(float64); ok {
		if d < 0 || d != float64(int(d)) {
			return nil, fmt.Errorf("display must be a non-negative integer, got %v", d)
		}
		display = int(d)
	}

	operationCtx := ctx
	var cancel context.CancelFunc
	if action == "screenshot" {
		operationCtx, cancel = boundedScreenContext(ctx, screenScreenshotBound)
		defer cancel()
	}
	if err := operationCtx.Err(); err != nil {
		return nil, newScreenCaptureError("show", "screen capture did not start", err)
	}

	surface := t.displaySurface()
	capability, probeErr := surface.Probe(operationCtx)
	if probeErr != nil || !capability.Usable() {
		reason := capability.Reason
		if display > 0 {
			reason = strings.TrimSpace(strings.Join([]string{
				fmt.Sprintf("display %d is not available", display), reason,
			}, ": "))
		}
		if probeErr != nil {
			capability.Reason = reason
			return nil, displayUnavailableForCapability("show", capability, probeErr)
		}
		capability.Reason = reason
		return nil, displayUnavailableForCapability("show", capability, nil)
	}
	if display >= capability.DisplayCount {
		return nil, &ScreenCaptureError{
			State:     ScreenCaptureUnavailable,
			Operation: "show",
			Reason:    fmt.Sprintf("display %d not available (only %d display(s) found)", display, capability.DisplayCount),
		}
	}

	switch action {
	case "screenshot":
		return t.takeScreenshotWithContext(operationCtx, display)
	case "record":
		duration := 3.0
		if d, ok := args["duration"].(float64); ok && d >= 1 && d <= 30 {
			duration = d
		}
		fps := 2.0
		if f, ok := args["fps"].(float64); ok && f >= 1 && f <= 5 {
			fps = f
		}
		return t.recordScreen(operationCtx, display, duration, fps)
	default:
		return nil, fmt.Errorf("unknown action %q: use 'screenshot' or 'record'", action)
	}
}

func (t *ScreenTool) takeScreenshotWithContext(ctx context.Context, display int) ([]messages.Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, newScreenCaptureError("show", "screen capture did not start", err)
	}
	bounds, err := t.displaySurface().Bounds(ctx, display)
	if err != nil {
		return nil, newScreenCaptureError("show display geometry", "display geometry is unavailable", err)
	}
	img, err := t.captureDisplay(ctx, display, bounds)
	if err != nil {
		return nil, newScreenCaptureError("show capture", "screen capture did not produce an image", err)
	}
	if img == nil || img.Bounds().Empty() {
		return nil, &ScreenCaptureError{
			State:     ScreenCaptureFailed,
			Operation: "show capture",
			Reason:    "screen capture returned empty image pixels",
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, newScreenCaptureError("show encode", "screenshot encoding failed", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, newScreenCaptureError("show encode", "screenshot encoding did not finish before the operation deadline", err)
	}

	msg := messages.Message{
		Role: messages.RoleTool,
		ContentParts: []messages.ContentPart{
			messages.TextPart{Text: fmt.Sprintf("Screenshot: display %d (%dx%d px)", display, bounds.Dx(), bounds.Dy())},
			messages.ImagePart{Bytes: buf.Bytes(), MediaType: "image/jpeg"},
		},
	}
	return []messages.Message{msg}, nil
}

func (t *ScreenTool) recordScreen(ctx context.Context, display int, duration, fps float64) ([]messages.Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("no frames captured: %w", err)
	}
	bounds, err := t.displaySurface().Bounds(ctx, display)
	if err != nil {
		return nil, newScreenCaptureError("show recording geometry", "display geometry is unavailable", err)
	}
	frameInterval := time.Duration(float64(time.Second) / fps)
	totalFrames := int(duration * fps)
	if totalFrames < 1 {
		totalFrames = 1
	}

	// GIF delay is in 100ths of a second (centiseconds).
	delayCS := int(frameInterval.Milliseconds() / 10)
	if delayCS < 1 {
		delayCS = 1
	}

	var frames []*image.Paletted
	var delays []int

	for i := 0; i < totalFrames; i++ {
		select {
		case <-ctx.Done():
			goto done
		default:
		}

		img, err := t.captureDisplay(ctx, display, bounds)
		if err != nil {
			return nil, newScreenCaptureError(fmt.Sprintf("show recording frame %d", i), "screen recording frame capture failed", err)
		}
		if img == nil || img.Bounds().Empty() {
			return nil, fmt.Errorf("capture frame %d: empty image pixels", i)
		}

		palImg := image.NewPaletted(img.Bounds(), palette.Plan9)
		draw.FloydSteinberg.Draw(palImg, img.Bounds(), img, image.Point{})
		frames = append(frames, palImg)
		delays = append(delays, delayCS)

		if i < totalFrames-1 {
			select {
			case <-ctx.Done():
				goto done
			case <-time.After(frameInterval):
			}
		}
	}

done:
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames captured")
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: frames, Delay: delays}); err != nil {
		return nil, newScreenCaptureError("show recording encode", "screen recording encoding failed", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, newScreenCaptureError("show recording encode", "screen recording encoding did not finish before the operation ended", err)
	}

	msg := messages.Message{
		Role: messages.RoleTool,
		ContentParts: []messages.ContentPart{
			messages.TextPart{Text: fmt.Sprintf(
				"Screen recording: display %d, %d frames, %.1fs at %.0f fps (%dx%d px)",
				display, len(frames), float64(len(frames))/fps, fps, bounds.Dx(), bounds.Dy(),
			)},
			messages.ImagePart{Bytes: buf.Bytes(), MediaType: "image/gif"},
		},
	}
	return []messages.Message{msg}, nil
}

func (t *ScreenTool) displaySurface() DisplaySurface {
	if t != nil && t.surface != nil {
		return t.surface
	}
	return NewHostDisplaySurface()
}

func (t *ScreenTool) captureDisplay(ctx context.Context, display int, bounds image.Rectangle) (*image.RGBA, error) {
	surface := t.displaySurface()
	if indexed, ok := surface.(indexedDisplaySurface); ok {
		return indexed.CaptureDisplay(ctx, display, bounds)
	}
	return surface.Capture(ctx, bounds)
}
