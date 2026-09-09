// Package replay defines artifact admission and replay planning for hosts.
package replay

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	ErrCaptureUnavailable         errorCode = "replay capture is unavailable"
	ErrBundleIncomplete           errorCode = "replay bundle is incomplete"
	ErrBundleMismatch             errorCode = "replay bundle evidence mismatch"
	ErrToolMismatch               errorCode = "replay tool invocation mismatch"
	ErrToolFailure                errorCode = "recorded tool execution failed"
	ErrDeterministicClockRequired errorCode = "offline replay requires an injected deterministic clock"
	ErrRuntimeFactoryRequired     errorCode = "offline replay runtime factory is required"
)

// CaptureKind identifies the protocol represented by an admitted capture.
// Turn captures are consumed by the ordinary session replay service; realtime
// captures contain provider WebSocket traffic and can drive LiveRunner.
type CaptureKind string

const (
	CaptureKindTurn     CaptureKind = "turn"
	CaptureKindRealtime CaptureKind = "realtime"
)

// CaptureInspection is the replay service's complete admission result for a
// capture path. Hosts consume this typed result instead of opening the file,
// probing JSON payloads, or deriving provider metadata themselves.
type CaptureInspection struct {
	SourcePath       string
	CapturePath      string
	Kind             CaptureKind
	Provider         string
	Model            string
	IntegrityWarning string
	LivePlan         *session.LiveReplayPlan
	// InitialTools describes the recorded initial provider advertisement, not
	// execution authorization. Hosts must retain their current tool policy.
	InitialTools      []string
	InitialToolsKnown bool
}

// IsRealtime reports whether the admitted capture can drive a continuous
// provider session.
func (i CaptureInspection) IsRealtime() bool { return i.Kind == CaptureKindRealtime }

// Service constructs bounded replay actions from explicit capture artifacts.
// Execution and device attachment remain owned by the session service.
type Service interface {
	// InspectCapture validates and classifies a raw capture or finalized
	// recording directory, returning provider metadata and any self-driving
	// live plan. The returned paths are safe for the provider replay adapter.
	InspectCapture(context.Context, string) (CaptureInspection, error)
	LoadLivePlan(context.Context, string) (session.LiveReplayPlan, error)
	// ResolveCapturePath admits either a raw provider capture or a finalized
	// recording directory. Directory admission verifies the manifest, complete
	// status, every declared artifact (including recorded PCM), and the
	// provider artifact path before returning the raw capture path to the
	// provider service.
	ResolveCapturePath(context.Context, string) (string, error)
}

// CaptureAdmission is the narrow admission dependency used by strict replay.
// Keeping it separate from Service lets the strict implementation reuse the
// canonical manifest/path validator without constructing its own Wire graph.
type CaptureAdmission interface {
	ResolveCapturePath(context.Context, string) (string, error)
}

// StrictRequest identifies a canonical finalized recording bundle. Provider
// selects the offline protocol adapter and Model, when supplied, is checked
// against the captured provider handshake. No credentials, device selectors,
// or executable tool factories belong in this request.
type StrictRequest struct {
	BundlePath string
	Provider   string
	Model      string
}

// StrictPrepared is the read-only public view of one hermetic headless
// preparation. The concrete value and completion witness remain private to
// the strict service, so callers cannot construct a prepared value that
// certifies arbitrary fake evidence. Hosts should obtain it from Prepare or
// use Run through the generated public Wire service.
type StrictPrepared interface {
	Capture() testing.SessionCapture
	Dialer() transport.Dialer
	ToolExecutor() messages.ToolExecutor
	Audio() *recording.Replay
	Clock() clock.Scheduler
	Scope() StrictEvidenceScope
	WireEvents() int
	ToolCalls() int
	ValidateComplete() error
	Close() error
}

// StrictEvidenceScope describes what a credential-free headless run can
// substantiate. Recorded PCM/render fields describe evidence availability, not
// physical device consumption or acoustic output.
type StrictEvidenceScope struct {
	Protocol             bool
	Tools                bool
	RecordedPCM          bool
	RecordedRender       bool
	RenderTapUnavailable bool
	DeviceExecution      bool
}

// StrictRuntime is the headless core runtime invoked by strict replay.
type StrictRuntime interface {
	Run(context.Context, io.Writer) error
}

// StrictRuntimeFactory constructs one isolated runtime from one prepared
// bundle. It must use only the prepared dialer, recorded executor, and clock.
type StrictRuntimeFactory interface {
	New(StrictPrepared) (StrictRuntime, error)
}

// StrictResult is returned only after runtime and exact-evidence validation
// complete successfully.
type StrictResult struct {
	Capture    testing.SessionCapture
	Scope      StrictEvidenceScope
	WireEvents int
	ToolCalls  int
}

// StrictService prepares and runs the complete public strict replay workflow.
type StrictService interface {
	Prepare(context.Context, StrictRequest) (StrictPrepared, error)
	Run(context.Context, io.Writer, StrictRequest) (StrictResult, error)
}

// Compatibility aliases keep the existing CLI adapter source-compatible while
// the runtime package owns the canonical strict contracts.
type Request = StrictRequest
type Prepared = StrictPrepared
type EvidenceScope = StrictEvidenceScope
type Runtime = StrictRuntime
type RuntimeFactory = StrictRuntimeFactory
type Result = StrictResult
