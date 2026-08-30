package tools

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ScreenCaptureState is the bounded outcome vocabulary for a display capture
// attempt. A successful capture has state granted; all other states are
// surfaced as typed errors and never as image-looking fallback content.
type ScreenCaptureState string

const (
	ScreenCaptureGranted          ScreenCaptureState = "granted"
	ScreenCaptureDenied           ScreenCaptureState = "denied"
	ScreenCaptureUnavailable      ScreenCaptureState = "unavailable"
	ScreenCaptureCanceled         ScreenCaptureState = "canceled"
	ScreenCaptureTimedOut         ScreenCaptureState = "timed_out"
	ScreenCaptureFailed           ScreenCaptureState = "failed"
	ScreenCapturePermissionDenied ScreenCaptureState = ScreenCaptureDenied
	ScreenCaptureTimeout          ScreenCaptureState = ScreenCaptureTimedOut
	ScreenCaptureCancelled        ScreenCaptureState = ScreenCaptureCanceled
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
// display-dependent tool. The capture permission check is allowed to be
// deferred until the actual capture on macOS because TCC's authoritative
// signal is the screencapture operation itself.
type DisplayCapability struct {
	State        ScreenCaptureState
	Available    bool
	DisplayCount int
	Reason       string
}

// Usable reports whether the snapshot proves at least one usable display.
// The empty state is accepted for old injected test doubles that explicitly
// set Available and DisplayCount; production snapshots always set State.
func (c DisplayCapability) Usable() bool {
	return c.Available && c.DisplayCount > 0 && (c.State == "" || c.State == ScreenCaptureGranted)
}

// UsableDisplayCapability constructs a normalized positive capability.
func UsableDisplayCapability(displayCount int) DisplayCapability {
	if displayCount < 0 {
		displayCount = 0
	}
	return DisplayCapability{
		State:        ScreenCaptureGranted,
		Available:    displayCount > 0,
		DisplayCount: displayCount,
	}
}

// UnavailableDisplayCapability constructs a normalized failed capability.
func UnavailableDisplayCapability(reason string) DisplayCapability {
	return DisplayCapability{
		State:     ScreenCaptureUnavailable,
		Available: false,
		Reason:    reason,
	}
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

// DisplayCapabilityProbe is the admission-only portion of a display surface.
// Probe must not capture or persist screen content.
type DisplayCapabilityProbe interface {
	Probe(context.Context) (DisplayCapability, error)
}

// DisplayCapabilityProbeFunc adapts a function to the admission probe seam.
type DisplayCapabilityProbeFunc func(context.Context) (DisplayCapability, error)

func (f DisplayCapabilityProbeFunc) Probe(ctx context.Context) (DisplayCapability, error) {
	if f == nil {
		return UnavailableDisplayCapability("display capability probe is not configured"), nil
	}
	return f(ctx)
}

// DisplayPermissionState describes an optional preflight permission result.
// On macOS the default production path leaves this seam nil and classifies
// TCC from the authoritative screencapture result instead of guessing.
type DisplayPermissionState string

const (
	DisplayPermissionGranted     DisplayPermissionState = "granted"
	DisplayPermissionDenied      DisplayPermissionState = "denied"
	DisplayPermissionUnavailable DisplayPermissionState = "unavailable"
	DisplayPermissionCanceled    DisplayPermissionState = "canceled"
	DisplayPermissionTimedOut    DisplayPermissionState = "timed_out"
	DisplayPermissionFailed      DisplayPermissionState = "failed"
)

type DisplayPermission struct {
	State  DisplayPermissionState
	Reason string
}

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

// DisplayProcess is the narrow subprocess boundary used by command-based
// platforms. Run must honor ctx; production uses exec.CommandContext.
type DisplayProcess interface {
	Run(context.Context, string, ...string) ([]byte, error)
	LookPath(string) (string, error)
}

type osDisplayProcess struct{}

func (osDisplayProcess) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (osDisplayProcess) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func defaultDisplayProcess() DisplayProcess { return osDisplayProcess{} }

// DisplayProcessAdapter is a deterministic process seam for platform tests.
type DisplayProcessAdapter struct {
	RunFunc      func(context.Context, string, ...string) ([]byte, error)
	LookPathFunc func(string) (string, error)
}

func (p DisplayProcessAdapter) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.RunFunc == nil {
		return nil, fmt.Errorf("display process runner is not configured")
	}
	return p.RunFunc(ctx, name, args...)
}

func (p DisplayProcessAdapter) LookPath(file string) (string, error) {
	if p.LookPathFunc == nil {
		return file, nil
	}
	return p.LookPathFunc(file)
}

// DisplayCapturer is the injectable image-producing boundary. The display
// index is preserved so macOS can use screencapture -D without reconstructing
// a region from a second frame.
type DisplayCapturer interface {
	Capture(context.Context, int, image.Rectangle) (*image.RGBA, error)
}

type DisplayCapturerFunc func(context.Context, int, image.Rectangle) (*image.RGBA, error)

func (f DisplayCapturerFunc) Capture(ctx context.Context, display int, bounds image.Rectangle) (*image.RGBA, error) {
	if f == nil {
		return nil, errors.New("display capturer is not configured")
	}
	return f(ctx, display, bounds)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := r.r.Read(p)
	if r.ctx != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, err
}

// DisplaySurface is the process/platform boundary used by ScreenTool.
type DisplaySurface interface {
	DisplayCapabilityProbe
	DisplayCount(context.Context) (int, error)
	Bounds(context.Context, int) (image.Rectangle, error)
	Capture(context.Context, image.Rectangle) (*image.RGBA, error)
}

type indexedDisplaySurface interface {
	CaptureDisplay(context.Context, int, image.Rectangle) (*image.RGBA, error)
}

// HostDisplaySurfaceOptions configures the production boundary and its
// hermetic permission/process/capturer seams.
type HostDisplaySurfaceOptions struct {
	Process           DisplayProcess
	PermissionChecker DisplayPermissionChecker
	Capturer          DisplayCapturer
}

// DisplaySurfaceOptions is a shorter compatibility alias for the options
// used by NewHostDisplaySurfaceWithOptions.
type DisplaySurfaceOptions = HostDisplaySurfaceOptions

type hostDisplaySurface struct {
	process    DisplayProcess
	permission DisplayPermissionChecker
	capturer   DisplayCapturer
}

// NewHostDisplaySurface returns the platform display boundary. The optional
// process parameter keeps the original constructor convenient for tests.
func NewHostDisplaySurface(process ...DisplayProcess) DisplaySurface {
	options := HostDisplaySurfaceOptions{}
	if len(process) > 0 {
		options.Process = process[0]
	}
	return NewHostDisplaySurfaceWithOptions(options)
}

func NewHostDisplaySurfaceWithOptions(options HostDisplaySurfaceOptions) DisplaySurface {
	process := options.Process
	if process == nil {
		process = defaultDisplayProcess()
	}
	return &hostDisplaySurface{
		process:    process,
		permission: options.PermissionChecker,
		capturer:   options.Capturer,
	}
}

func (s *hostDisplaySurface) Probe(ctx context.Context) (DisplayCapability, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return capabilityForScreenError(err, "display capability check was canceled"), &ScreenCaptureError{
			State: ScreenCaptureCanceled, Operation: "display capability check", Reason: "display capability check was canceled", Cause: err,
		}
	}
	if s.permission != nil {
		permission, err := s.permission.Check(ctx)
		if err != nil {
			return capabilityForScreenError(err, "screen recording permission check failed"), newScreenCaptureError("screen recording permission check", "", err)
		}
		if permission.State != "" && permission.State != DisplayPermissionGranted {
			state := screenCaptureStateForPermission(permission.State)
			capability := DisplayCapability{State: state, Reason: permission.Reason}
			if capability.Reason == "" {
				capability.Reason = "the display permission check did not grant access"
			}
			return capability, &ScreenCaptureError{State: state, Operation: "screen recording permission check", Reason: capability.Reason}
		}
	}

	count, bounds, err := screenDisplayInfoWithContextAndProcess(ctx, s.process)
	if err != nil {
		state := classifyScreenCaptureState(err)
		if state == ScreenCaptureGranted || state == ScreenCaptureFailed {
			state = ScreenCaptureUnavailable
		}
		capability := DisplayCapability{State: state, Reason: "display discovery is unavailable"}
		return capability, &ScreenCaptureError{State: state, Operation: "display discovery", Reason: capability.Reason, Cause: err}
	}
	if count <= 0 {
		capability := UnavailableDisplayCapability("no usable display was discovered")
		return capability, &ScreenCaptureError{State: ScreenCaptureUnavailable, Operation: "display discovery", Reason: capability.Reason}
	}
	if err := screenCapturePrerequisitesWithContextAndProcess(ctx, s.process); err != nil {
		state := classifyScreenCaptureState(err)
		if state == ScreenCaptureGranted || state == ScreenCaptureFailed {
			state = ScreenCaptureUnavailable
		}
		capability := DisplayCapability{State: state, Reason: "the screen capture command is unavailable"}
		return capability, &ScreenCaptureError{State: state, Operation: "screen capture admission", Reason: capability.Reason, Cause: err}
	}
	if bounds.Empty() {
		capability := UnavailableDisplayCapability("display geometry is empty")
		return capability, &ScreenCaptureError{State: ScreenCaptureUnavailable, Operation: "display geometry", Reason: capability.Reason}
	}
	return UsableDisplayCapability(count), nil
}

