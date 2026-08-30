package tools

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
	"image/jpeg"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ScreenTool captures the screen as a screenshot or a timed recording.
// The display surface is injected so capture, permission, and cancellation
// behavior can be tested without depending on the host desktop.
type ScreenTool struct {
	surface       DisplaySurface
	recordEncoder ScreenRecordingEncoder
}

const (
	defaultScreenRecordingDurationSeconds = 3.0
	minScreenRecordingDurationSeconds     = 1.0
	maxScreenRecordingDurationSeconds     = 5.0
	defaultScreenRecordingFPS             = 2.0
	minScreenRecordingFPS                 = 1.0
	maxScreenRecordingFPS                 = 2.0
)

var (
	// ErrInvalidScreenRecording identifies a record request that cannot be
	// admitted under the interactive capture limits.
	ErrInvalidScreenRecording = errors.New("invalid screen recording option")
	// ErrInvalidScreenRecordingOption is a descriptive compatibility alias.
	ErrInvalidScreenRecordingOption = ErrInvalidScreenRecording
)

const (
	// ScreenToolID is the stable model-facing name of the physical display
	// capture tool.
	ScreenToolID = "show"

	// ScreenRecordingPermissionDeniedErrorCode is the provider-visible error
	// classification for a denied macOS Screen Recording preflight.
	ScreenRecordingPermissionDeniedErrorCode = "screen_recording_permission_denied"

	ScreenResultVersion                   = sight.ResultVersion
	ScreenResultStatusSuccess             = sight.StatusSuccess
	ScreenResultStatusError               = sight.StatusError
	ScreenResultTypedProjectionInputImage = sight.TypedProjectionInputImage
	ScreenResultSource                    = sight.SourceScreen
)

// ScreenResult is the bounded textual projection paired with one image part.
// Keeping the alias public lets callers inspect the contract without having
// to know which capture implementation produced it.
type ScreenResult = sight.Result

// ScreenToolErrorResult creates the pixel-free result sent when the screen
// boundary is denied, unavailable, canceled, or otherwise fails. The direct
// ScreenTool contract still returns the original typed Go error; session
// adapters use this envelope when they need to keep the session alive.
func ScreenToolErrorResult(err error) string {
	result := sight.NewError(sight.SourceScreen, err)
	var captureErr *ScreenCaptureError
	if errors.As(err, &captureErr) && captureErr != nil && captureErr.State != "" {
		if captureErr.State == ScreenCaptureDenied {
			result.ErrorCode = ScreenRecordingPermissionDeniedErrorCode
		} else {
			result.ErrorCode = string(captureErr.State)
		}
	}
	if errors.Is(err, ErrScreenRecordingPermissionDenied) {
		result.ErrorCode = ScreenRecordingPermissionDeniedErrorCode
		if !strings.Contains(result.Error, "System Settings → Privacy & Security → Screen & System Audio Recording") {
			result.Error = strings.TrimSpace(strings.Join([]string{result.Error, screenRecordingPermissionGuidance()}, " "))
		}
	}
	encoded, encodeErr := sight.Encode(result)
	if encodeErr != nil {
		return `{"version":2,"status":"error","source":"screen","error_code":"capture_failed","error":"image capture failed"}`
	}
	return string(encoded)
}

// ScreenRecordingValidationError describes one invalid record argument. It is
// returned before display admission or capture side effects take place.
type ScreenRecordingValidationError struct {
	Field  string
	Value  string
	Reason string
}

// ScreenRecordingArgumentError is a descriptive alias for callers that use
// argument-validation terminology.
type ScreenRecordingArgumentError = ScreenRecordingValidationError

func (e *ScreenRecordingValidationError) Error() string {
	if e == nil {
		return ErrInvalidScreenRecording.Error()
	}
	field := strings.TrimSpace(e.Field)
	if field == "" {
		field = "recording"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "the value is not supported"
	}
	value := strings.TrimSpace(e.Value)
	if value == "" {
		return fmt.Sprintf("invalid screen recording option %q: %s", field, reason)
	}
	return fmt.Sprintf("invalid screen recording option %q (%s): %s", field, value, reason)
}

func (e *ScreenRecordingValidationError) Unwrap() error { return ErrInvalidScreenRecording }

// ScreenRecordingEncoder is the context-aware GIF encoding boundary used by
// record. The default implementation is the standard library encoder; the
// seam lets deterministic tests model cancellation during encoding.
type ScreenRecordingEncoder interface {
	Encode(context.Context, io.Writer, *gif.GIF) error
}

// ScreenRecordingEncoderFunc adapts a function to ScreenRecordingEncoder.
type ScreenRecordingEncoderFunc func(context.Context, io.Writer, *gif.GIF) error

func (f ScreenRecordingEncoderFunc) Encode(ctx context.Context, w io.Writer, recording *gif.GIF) error {
	if f == nil {
		return errors.New("screen recording encoder is not configured")
	}
	return f(ctx, w, recording)
}

type standardScreenRecordingEncoder struct{}

func (standardScreenRecordingEncoder) Encode(ctx context.Context, w io.Writer, recording *gif.GIF) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return gif.EncodeAll(w, recording)
}

// ScreenToolOptions configures the display and recording boundaries used by
// ScreenTool. The encoder is optional and defaults to GIF encoding.
type ScreenToolOptions struct {
	DisplaySurface   DisplaySurface
	RecordingEncoder ScreenRecordingEncoder
}

func NewScreenTool() *ScreenTool {
	return NewScreenToolWithOptions(ScreenToolOptions{DisplaySurface: NewHostDisplaySurface()})
}

