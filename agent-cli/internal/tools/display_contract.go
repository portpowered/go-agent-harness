package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// ScreenCaptureState is the bounded outcome vocabulary for a display capture
// attempt. A successful capture has state granted; all other states are
// surfaced as typed errors and never as image-looking fallback content.
type ScreenCaptureState = runtimeTools.ScreenCaptureState

const (
	ScreenToolID          = runtimeTools.ScreenToolID
	HostDisplayToolID     = runtimeTools.HostDisplayToolID
	PhysicalDisplayToolID = runtimeTools.PhysicalDisplayToolID
	PageSightToolID       = runtimeTools.PageSightToolID

	ScreenRecordingPermissionDeniedErrorCode = runtimeTools.ScreenRecordingPermissionDeniedErrorCode

	ScreenCaptureGranted          = runtimeTools.ScreenCaptureGranted
	ScreenCaptureDenied           = runtimeTools.ScreenCaptureDenied
	ScreenCaptureUnavailable      = runtimeTools.ScreenCaptureUnavailable
	ScreenCaptureCanceled         = runtimeTools.ScreenCaptureCanceled
	ScreenCaptureTimedOut         = runtimeTools.ScreenCaptureTimedOut
	ScreenCaptureFailed           = runtimeTools.ScreenCaptureFailed
	ScreenCapturePermissionDenied = runtimeTools.ScreenCapturePermissionDenied
	ScreenCaptureTimeout          = runtimeTools.ScreenCaptureTimeout
	ScreenCaptureCancelled        = runtimeTools.ScreenCaptureCancelled
)

// ScreenToolErrorResult and ScreenToolSessionErrorResult are the CLI host
// projections of the runtime display error contract. The display capture
// implementation lives in the reusable tools service; these helpers keep
// operator guidance and session-safe text at the composition edge.
func ScreenToolErrorResult(err error) string {
	return encodeScreenToolErrorResult(err, true)
}

func ScreenToolSessionErrorResult(err error) string {
	return encodeScreenToolErrorResult(err, false)
}

func ScreenToolErrorCode(err error) string {
	result := sight.NewError(sight.SourceScreen, err)
	var captureErr *ScreenCaptureError
	if errors.As(err, &captureErr) && captureErr != nil && captureErr.State != "" {
		if captureErr.State == ScreenCaptureDenied {
			return ScreenRecordingPermissionDeniedErrorCode
		}
		return string(captureErr.State)
	}
	if errors.Is(err, ErrScreenRecordingPermissionDenied) {
		return ScreenRecordingPermissionDeniedErrorCode
	}
	return result.ErrorCode
}

func encodeScreenToolErrorResult(err error, includeOperatorGuidance bool) string {
	result := sight.NewError(sight.SourceScreen, err)
	result.ErrorCode = ScreenToolErrorCode(err)
	if !includeOperatorGuidance {
		result.Error = "Screen sight is unavailable."
	}
	if includeOperatorGuidance && errors.Is(err, ErrScreenRecordingPermissionDenied) && !strings.Contains(result.Error, "System Settings → Privacy & Security → Screen & System Audio Recording") {
		result.Error = strings.TrimSpace(strings.Join([]string{result.Error, screenRecordingPermissionGuidance()}, " "))
	}
	encoded, encodeErr := sight.Encode(result)
	if encodeErr != nil {
		return `{"version":2,"status":"error","source":"screen","error_code":"capture_failed","error":"image capture failed"}`
	}
	return string(encoded)
}

// IsPhysicalDisplayToolName identifies calls that may reach a host display.
func IsPhysicalDisplayToolName(name string) bool {
	return name == ScreenToolID || name == HostDisplayToolID
}

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
type DisplayCapability = runtimeTools.DisplayCapability

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

func screenRecordingPermissionGuidance() string {
	host := screenRecordingHostName()
	return fmt.Sprintf(
		"Screen-recording permission is not granted, so I cannot see the screen. Tell the customer to open System Settings → Privacy & Security → Screen & System Audio Recording, enable the hosting application %q, then completely quit and restart that application before asking again. macOS Sequoia may require monthly re-confirmation. The CLI cannot grant this permission itself.",
		host,
	)
}

func screenRecordingHostName() string {
	host := strings.TrimSpace(os.Getenv("TERM_PROGRAM"))
	switch strings.ToLower(host) {
	case "apple_terminal":
		host = "Terminal"
	case "iterm.app":
		host = "iTerm2"
	case "vscode":
		host = "VS Code"
	case "goland", "intellijidea", "pycharm", "rubymine", "webstorm":
		host = "the JetBrains IDE terminal host"
	}
	if host == "" {
		if len(os.Args) > 0 {
			host = filepath.Base(os.Args[0])
		}
	}
	if host == "" || host == "." || host == string(filepath.Separator) {
		host = "launching terminal or CLI host"
	}
	return host
}
