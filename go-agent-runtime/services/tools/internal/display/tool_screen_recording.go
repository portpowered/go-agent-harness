package display

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/sight"
)

type screenRecordingOptions struct {
	durationSeconds float64
	fps             float64
	frameInterval   time.Duration
	maxFrames       int
	delayCS         int
}

func parseScreenRecordingOptions(args map[string]any) (screenRecordingOptions, error) {
	duration := defaultScreenRecordingDurationSeconds
	if raw, ok := args["duration"]; ok {
		value, err := screenNumberArgument(raw, "duration")
		if err != nil {
			return screenRecordingOptions{}, err
		}
		duration = value
	}
	fps := defaultScreenRecordingFPS
	if raw, ok := args["fps"]; ok {
		value, err := screenNumberArgument(raw, "fps")
		if err != nil {
			return screenRecordingOptions{}, err
		}
		fps = value
	}
	return newScreenRecordingOptions(duration, fps)
}

func newScreenRecordingOptions(duration, fps float64) (screenRecordingOptions, error) {
	if err := validateScreenRecordingNumber("duration", duration, minScreenRecordingDurationSeconds, maxScreenRecordingDurationSeconds, "seconds"); err != nil {
		return screenRecordingOptions{}, err
	}
	if err := validateScreenRecordingNumber("fps", fps, minScreenRecordingFPS, maxScreenRecordingFPS, "frames per second"); err != nil {
		return screenRecordingOptions{}, err
	}
	frameInterval := time.Duration(float64(time.Second) / fps)
	maxFrames := int(math.Ceil(duration * fps))
	if maxFrames < 1 {
		maxFrames = 1
	}
	delayCS := int(math.Round(float64(frameInterval) / float64(10*time.Millisecond)))
	if delayCS < 1 {
		delayCS = 1
	}
	return screenRecordingOptions{
		durationSeconds: duration,
		fps:             fps,
		frameInterval:   frameInterval,
		maxFrames:       maxFrames,
		delayCS:         delayCS,
	}, nil
}

func validateScreenRecordingNumber(field string, value, minimum, maximum float64, unit string) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < minimum || value > maximum {
		return &ScreenRecordingValidationError{
			Field:  field,
			Value:  formatScreenNumber(value),
			Reason: fmt.Sprintf("must be between %s and %s %s", formatScreenNumber(minimum), formatScreenNumber(maximum), unit),
		}
	}
	return nil
}

func screenNumberArgument(value any, field string) (float64, error) {
	switch number := value.(type) {
	case float64:
		return number, nil
	case float32:
		return float64(number), nil
	case int:
		return float64(number), nil
	case int8:
		return float64(number), nil
	case int16:
		return float64(number), nil
	case int32:
		return float64(number), nil
	case int64:
		return float64(number), nil
	case uint:
		return float64(number), nil
	case uint8:
		return float64(number), nil
	case uint16:
		return float64(number), nil
	case uint32:
		return float64(number), nil
	case uint64:
		return float64(number), nil
	case json.Number:
		parsed, err := strconv.ParseFloat(string(number), 64)
		if err == nil {
			return parsed, nil
		}
	}
	return 0, &ScreenRecordingValidationError{
		Field:  field,
		Value:  fmt.Sprintf("%T", value),
		Reason: "must be a finite number",
	}
}

func parseDisplayIndex(value float64) (int, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || math.Trunc(value) != value || value > float64(^uint(0)>>1) {
		return 0, fmt.Errorf("display must be a non-negative integer, got %v", value)
	}
	return int(value), nil
}

