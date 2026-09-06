package display

import (
	"context"
	"errors"
	"fmt"
	"strings"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type screenRequest struct {
	action           string
	display          int
	recordingOptions screenRecordingOptions
}

const (
	screenActionScreenshot = "screenshot"
	screenActionRecord     = "record"
	screenCaptureCommand   = "screencapture"
)

func parseScreenRequest(args map[string]any) (screenRequest, error) {
	action, ok := args["action"].(string)
	if !ok {
		action = ""
	}
	if action == "" {
		action = screenActionScreenshot
	}
	if action != screenActionScreenshot && action != screenActionRecord {
		return screenRequest{}, fmt.Errorf("unknown action %q: use 'screenshot' or 'record'", action)
	}
	display, err := parseScreenDisplay(args)
	if err != nil {
		return screenRequest{}, err
	}
	request := screenRequest{action: action, display: display}
	if action == screenActionRecord {
		request.recordingOptions, err = parseScreenRecordingOptions(args)
		if err != nil {
			return screenRequest{}, err
		}
	}
	return request, nil
}

func parseScreenDisplay(args map[string]any) (int, error) {
	raw, present := args["display"]
	if !present {
		return 0, nil
	}
	displayValue, err := screenNumberArgument(raw, "display")
	if err != nil {
		return 0, err
	}
	return parseDisplayIndex(displayValue)
}

func screenOperationContext(ctx context.Context, action string) (context.Context, context.CancelFunc) {
	if action == screenActionScreenshot {
		return boundedScreenContext(ctx, screenScreenshotBound)
	}
	return ctx, func() {}
}

func admitScreenCapture(ctx context.Context, surface DisplaySurface, display int) error {
	capability, probeErr := surface.Probe(ctx)
	if probeErr != nil || !capability.Usable() {
		reason := capability.Reason
		if display > 0 {
			reason = strings.TrimSpace(strings.Join([]string{fmt.Sprintf("display %d is not available", display), reason}, ": "))
		}
		capability.Reason = reason
		return displayUnavailableForCapability("show", capability, probeErr)
	}
	if display >= capability.DisplayCount {
		return &ScreenCaptureError{
			State: ScreenCaptureUnavailable, Operation: "show",
			Reason: fmt.Sprintf("display %d not available (only %d display(s) found)", display, capability.DisplayCount),
		}
	}
	return nil
}

type ScreenCaptureState = public.ScreenCaptureState

const (
	ScreenCaptureGranted          = public.ScreenCaptureGranted
	ScreenCaptureDenied           = public.ScreenCaptureDenied
	ScreenCaptureUnavailable      = public.ScreenCaptureUnavailable
	ScreenCaptureCanceled         = public.ScreenCaptureCanceled
	ScreenCaptureTimedOut         = public.ScreenCaptureTimedOut
	ScreenCaptureFailed           = public.ScreenCaptureFailed
	ScreenCapturePermissionDenied = public.ScreenCapturePermissionDenied
	ScreenCaptureTimeout          = public.ScreenCaptureTimeout
	ScreenCaptureCancelled        = public.ScreenCaptureCancelled
)

// DisplayCapabilityState is retained as an alias for callers that only need
// to inspect the admission state of a display surface.
type DisplayCapabilityState = ScreenCaptureState

const (
	DisplayCapabilityUsable      = ScreenCaptureGranted
	DisplayCapabilityUnavailable = ScreenCaptureUnavailable
)

var (
	// ErrScreenCapture is the stable identity for every classified capture
	// failure. More specific state sentinels are joined by ScreenCaptureError.
	ErrScreenCapture = errors.New("screen capture failed")
	// ErrScreenRecordingPermissionDenied identifies macOS TCC denial.
	ErrScreenRecordingPermissionDenied = errors.New("screen recording permission denied")
	// ErrScreenCapturePermissionDenied is the descriptive compatibility alias.
	ErrScreenCapturePermissionDenied = ErrScreenRecordingPermissionDenied
	ErrDisplayUnavailable            = errors.New("display surface unavailable")
	ErrScreenCaptureCanceled         = errors.New("screen capture canceled")
	ErrScreenCaptureTimedOut         = errors.New("screen capture timed out")
	ErrScreenCaptureFailed           = errors.New("screen capture command failed")
)

// DisplayCapability is the side-effect-free admission snapshot for the
// display-dependent tool. On macOS, Probe runs the non-prompting Screen
// Recording preflight before it asks the host for display metadata.
type DisplayCapability = public.DisplayCapability

// UsableDisplayCapability constructs a normalized positive capability.
func UsableDisplayCapability(displayCount int) DisplayCapability {
	if displayCount < 0 {
		displayCount = 0
	}
	return DisplayCapability{State: ScreenCaptureGranted, Available: displayCount > 0, DisplayCount: displayCount}
}

// UnavailableDisplayCapability constructs a normalized failed capability.
func UnavailableDisplayCapability(reason string) DisplayCapability {
	return DisplayCapability{State: ScreenCaptureUnavailable, Reason: reason}
}

// ScreenCaptureError is a stable, inspectable error for display failures.
// Reason is safe for model-facing output and Cause retains context/process
// identity for programmatic callers.
type ScreenCaptureError struct {
	State     ScreenCaptureState
	Operation string
	Reason    string
	Cause     error
}

// DisplayUnavailableError is the historical name for a typed display
// capture failure. It remains an alias so callers can migrate to the richer
// state vocabulary without losing errors.As compatibility.
type DisplayUnavailableError = ScreenCaptureError

func (e *ScreenCaptureError) Error() string {
	if e == nil {
		return ErrScreenCapture.Error()
	}
	operation := e.Operation
	if operation == "" {
		operation = "display capture"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" && e.Cause != nil {
		reason = e.Cause.Error()
	}
	if reason == "" {
		reason = "the display capture boundary did not produce an image"
	}
	if e.State == ScreenCaptureDenied {
		reason = strings.TrimSpace(strings.Join([]string{reason, screenRecordingPermissionGuidance()}, " "))
	}
	return fmt.Sprintf("%s (%s): %s", operation, e.State, reason)
}

func (e *ScreenCaptureError) Unwrap() error {
	if e == nil {
		return ErrScreenCapture
	}
	errs := []error{ErrScreenCapture}
	switch e.State {
	case ScreenCaptureGranted:
		// A successful capability cannot carry a capture failure sentinel.
	case ScreenCaptureDenied:
		errs = append(errs, ErrScreenRecordingPermissionDenied)
	case ScreenCaptureUnavailable:
		errs = append(errs, ErrDisplayUnavailable)
	case ScreenCaptureCanceled:
		errs = append(errs, ErrScreenCaptureCanceled)
	case ScreenCaptureTimedOut:
		errs = append(errs, ErrScreenCaptureTimedOut)
	case ScreenCaptureFailed:
		errs = append(errs, ErrScreenCaptureFailed)
	}
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}
	return errors.Join(errs...)
}

