package tools

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os/exec"
)

// DisplayCapabilityState is the result of checking whether the host has a
// display surface that the session can use. Unknown and unavailable are
// intentionally represented as unavailable so a failed probe cannot turn
// into a fictitious advertised capability.
type DisplayCapabilityState string

const (
	DisplayCapabilityUsable      DisplayCapabilityState = "usable"
	DisplayCapabilityUnavailable DisplayCapabilityState = "unavailable"
)

// DisplayCapability is the side-effect-free admission snapshot for the
// display-dependent tools. Available is retained as a convenient boolean for
// callers; State is the explicit serialized vocabulary used by diagnostics.
type DisplayCapability struct {
	State        DisplayCapabilityState
	Available    bool
	DisplayCount int
	Reason       string
}

// Usable reports whether the snapshot proves at least one display exists.
// A zero State is accepted for injected test doubles that predate the typed
// state field, provided they explicitly set Available and DisplayCount.
func (c DisplayCapability) Usable() bool {
	return c.Available && c.DisplayCount > 0 && (c.State == "" || c.State == DisplayCapabilityUsable)
}

// UsableDisplayCapability constructs a normalized positive capability.
func UsableDisplayCapability(displayCount int) DisplayCapability {
	if displayCount < 0 {
		displayCount = 0
	}
	return DisplayCapability{
		State:        DisplayCapabilityUsable,
		Available:    displayCount > 0,
		DisplayCount: displayCount,
	}
}

// UnavailableDisplayCapability constructs a normalized failed capability.
func UnavailableDisplayCapability(reason string) DisplayCapability {
	return DisplayCapability{
		State:     DisplayCapabilityUnavailable,
		Reason:    reason,
		Available: false,
	}
}

// ErrDisplayUnavailable identifies a display-dependent operation that cannot
// run in the current session environment.
var ErrDisplayUnavailable = errors.New("display surface unavailable")

// DisplayUnavailableError preserves a stable error identity while keeping
// the reason safe and useful for a model-facing tool result.
type DisplayUnavailableError struct {
	Operation string
	Reason    string
	Cause     error
}

func (e *DisplayUnavailableError) Error() string {
	if e == nil {
		return ErrDisplayUnavailable.Error()
	}
	operation := e.Operation
	if operation == "" {
		operation = "display operation"
	}
	reason := e.Reason
	if reason == "" && e.Cause != nil {
		reason = e.Cause.Error()
	}
	if reason == "" {
		reason = "no usable display or capture surface is available"
	}
	return fmt.Sprintf("display unavailable for %s: %s", operation, reason)
}

func (e *DisplayUnavailableError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrDisplayUnavailable
	}
	return errors.Join(ErrDisplayUnavailable, e.Cause)
}

// DisplayCapabilityProbe is the admission-only portion of a display surface.
// Probe must not capture or persist screen content.
type DisplayCapabilityProbe interface {
	Probe(context.Context) (DisplayCapability, error)
}

// DisplayCapabilityProbeFunc adapts a function to the admission probe
// boundary, keeping capability resolution easy to inject in hermetic callers.
type DisplayCapabilityProbeFunc func(context.Context) (DisplayCapability, error)

func (f DisplayCapabilityProbeFunc) Probe(ctx context.Context) (DisplayCapability, error) {
	if f == nil {
		return UnavailableDisplayCapability("display capability probe is not configured"), nil
	}
	return f(ctx)
}

// DisplaySurface is the process/platform boundary used by ScreenTool. The
// same boundary supplies the admission probe and the later capture operation,
// which makes hermetic capability-loss and cancellation tests possible.
type DisplaySurface interface {
	DisplayCapabilityProbe
	DisplayCount(context.Context) (int, error)
	Bounds(context.Context, int) (image.Rectangle, error)
	Capture(context.Context, image.Rectangle) (*image.RGBA, error)
}

// DisplayProcess is the narrow subprocess boundary used by platforms whose
// screenshot APIs are command based. Run must honor ctx; LookPath is used by
// admission to prove that capture can be attempted without taking a frame.
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

// DisplayProcessAdapter is a convenient injected process boundary for
// deterministic tests. LookPathFunc may be omitted when command existence is
// not part of the test being exercised.
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

type hostDisplaySurface struct {
	process DisplayProcess
}

// NewHostDisplaySurface returns the platform display boundary. An optional
// process lets tests replace command execution without changing production
// behavior.
func NewHostDisplaySurface(process ...DisplayProcess) DisplaySurface {
	runner := defaultDisplayProcess()
	if len(process) > 0 && process[0] != nil {
		runner = process[0]
	}
	return &hostDisplaySurface{process: runner}
}

func (s *hostDisplaySurface) Probe(ctx context.Context) (DisplayCapability, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		capability := UnavailableDisplayCapability("display capability check was canceled")
		return capability, &DisplayUnavailableError{Operation: "display capability check", Reason: capability.Reason, Cause: err}
	}
	count, bounds, err := screenDisplayInfoWithContextAndProcess(ctx, s.process)
	if err != nil {
		capability := UnavailableDisplayCapability("display discovery failed")
		return capability, &DisplayUnavailableError{Operation: "display discovery", Reason: capability.Reason, Cause: err}
	}
	if count <= 0 {
		capability := UnavailableDisplayCapability("no display was discovered")
		return capability, &DisplayUnavailableError{Operation: "display discovery", Reason: capability.Reason}
	}
	if err := screenCapturePrerequisitesWithContextAndProcess(ctx, s.process); err != nil {
		capability := UnavailableDisplayCapability("screen capture is not available")
		return capability, &DisplayUnavailableError{Operation: "screen capture admission", Reason: capability.Reason, Cause: err}
	}
	if bounds.Empty() {
		capability := UnavailableDisplayCapability("display geometry is empty")
		return capability, &DisplayUnavailableError{Operation: "display geometry", Reason: capability.Reason}
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
	return screenCaptureWithContextAndProcess(ctx, bounds, s.process)
}

// The legacy helper names remain available to platform tests and direct tool
// callers, but failures now return zero/empty values instead of inventing a
// display. Session code uses the typed error-returning boundary above.
func screenDisplayCount() int {
	count, _ := screenDisplayCountWithContext(context.Background())
	return count
}

func screenDisplayCountWithContext(ctx context.Context) (int, error) {
	return screenDisplayCountWithContextAndProcess(ctx, defaultDisplayProcess())
}

func screenDisplayBounds(idx int) image.Rectangle {
	bounds, _ := screenDisplayBoundsWithContext(context.Background(), idx)
	return bounds
}

func screenDisplayBoundsWithContext(ctx context.Context, idx int) (image.Rectangle, error) {
	return screenDisplayBoundsWithContextAndProcess(ctx, idx, defaultDisplayProcess())
}