func (s *hostDisplaySurface) DisplayCount(ctx context.Context) (int, error) {
	return screenDisplayCountWithContextAndProcess(ctx, s.process)
}

func (s *hostDisplaySurface) Bounds(ctx context.Context, display int) (image.Rectangle, error) {
	return screenDisplayBoundsWithContextAndProcess(ctx, display, s.process)
}

func (s *hostDisplaySurface) Capture(ctx context.Context, bounds image.Rectangle) (*image.RGBA, error) {
	return s.CaptureDisplay(ctx, 0, bounds)
}

func (s *hostDisplaySurface) CaptureDisplay(ctx context.Context, display int, bounds image.Rectangle) (*image.RGBA, error) {
	if s.capturer != nil {
		return s.capturer.Capture(ctx, display, bounds)
	}
	return screenCaptureDisplayWithContextAndProcess(ctx, display, bounds, s.process)
}

// The legacy helper names remain available to platform tests and direct tool
// callers. They retain their old fallback shape for compatibility, while the
// session-facing boundary above returns typed failures instead of inventing a
// display or image.
func screenDisplayCount() int {
	count, err := screenDisplayCountWithContext(context.Background())
	if err != nil || count <= 0 {
		return 1
	}
	return count
}

func screenDisplayCountWithContext(ctx context.Context) (int, error) {
	return screenDisplayCountWithContextAndProcess(ctx, defaultDisplayProcess())
}