// NewScreenToolWithDisplaySurface injects the platform boundary used for
// display admission, geometry, and image capture.
func NewScreenToolWithDisplaySurface(surface DisplaySurface) *ScreenTool {
	return NewScreenToolWithOptions(ScreenToolOptions{DisplaySurface: surface})
}

// NewScreenToolWithOptions injects the platform display surface and optional
// recording encoder. It is intended for composed sessions and hermetic tests.
func NewScreenToolWithOptions(options ScreenToolOptions) *ScreenTool {
	surface := options.DisplaySurface
	if surface == nil {
		surface = NewHostDisplaySurface()
	}
	encoder := options.RecordingEncoder
	if encoder == nil {
		encoder = standardScreenRecordingEncoder{}
	}
	return &ScreenTool{surface: surface, recordEncoder: encoder}
}

func (t *ScreenTool) Name() string { return ScreenToolID }

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
				"description": "Recording duration in seconds (1–5). Only used with 'record'. Defaults to 3.",
				"minimum":     minScreenRecordingDurationSeconds,
				"maximum":     maxScreenRecordingDurationSeconds,
			},
			"fps": map[string]any{
				"type":        "number",
				"description": "Frames per second for recording (1–2). Only used with 'record'. Defaults to 2.",
				"minimum":     minScreenRecordingFPS,
				"maximum":     maxScreenRecordingFPS,
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
		parsed, err := parseDisplayIndex(d)
		if err != nil {
			return nil, err
		}
		display = parsed
	} else if raw, present := args["display"]; present {
		d, err := screenNumberArgument(raw, "display")
		if err != nil {
			return nil, err
		}
		parsed, err := parseDisplayIndex(d)
		if err != nil {
			return nil, err
		}
		display = parsed
	}

	var recordingOptions screenRecordingOptions
	if action == "record" {
		var err error
		recordingOptions, err = parseScreenRecordingOptions(args)
		if err != nil {
			return nil, err
		}
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
		return t.recordScreenWithOptions(operationCtx, display, recordingOptions)
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
	decoded, _, err := image.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil || decoded.Bounds().Dx() <= 0 || decoded.Bounds().Dy() <= 0 {
		if err == nil {
			err = errors.New("encoded screenshot has invalid dimensions")
		}
		return nil, newScreenCaptureError("show encode", "screenshot result could not be validated", err)
	}
	msg, err := screenImageMessage(sight.SourceScreen, "image/jpeg", buf.Bytes(), decoded.Bounds().Dx(), decoded.Bounds().Dy())
	if err != nil {
		return nil, newScreenCaptureError("show encode", "screenshot result metadata could not be created", err)
	}
	return []messages.Message{msg}, nil
}

func (t *ScreenTool) recordScreen(ctx context.Context, display int, duration, fps float64) ([]messages.Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, newScreenCaptureError("show recording", "no frames captured: recording did not start", err)
	}
	options, err := newScreenRecordingOptions(duration, fps)
	if err != nil {
		return nil, err
	}
	return t.recordScreenWithOptions(ctx, display, options)
}

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
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, newScreenCaptureError("show recording", "no frames captured: recording did not start", err)
	}
	bounds, err := t.displaySurface().Bounds(ctx, display)
	if err != nil {
		return nil, newScreenCaptureError("show recording geometry", "display geometry is unavailable", err)
	}

	var frames []*image.Paletted
	var delays []int
	startedAt := time.Now()

	for i := 0; i < options.maxFrames; i++ {
		if i > 0 {
			target := startedAt.Add(time.Duration(i) * options.frameInterval)
			if err := waitForScreenRecordingFrame(ctx, target); err != nil {
				return nil, screenRecordingContextError("show recording wait", "screen recording stopped before the next frame", err)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, screenRecordingContextError("show recording", "screen recording stopped before frame capture", err)
		}

		img, err := t.captureDisplay(ctx, display, bounds)
		if err != nil {
			return nil, newScreenCaptureError(fmt.Sprintf("show recording frame %d", i), "screen recording frame capture failed", err)
		}
		if img == nil || img.Bounds().Empty() {
			return nil, newScreenCaptureError(fmt.Sprintf("show recording frame %d", i), "screen recording returned empty image pixels", errors.New("empty image pixels"))
		}

		palImg, err := palettizeScreenFrame(ctx, img)
		if err != nil {
			return nil, screenRecordingContextError(fmt.Sprintf("show recording frame %d", i), "screen recording frame conversion did not finish", err)
		}
		frames = append(frames, palImg)
		delays = append(delays, options.delayCS)
	}

	if len(frames) == 0 {
		return nil, newScreenCaptureError("show recording", "no frames captured", errors.New("no frames captured"))
	}
	if err := ctx.Err(); err != nil {
		return nil, screenRecordingContextError("show recording encode", "screen recording stopped before encoding", err)
	}

	var buf bytes.Buffer
	encoder := t.recordingEncoder()
	recording := &gif.GIF{Image: frames, Delay: delays}
	if err := encoder.Encode(ctx, contextAwareScreenWriter{ctx: ctx, writer: &buf}, recording); err != nil {
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
	decoded, err := gif.DecodeAll(contextReader{ctx: ctx, r: bytes.NewReader(encoded)})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, screenRecordingContextError("show recording result", "screen recording validation did not finish", ctxErr)
		}
		return nil, newScreenCaptureError("show recording result", "screen recording encoder returned an undecodable GIF", err)
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