func formatScreenNumber(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func (t *ScreenTool) recordScreenWithOptions(ctx context.Context, display int, options screenRecordingOptions) ([]messages.Message, error) {
	if ctx == nil {
		return nil, errors.New("screen recording context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, newScreenCaptureError("show recording", "no frames captured: recording did not start", err)
	}
	bounds, err := t.displaySurface().Bounds(ctx, display)
	if err != nil {
		return nil, newScreenCaptureError("show recording geometry", "display geometry is unavailable", err)
	}
	frames, delays, err := t.captureRecordingFrames(ctx, display, bounds, options)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, newScreenCaptureError("show recording", "no frames captured", errors.New("no frames captured"))
	}
	encoded, err := t.encodeRecording(ctx, frames, delays)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeRecording(ctx, encoded)
	if err != nil {
		return nil, err
	}
	if len(decoded.Image) != len(frames) {
		return nil, newScreenCaptureError("show recording result", "screen recording encoder returned an unexpected frame count", fmt.Errorf("encoded %d frame(s), want %d", len(decoded.Image), len(frames)))
	}
	firstFrame := decoded.Image[0]
	result, err := sight.NewSuccess(sight.SourceScreen, "image/gif", encoded, firstFrame.Bounds().Dx(), firstFrame.Bounds().Dy())
	if err != nil {
		return nil, newScreenCaptureError("show recording result", "screen recording metadata could not be created", err)
	}
	result.FrameCount = len(frames)
	result.DurationSeconds = float64(len(frames)) / options.fps
	msg, err := screenImageMessageFromResult(result, encoded)
	if err != nil {
		return nil, newScreenCaptureError("show recording result", "screen recording metadata could not be created", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, screenRecordingContextError("show recording result", "screen recording ended before its result could be returned", err)
	}
	return []messages.Message{msg}, nil
}

func (t *ScreenTool) captureRecordingFrames(ctx context.Context, display int, bounds image.Rectangle, options screenRecordingOptions) ([]*image.Paletted, []int, error) {
	frames := make([]*image.Paletted, 0, options.maxFrames)
	delays := make([]int, 0, options.maxFrames)
	startedAt := time.Now()
	for i := 0; i < options.maxFrames; i++ {
		if i > 0 {
			target := startedAt.Add(time.Duration(i) * options.frameInterval)
			if err := waitForScreenRecordingFrame(ctx, target); err != nil {
				return nil, nil, screenRecordingContextError("show recording wait", "screen recording stopped before the next frame", err)
			}
		}
		frame, err := t.captureRecordingFrame(ctx, display, bounds, i)
		if err != nil {
			return nil, nil, err
		}
		frames = append(frames, frame)
		delays = append(delays, options.delayCS)
	}
	return frames, delays, nil
}

func (t *ScreenTool) captureRecordingFrame(ctx context.Context, display int, bounds image.Rectangle, index int) (*image.Paletted, error) {
	if err := ctx.Err(); err != nil {
		return nil, screenRecordingContextError("show recording", "screen recording stopped before frame capture", err)
	}
	img, err := t.captureDisplay(ctx, display, bounds)
	if err != nil {
		return nil, newScreenCaptureError(fmt.Sprintf("show recording frame %d", index), "screen recording frame capture failed", err)
	}
	if img == nil || img.Bounds().Empty() {
		return nil, newScreenCaptureError(fmt.Sprintf("show recording frame %d", index), "screen recording returned empty image pixels", errors.New("empty image pixels"))
	}
	palImg, err := palettizeScreenFrame(ctx, img)
	if err != nil {
		return nil, screenRecordingContextError(fmt.Sprintf("show recording frame %d", index), "screen recording frame conversion did not finish", err)
	}
	return palImg, nil
}

func (t *ScreenTool) encodeRecording(ctx context.Context, frames []*image.Paletted, delays []int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, screenRecordingContextError("show recording encode", "screen recording stopped before encoding", err)
	}
	var buf bytes.Buffer
	recording := &gif.GIF{Image: frames, Delay: delays}
	if err := t.recordingEncoder().Encode(ctx, contextAwareScreenWriter{ctx: ctx, writer: &buf}, recording); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, screenRecordingContextError("show recording encode", "screen recording encoding did not finish before the operation ended", ctxErr)
		}
		return nil, newScreenCaptureError("show recording encode", "screen recording encoding failed", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, screenRecordingContextError("show recording encode", "screen recording encoding did not finish before the operation ended", err)
	}
	encoded := append([]byte(nil), buf.Bytes()...)
	if len(encoded) == 0 {
		return nil, newScreenCaptureError("show recording encode", "screen recording encoder returned empty animation bytes", errors.New("empty animation bytes"))
	}
	return encoded, nil
}