func screenDisplayBounds(idx int) image.Rectangle {
	bounds, err := screenDisplayBoundsWithContext(context.Background(), idx)
	if err != nil || bounds.Empty() {
		return image.Rect(0, 0, 1920, 1080)
	}
	return bounds
}

func screenDisplayBoundsWithContext(ctx context.Context, idx int) (image.Rectangle, error) {
	return screenDisplayBoundsWithContextAndProcess(ctx, idx, defaultDisplayProcess())
}

func screenCapture(bounds image.Rectangle) (*image.RGBA, error) {
	return screenCaptureWithContext(context.Background(), bounds)
}

func screenCaptureWithContext(ctx context.Context, bounds image.Rectangle) (*image.RGBA, error) {
	return screenCaptureWithContextAndProcess(ctx, bounds, defaultDisplayProcess())
}

func normalizeDisplayProcess(process DisplayProcess) DisplayProcess {
	if process == nil {
		return defaultDisplayProcess()
	}
	return process
}

func screenCaptureStateForPermission(state DisplayPermissionState) ScreenCaptureState {
	switch state {
	case DisplayPermissionDenied:
		return ScreenCaptureDenied
	case DisplayPermissionUnavailable:
		return ScreenCaptureUnavailable
	case DisplayPermissionCanceled:
		return ScreenCaptureCanceled
	case DisplayPermissionTimedOut:
		return ScreenCaptureTimedOut
	default:
		return ScreenCaptureFailed
	}
}

func classifyScreenCaptureState(err error) ScreenCaptureState {
	if err == nil {
		return ScreenCaptureGranted
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrScreenCaptureCanceled) {
		return ScreenCaptureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrScreenCaptureTimedOut) {
		return ScreenCaptureTimedOut
	}
	if errors.Is(err, ErrScreenRecordingPermissionDenied) || screenRecordingPermissionText(err.Error()) {
		return ScreenCaptureDenied
	}
	if errors.Is(err, ErrDisplayUnavailable) || errors.Is(err, exec.ErrNotFound) {
		return ScreenCaptureUnavailable
	}
	return ScreenCaptureFailed
}

func capabilityForScreenError(err error, fallback string) DisplayCapability {
	state := classifyScreenCaptureState(err)
	if state == ScreenCaptureGranted {
		state = ScreenCaptureUnavailable
	}
	return DisplayCapability{State: state, Reason: fallback}
}

func newScreenCaptureError(operation, reason string, cause error) *ScreenCaptureError {
	if reason == "" && cause != nil {
		var existing *ScreenCaptureError
		if errors.As(cause, &existing) && existing != nil {
			return existing
		}
	}
	state := classifyScreenCaptureState(cause)
	if state == ScreenCaptureGranted {
		state = ScreenCaptureFailed
	}
	return &ScreenCaptureError{State: state, Operation: operation, Reason: reason, Cause: cause}
}

func displayUnavailableForCapability(operation string, capability DisplayCapability, cause error) error {
	if cause != nil {
		return newScreenCaptureError(operation, capability.Reason, cause)
	}
	state := capability.State
	if state == "" || state == ScreenCaptureGranted {
		state = ScreenCaptureUnavailable
	}
	reason := capability.Reason
	if reason == "" {
		reason = "no usable display or capture surface is available"
	}
	return &ScreenCaptureError{State: state, Operation: operation, Reason: reason}
}

func screenRecordingPermissionText(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"screen recording",
		"not authorized",
		"not authorised",
		"operation not permitted",
		"permission denied",
		"tcc",
		"could not create image from display",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isScreenRecordingPermissionDenied(output []byte, err error) bool {
	if errors.Is(err, ErrScreenRecordingPermissionDenied) {
		return true
	}
	text := strings.TrimSpace(string(output))
	if err != nil {
		text = strings.TrimSpace(strings.Join([]string{text, err.Error()}, " "))
	}
	return screenRecordingPermissionText(text)
}

const screenScreenshotBound = 5 * time.Second

func boundedScreenContext(ctx context.Context, limit time.Duration) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= limit {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, limit)
}

func screenRecordingPermissionGuidance() string {
	host := screenRecordingHostName()
	return fmt.Sprintf(
		"Open System Settings → Privacy & Security → Screen Recording and enable the launching terminal/CLI host %q; quit and restart that host before retrying. The CLI cannot grant Screen Recording permission itself.",
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