// ScreenRecordingPermissionError is used by the macOS process seam when the
// screencapture command reports a TCC denial. The higher-level tool adds the
// actionable System Settings guidance while retaining the raw safe detail.
type ScreenRecordingPermissionError struct {
	Detail string
	Cause  error
}

func (e *ScreenRecordingPermissionError) Error() string {
	if e == nil || strings.TrimSpace(e.Detail) == "" {
		return ErrScreenRecordingPermissionDenied.Error()
	}
	return fmt.Sprintf("%s: %s", ErrScreenRecordingPermissionDenied, strings.TrimSpace(e.Detail))
}

func (e *ScreenRecordingPermissionError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrScreenRecordingPermissionDenied
	}
	return errors.Join(ErrScreenRecordingPermissionDenied, e.Cause)
}

// DisplayCapabilityProbe is the admission-only portion of a display surface.
// Probe must not capture or persist screen content.
type DisplayCapabilityProbe = public.DisplayCapabilityProbe

// DisplayCapabilityProbeFunc adapts a function to the admission probe seam.
type DisplayCapabilityProbeFunc func(context.Context) (DisplayCapability, error)

func (f DisplayCapabilityProbeFunc) Probe(ctx context.Context) (DisplayCapability, error) {
	if f == nil {
		return UnavailableDisplayCapability("display capability probe is not configured"), nil
	}
	return f(ctx)
}

// DisplayPermissionState and DisplayPermission are aliases of the neutral
// tools contract. Platform display code can therefore be surfaced through a
// composed runtime executor without importing the CLI display package.
type DisplayPermissionState = public.DisplayPermissionState

const (
	DisplayPermissionGranted     = public.DisplayPermissionGranted
	DisplayPermissionDenied      = public.DisplayPermissionDenied
	DisplayPermissionUnavailable = public.DisplayPermissionUnavailable
	DisplayPermissionCanceled    = public.DisplayPermissionCanceled
	DisplayPermissionTimedOut    = public.DisplayPermissionTimedOut
	DisplayPermissionFailed      = public.DisplayPermissionFailed
)

type DisplayPermission = public.DisplayPermission

type DisplayPermissionChecker interface {
	Check(context.Context) (DisplayPermission, error)
}

type DisplayPermissionCheckerFunc func(context.Context) (DisplayPermission, error)

func (f DisplayPermissionCheckerFunc) Check(ctx context.Context) (DisplayPermission, error) {
	if f == nil {
		return DisplayPermission{State: DisplayPermissionGranted}, nil
	}
	return f(ctx)
}

// ScreenRecordingPermissionRechecker is the optional session boundary used to
// inspect a macOS permission state after an interactive screen call times out.
// The recheck is deliberately separate from DisplaySurface so non-screen tools
// and non-macOS surfaces do not acquire timeout-specific behavior by accident.
type ScreenRecordingPermissionRechecker interface {
	ScreenRecordingPermissionRecheckSupported() bool
	RecheckScreenRecordingPermission(context.Context) (DisplayPermission, error)
}

// DisplayProcess is the narrow subprocess boundary used by command-based
// platforms. Run must honor ctx; production uses exec.CommandContext.
type DisplayProcess interface {
	Run(context.Context, string, ...string) ([]byte, error)
	LookPath(string) (string, error)
}

func normalizeDisplayProcess(process DisplayProcess) DisplayProcess {
	if process == nil {
		return defaultDisplayProcess()
	}
	return process
}