func decodeRecording(ctx context.Context, encoded []byte) (*gif.GIF, error) {
	decoded, err := gif.DecodeAll(contextReader{ctx: ctx, r: bytes.NewReader(encoded)})
	if err == nil {
		return decoded, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, screenRecordingContextError("show recording result", "screen recording validation did not finish", ctxErr)
	}
	return nil, newScreenCaptureError("show recording result", "screen recording encoder returned an undecodable GIF", err)
}

func screenImageMessage(source, mediaType string, pixels []byte, width, height int) (messages.Message, error) {
	result, err := sight.NewSuccess(source, mediaType, pixels, width, height)
	if err != nil {
		return messages.Message{}, err
	}
	return screenImageMessageFromResult(result, pixels)
}

func screenImageMessageFromResult(result sight.Result, pixels []byte) (messages.Message, error) {
	encoded, err := sight.Encode(result)
	if err != nil {
		return messages.Message{}, err
	}
	ownedPixels := append([]byte(nil), pixels...)
	return messages.Message{
		Role: messages.RoleTool,
		ContentParts: []messages.ContentPart{
			messages.TextPart{Text: string(encoded)},
			messages.ImagePart{Bytes: ownedPixels, MediaType: result.MIMEType},
		},
	}, nil
}

func waitForScreenRecordingFrame(ctx context.Context, target time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wait := time.Until(target)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func palettizeScreenFrame(ctx context.Context, img image.Image) (*image.Paletted, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	paletted := image.NewPaletted(bounds, palette.Plan9)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			paletted.SetColorIndex(x, y, uint8(color.Palette(palette.Plan9).Index(img.At(x, y))))
		}
	}
	return paletted, ctx.Err()
}

type contextAwareScreenWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextAwareScreenWriter) Write(p []byte) (int, error) {
	if w.ctx != nil {
		if err := w.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := w.writer.Write(p)
	if err != nil {
		return n, err
	}
	if w.ctx != nil {
		if ctxErr := w.ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, nil
}

func screenRecordingContextError(operation, reason string, cause error) error {
	return newScreenCaptureError(operation, reason, cause)
}

func (t *ScreenTool) displaySurface() DisplaySurface {
	if t != nil && t.surface != nil {
		return t.surface
	}
	return NewHostDisplaySurface()
}

func (t *ScreenTool) recordingEncoder() ScreenRecordingEncoder {
	if t != nil && t.recordEncoder != nil {
		return t.recordEncoder
	}
	return standardScreenRecordingEncoder{}
}

func (t *ScreenTool) captureDisplay(ctx context.Context, display int, bounds image.Rectangle) (*image.RGBA, error) {
	surface := t.displaySurface()
	if indexed, ok := surface.(indexedDisplaySurface); ok {
		return indexed.CaptureDisplay(ctx, display, bounds)
	}
	return surface.Capture(ctx, bounds)
}

func (t *ScreenTool) ScreenRecordingPermissionRecheckSupported() bool {
	if t == nil {
		return false
	}
	rechecker, ok := t.displaySurface().(ScreenRecordingPermissionRechecker)
	return ok && rechecker.ScreenRecordingPermissionRecheckSupported()
}

func (t *ScreenTool) RecheckScreenRecordingPermission(ctx context.Context) (DisplayPermission, error) {
	if !t.ScreenRecordingPermissionRecheckSupported() {
		return DisplayPermission{
			State:  DisplayPermissionUnavailable,
			Reason: "macOS Screen Recording permission re-check is unavailable",
		}, nil
	}
	rechecker, ok := t.displaySurface().(ScreenRecordingPermissionRechecker)
	if !ok {
		return DisplayPermission{
			State:  DisplayPermissionUnavailable,
			Reason: "macOS Screen Recording permission re-check is unavailable",
		}, nil
	}
	return rechecker.RecheckScreenRecordingPermission(ctx)
}
