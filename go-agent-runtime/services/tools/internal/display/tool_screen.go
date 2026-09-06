package display

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/sight"
)

// ScreenTool captures the screen as a screenshot or a timed recording.
// The display surface is injected so capture, permission, and cancellation
// behavior can be tested without depending on the host desktop.
type ScreenTool struct {
	surface       DisplaySurface
	recordEncoder ScreenRecordingEncoder
}

const (
	screenJPEGQuality = 85

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
	// capture tool outside a browser-composed session. Browser-composed
	// sessions reserve this legacy name for selected-page sight; the explicit
	// host-display alias below is advertised for physical-display requests.
	ScreenToolID = "show"
	// HostDisplayToolID is the explicit physical-display name used when a
	// browser-backed session also has selected-page sight. Keeping this name
	// distinct prevents a page-content request from silently reaching the host
	// display capture backend.
	HostDisplayToolID = "show_screen"
	// PhysicalDisplayToolID is a descriptive alias for HostDisplayToolID.
	PhysicalDisplayToolID = HostDisplayToolID
	// PageSightToolID is the stable selected-browser-page capture name. It is
	// duplicated here as a provider-neutral composition constant so the tools
	// package does not depend on the WebMCP adapter package.
	PageSightToolID = "show_page"

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
	return encodeScreenToolErrorResult(err, true)
}

// ScreenToolSessionErrorResult creates the customer-safe result sent across
// a live session boundary. It retains the typed source and error code while
// intentionally omitting operator remediation; the original typed Go error
// remains available to the direct tool/logger path.
func ScreenToolSessionErrorResult(err error) string {
	return encodeScreenToolErrorResult(err, false)
}

// ScreenToolErrorCode returns the stable classification used by both the
// typed result envelope and operator diagnostics.
func ScreenToolErrorCode(err error) string {
	return screenErrorCode(err)
}

func encodeScreenToolErrorResult(err error, includeOperatorGuidance bool) string {
	result := sight.NewError(sight.SourceScreen, err)
	result.ErrorCode = screenErrorCode(err)
	if !includeOperatorGuidance {
		result.Error = "Screen sight is unavailable."
	}
	if includeOperatorGuidance && errors.Is(err, ErrScreenRecordingPermissionDenied) {
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

func screenErrorCode(err error) string {
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
	}
	return result.ErrorCode
}

// IsPhysicalDisplayToolName identifies names that can reach the host display
// backend. The generic ScreenToolID remains physical for plain/direct
// sessions; composed browser sessions additionally use HostDisplayToolID.
func IsPhysicalDisplayToolName(name string) bool {
	return name == ScreenToolID || name == HostDisplayToolID
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
				"enum":        []string{screenActionScreenshot, screenActionRecord},
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
		return nil, errors.New("screen capture context is required")
	}
	request, err := parseScreenRequest(args)
	if err != nil {
		return nil, err
	}
	operationCtx, cancel := screenOperationContext(ctx, request.action)
	defer cancel()
	if err := operationCtx.Err(); err != nil {
		return nil, newScreenCaptureError("show", "screen capture did not start", err)
	}
	surface := t.displaySurface()
	if err := admitScreenCapture(operationCtx, surface, request.display); err != nil {
		return nil, err
	}
	return t.executeScreenRequest(operationCtx, request)
}

func (t *ScreenTool) executeScreenRequest(ctx context.Context, request screenRequest) ([]messages.Message, error) {
	if request.action == screenActionScreenshot {
		return t.takeScreenshotWithContext(ctx, request.display)
	}
	return t.recordScreenWithOptions(ctx, request.display, request.recordingOptions)
}

func (t *ScreenTool) takeScreenshotWithContext(ctx context.Context, display int) ([]messages.Message, error) {
	if ctx == nil {
		return nil, errors.New("screen capture context is required")
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
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: screenJPEGQuality}); err != nil {
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
