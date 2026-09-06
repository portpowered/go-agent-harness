package display

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

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type osDisplayProcess struct{}

func (osDisplayProcess) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("display process context is required")
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
type DisplaySurface = public.DisplaySurface

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
		err := errors.New("display capability context is required")
		return capabilityForScreenError(err, "display capability check requires a context"), newScreenCaptureError("display capability check", "display capability check requires a context", err)
	}
	if err := ctx.Err(); err != nil {
		return capabilityForScreenError(err, "display capability check was canceled"), &ScreenCaptureError{
			State: ScreenCaptureCanceled, Operation: "display capability check", Reason: "display capability check was canceled", Cause: err,
		}
	}
	if capability, err := s.probePermission(ctx); err != nil {
		return capability, err
	}
	count, bounds, err := screenDisplayInfoWithContextAndProcess(ctx, s.process)
	if err != nil {
		return unavailableDisplayProbe("display discovery", "display discovery is unavailable", err)
	}
	if count <= 0 {
		capability := UnavailableDisplayCapability("no usable display was discovered")
		return capability, &ScreenCaptureError{State: ScreenCaptureUnavailable, Operation: "display discovery", Reason: capability.Reason}
	}
	if err := screenCapturePrerequisitesWithContextAndProcess(ctx, s.process); err != nil {
		return unavailableDisplayProbe("screen capture admission", "the screen capture command is unavailable", err)
	}
	if bounds.Empty() {
		capability := UnavailableDisplayCapability("display geometry is empty")
		return capability, &ScreenCaptureError{State: ScreenCaptureUnavailable, Operation: "display geometry", Reason: capability.Reason}
	}
	return UsableDisplayCapability(count), nil
}

func (s *hostDisplaySurface) probePermission(ctx context.Context) (DisplayCapability, error) {
	if s.permission == nil {
		return DisplayCapability{}, nil
	}
	permission, err := s.checkScreenRecordingPermission(ctx)
	if err != nil {
		return capabilityForScreenError(err, "screen recording permission check failed"), newScreenCaptureError("screen recording permission check", "", err)
	}
	if permission.State == "" || permission.State == DisplayPermissionGranted {
		return DisplayCapability{}, nil
	}
	state := screenCaptureStateForPermission(permission.State)
	capability := DisplayCapability{State: state, Reason: permission.Reason}
	if capability.Reason == "" {
		capability.Reason = "the display permission check did not grant access"
	}
	return capability, &ScreenCaptureError{State: state, Operation: "screen recording permission check", Reason: capability.Reason}
}

func unavailableDisplayProbe(operation, reason string, cause error) (DisplayCapability, error) {
	state := classifyScreenCaptureState(cause)
	if state == ScreenCaptureGranted || state == ScreenCaptureFailed {
		state = ScreenCaptureUnavailable
	}
	capability := DisplayCapability{State: state, Reason: reason}
	return capability, &ScreenCaptureError{State: state, Operation: operation, Reason: reason, Cause: cause}
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
