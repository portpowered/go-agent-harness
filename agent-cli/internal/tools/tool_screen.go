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
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ScreenTool captures the screen as a screenshot or a timed recording.
// On Windows it uses the GDI API directly (no CGO or external libraries
// needed); other supported platforms use their native capture command.
type ScreenTool struct {
	surface DisplaySurface
}

// NewScreenTool constructs a screen tool backed by the host display surface.
func NewScreenTool() *ScreenTool {
	return NewScreenToolWithDisplaySurface(NewHostDisplaySurface())
}

// NewScreenToolWithDisplaySurface injects the platform boundary used for
// admission, geometry, and capture. It is useful for hermetic tests and keeps
// capability loss observable after a session has already been admitted.
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
	if err := ctx.Err(); err != nil {
		return nil, displayUnavailableForCapability("show", DisplayCapability{}, err)
	}
	action, _ := args["action"].(string)
	if action == "" {
		action = "screenshot"
	}

	display := 0
	if d, ok := args["display"].(float64); ok && int(d) >= 0 {
		display = int(d)
	}

	switch action {
	case "screenshot", "record":
	default:
		return nil, fmt.Errorf("unknown action %q: use 'screenshot' or 'record'", action)
	}

	capability, err := t.displaySurface().Probe(ctx)
	if err != nil || !capability.Usable() {
		return nil, displayUnavailableForCapability("show", capability, err)
	}
	if display >= capability.DisplayCount {
		return nil, fmt.Errorf("display %d not available (only %d display(s) found)", display, capability.DisplayCount)
	}

	switch action {
	case "screenshot":
		return t.takeScreenshotWithContext(ctx, display)
	case "record":
		duration := 3.0
		if d, ok := args["duration"].(float64); ok && d >= 1 && d <= 30 {
			duration = d
		}
		fps := 2.0
		if f, ok := args["fps"].(float64); ok && f >= 1 && f <= 5 {
			fps = f
		}
		return t.recordScreen(ctx, display, duration, fps)
	}
	return nil, nil
}

func (t *ScreenTool) takeScreenshotWithContext(ctx context.Context, display int) ([]messages.Message, error) {
	bounds, err := t.displaySurface().Bounds(ctx, display)
	if err != nil {
		return nil, displayUnavailableForCapability("show geometry", DisplayCapability{}, err)
	}
	img, err := t.displaySurface().Capture(ctx, bounds)
	if err != nil {
		return nil, displayUnavailableForCapability("show capture", DisplayCapability{}, err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("encode screenshot: %w", err)
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
		return nil, displayUnavailableForCapability("show recording geometry", DisplayCapability{}, err)
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

		img, err := t.displaySurface().Capture(ctx, bounds)
		if err != nil {
			return nil, displayUnavailableForCapability(fmt.Sprintf("show recording frame %d", i), DisplayCapability{}, err)
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
		return nil, fmt.Errorf("encode recording: %w", err)
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

func displayUnavailableForCapability(operation string, capability DisplayCapability, cause error) error {
	reason := capability.Reason
	if reason == "" && cause == nil {
		reason = "no usable display or capture surface is available"
	}
	return &DisplayUnavailableError{Operation: operation, Reason: reason, Cause: cause}
}
