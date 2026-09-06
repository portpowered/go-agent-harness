// Package tools defines the reusable tool capability boundary.
//
// The package contains value contracts only. Concrete registries, filesystem
// policy, display adapters, and browser implementations live below the
// service's internal boundary and are assembled by services/tools/wire.
package tools

import (
	"context"
	"errors"
	"image"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ScreenCaptureState is the bounded outcome vocabulary for a display
// capability probe or capture attempt. It is a value contract so a host can
// inject its own display surface without importing the runtime's platform
// implementation.
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

// DisplayCapability is the side-effect-free admission snapshot for a
// display-dependent tool. A probe may report a structurally present display
// whose capture permission is currently denied; Advertisable preserves that
// distinction for model-facing tool definitions.
type DisplayCapability struct {
	State        ScreenCaptureState
	Available    bool
	DisplayCount int
	Reason       string
}

func (c DisplayCapability) Usable() bool {
	return c.Available && c.DisplayCount > 0 && (c.State == "" || c.State == ScreenCaptureGranted)
}

func (c DisplayCapability) Advertisable() bool {
	if c.Usable() {
		return true
	}
	return c.State != ScreenCaptureUnavailable && c.State != ""
}

// DisplayCapabilityProbe is the admission-only portion of a display surface.
// Probe must not capture or persist screen content.
type DisplayCapabilityProbe interface {
	Probe(context.Context) (DisplayCapability, error)
}

// DisplaySurface is the host/platform boundary used by physical-display
// tools. Its implementation stays at the composition edge; the runtime only
// consumes this narrow value-oriented contract.
type DisplaySurface interface {
	DisplayCapabilityProbe
	DisplayCount(context.Context) (int, error)
	Bounds(context.Context, int) (image.Rectangle, error)
	Capture(context.Context, image.Rectangle) (*image.RGBA, error)
}

// DisplayPermissionState and DisplayPermission describe a host permission
// check without carrying platform-specific types across the service edge.
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

var (
	// ErrToolNotFound identifies an invocation for a name absent from the
	// resolved capability surface.
	ErrToolNotFound = errors.New("tool not found")
	// ErrToolCompositionCollision identifies an advertised name owned by both
	// the static and browser surfaces.
	ErrToolCompositionCollision = errors.New("tool composition collision")
	// ErrToolCompositionInvalid identifies a surface whose definitions and
	// execution routes cannot form a safe request-scoped capability.
	ErrToolCompositionInvalid = errors.New("invalid tool composition")
	// ErrCapabilityClosePanic identifies a browser cleanup hook that panicked.
	// The service records the failure and lets the owning session continue its
	// remaining finalization work.
	ErrCapabilityClosePanic = errors.New("tool capability cleanup panicked")
	// ErrCapabilityCloseTimeout identifies a browser cleanup hook that did not
	// return before its request-scoped cleanup budget elapsed.
	ErrCapabilityCloseTimeout = errors.New("tool capability cleanup timed out")
)

// DefaultCapabilityCloseTimeout bounds a browser cleanup callback when a host
// does not provide a tighter request-specific budget.
const DefaultCapabilityCloseTimeout = 15 * time.Second

// ToolSelection controls one tool in a request-scoped capability surface.
// Tools absent from a non-empty list remain enabled, matching the CLI config
// semantics; a nil list enables the complete default surface.
type ToolSelection struct {
	ID      string
	Enabled bool
}

// ExecPolicy contains the shell command policy admitted for one surface.
// Filesystem authorization remains separate and is always resolved by the
// service before filesystem tools are constructed.
type ExecPolicy struct {
	EnableDenyPatterns bool
	CustomDenyPatterns []string
	// Configured distinguishes an explicit host policy from the reusable
	// service default. An unconfigured policy uses the built-in deny patterns;
	// hosts that load config set this field even when deny patterns are off.
	Configured bool
}

// Request is the normalized input to a tool capability resolution. Hosts
// resolve paths and configuration before passing this value to the runtime;
// the service does not inspect CLI flags, home directories, or environment
// state to fill missing values.
type Request struct {
	WorkDir    string
	AllowPaths []string
	// DisplaySurface is an optional host-provided physical display boundary.
	// When DisplayCapabilitySet is true, the runtime gates show/mouse
	// definitions using DisplayCapability and binds this surface to them.
	// Leaving it nil lets the reusable service use its platform default.
	DisplaySurface       DisplaySurface
	DisplayCapability    DisplayCapability
	DisplayCapabilitySet bool
	// SkillRoots are ordered directories that directly contain skills. The
	// host resolves these paths; the service does not infer a skills layout
	// from WorkDir, ConfigDir, home, or environment state.
	SkillRoots []SkillRoot
	Selections []ToolSelection
	Exec       ExecPolicy
	Inferencer messages.Inferencer
	Executor   messages.ToolExecutor
	// DiagnosticWriter receives operator-facing tool execution diagnostics.
	// A nil writer is treated as io.Discard by the service.
	DiagnosticWriter io.Writer
	Definitions      []messages.ToolDefinition
	// FilesystemPolicyApplied tells the service that the host has already
	// applied the normalized filesystem policy to Executor. This is useful for
	// host-owned registries: the runtime still records the request scope, but
	// it does not wrap the same executor a second time.
	FilesystemPolicyApplied bool
	// Browser supplies an optional request-scoped browser tool surface. The
	// browser implementation remains host-owned; only this neutral executor,
	// definition, and lifecycle contract crosses into the reusable service.
	Browser *BrowserSurface
	// UseDefaultTool requests construction of the built-in registry when
	// Executor is nil. A false value intentionally produces an empty surface;
	// this is used by embedded hosts that own their complete tool set.
	UseDefaultTool bool
}

// BrowserSurface is the narrow browser boundary accepted by the reusable
// tools service. Hosts own broker discovery, selection, event streams, and
// platform adapters; the runtime owns composition of this surface with static
// tools and the request-scoped lifecycle handle.
//
// Executor and Definitions are a pair. RefreshDefinitions may return live
// page definitions, but it must continue to use the same executor and browser
// capability instance for the request.
type BrowserSurface struct {
	Executor           messages.ToolExecutor
	Definitions        []messages.ToolDefinition
	RefreshDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	Initialize         func(context.Context) error
	Close              func() error
	// CloseTimeout bounds one browser cleanup callback. A zero value uses the
	// tools service default; a non-cooperative host adapter must not hold the
	// session finalizer forever.
	CloseTimeout time.Duration
}

// ScreenRecordingPermissionRechecker is an optional runtime executor
// capability. A host adapter may provide it for the physical display route;
// page sight must never claim this capability.
type ScreenRecordingPermissionRechecker interface {
	ScreenRecordingPermissionRecheckSupported() bool
	RecheckScreenRecordingPermission(context.Context) (DisplayPermission, error)
}

// DynamicToolRouter marks an executor that can resolve live names beyond its
// initial definitions, such as tools discovered from a selected page.
type DynamicToolRouter interface {
	ResolvesDynamicTools() bool
}

// PageSightToolRouter identifies calls routed to selected page sight. Hosts
// use this to keep page calls away from physical display permission handling.
type PageSightToolRouter interface {
	IsPageSightTool(string) bool
}

// DefinitionValidator is implemented by the service's composition owner for
// hosts that need to reject a static/browser name collision before allocating
// a browser adapter. It is separate from Service so lightweight embedders can
// provide only Resolve through the stable service contract.
type DefinitionValidator interface {
	ValidateToolDefinitionNamespaces([]messages.ToolDefinition, []messages.ToolDefinition) error
}

// CapabilityHandle owns resources associated with one resolved capability.
// Browser and display implementations may use Initialize and
// RefreshDefinitions to start or update those resources; every handle must be
// safe to close more than once. Hosts should close the handle when the
// invocation ends.
type CapabilityHandle interface {
	Initialize(context.Context) error
	RefreshDefinitions(context.Context) ([]messages.ToolDefinition, error)
	Close() error
}

// CleanupCoordinator owns cleanup hooks transferred from a host capability
// factory. It is deliberately smaller than CapabilityHandle because session
// finalization may need to coordinate a host-owned hook before a browser
// capability has been fully resolved. The tools service owns the idempotence,
// ordering, panic conversion, and per-hook timeout policy.
type CleanupCoordinator interface {
	Close() error
	IsClosed() bool
}

// Capability is one request-scoped tool surface. Executor and Definitions are
// paired so model-advertised tools cannot drift from invocation routes. Handle
// owns any browser or other request-scoped resources created for this surface.
type Capability struct {
	Executor       messages.ToolExecutor
	Definitions    []messages.ToolDefinition
	WorkspaceDir   string
	AdditionalDirs []string
	Invoker        Invoker
	Handle         CapabilityHandle
}

// Invocation is the transport-neutral shape of one model tool call.
type Invocation struct {
	ID        string
	Name      string
	Arguments string
}

// InvocationResult is the typed result returned by an Invoker.
type InvocationResult struct {
	ID           string
	Content      string
	ContentParts []messages.ContentPart
}

// Invoker executes one admitted invocation. The implementation is owned by
// the tools service; hosts receive this narrow behavior contract instead of a
// concrete registry or tool implementation.
type Invoker interface {
	Invoke(context.Context, Invocation) (InvocationResult, error)
}

// Service resolves request-scoped capability surfaces. Construction of a
// service is inert; tool resources are created only by Resolve.
type Service interface {
	Resolve(context.Context, Request) (Capability, error)
	BuildSkillsSummary(context.Context, SkillSummaryRequest) (string, error)
	BrowserContract() BrowserContract
	NewCleanupCoordinator(...func() error) CleanupCoordinator
	NewCleanupCoordinatorWithTimeout(time.Duration, ...func() error) CleanupCoordinator
}

// ResolverFunc adapts a function to Service for hosts and tests that own
// their complete tool surface.
type ResolverFunc func(context.Context, Request) (Capability, error)

func (f ResolverFunc) Resolve(ctx context.Context, request Request) (Capability, error) {
	if f == nil {
		return Capability{}, errors.New("tool capability resolver is required")
	}
	return f(ctx, request)
}

func (f ResolverFunc) BuildSkillsSummary(context.Context, SkillSummaryRequest) (string, error) {
	return "", errors.New("skill summary is unavailable for function resolver")
}
