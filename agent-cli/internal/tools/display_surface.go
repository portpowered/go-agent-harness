package tools

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strings"

	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// DisplayCapabilityProbe is the admission-only portion of a display surface.
// Probe must not capture or persist screen content.
type DisplayCapabilityProbe = runtimeTools.DisplayCapabilityProbe

// DisplayCapabilityProbeFunc adapts a function to the admission probe seam.
type DisplayCapabilityProbeFunc func(context.Context) (DisplayCapability, error)

func (f DisplayCapabilityProbeFunc) Probe(ctx context.Context) (DisplayCapability, error) {
	if f == nil {
		return UnavailableDisplayCapability("display capability probe is not configured"), nil
	}
	return f(ctx)
}

// DisplayPermissionState describes a preflight permission result. The
// production macOS implementation obtains it from
// CGPreflightScreenCaptureAccess; other platforms leave this boundary nil.
type DisplayPermissionState = runtimeTools.DisplayPermissionState

const (
	DisplayPermissionGranted     = runtimeTools.DisplayPermissionGranted
	DisplayPermissionDenied      = runtimeTools.DisplayPermissionDenied
	DisplayPermissionUnavailable = runtimeTools.DisplayPermissionUnavailable
	DisplayPermissionCanceled    = runtimeTools.DisplayPermissionCanceled
	DisplayPermissionTimedOut    = runtimeTools.DisplayPermissionTimedOut
	DisplayPermissionFailed      = runtimeTools.DisplayPermissionFailed
)

type DisplayPermission = runtimeTools.DisplayPermission

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
type DisplaySurface = runtimeTools.DisplaySurface

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
	permission := options.PermissionChecker
	if permission == nil {
		permission = defaultDisplayPermissionChecker()
	}
	return &hostDisplaySurface{
		process:    process,
		permission: permission,
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
		permission, err := s.checkScreenRecordingPermission(ctx)
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

func (s *hostDisplaySurface) ScreenRecordingPermissionRecheckSupported() bool {
	return screenRecordingPermissionRecheckSupported()
}

// RecheckScreenRecordingPermission uses the same permission checker as Probe.
// It does not inspect display metadata or start a capture process.
func (s *hostDisplaySurface) RecheckScreenRecordingPermission(ctx context.Context) (DisplayPermission, error) {
	if !s.ScreenRecordingPermissionRecheckSupported() {
		return DisplayPermission{
			State:  DisplayPermissionUnavailable,
			Reason: "macOS Screen Recording permission re-check is unavailable on this platform",
		}, nil
	}
	return s.checkScreenRecordingPermission(ctx)
}

func (s *hostDisplaySurface) checkScreenRecordingPermission(ctx context.Context) (DisplayPermission, error) {
	if s == nil || s.permission == nil {
		return DisplayPermission{State: DisplayPermissionGranted}, nil
	}
	return s.permission.Check(ctx)
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
