// Package replay defines the offline session-bundle replay service boundary.
// Its implementation is kept under services/internal/replay so transports
// cannot construct live providers or tools while preparing a replay.
package replay

import (
	"context"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	ErrBundleIncomplete           = errors.New("replay bundle is incomplete")
	ErrBundleMismatch             = errors.New("replay bundle evidence mismatch")
	ErrToolMismatch               = errors.New("replay tool invocation mismatch")
	ErrToolFailure                = errors.New("recorded tool execution failed")
	ErrDeterministicClockRequired = errors.New("offline replay requires an injected deterministic clock")
	ErrRuntimeFactoryRequired     = errors.New("offline replay runtime factory is required")
)

// Request identifies a canonical --record-dir bundle. Provider selects the
// protocol adapter in the composed runtime (the production replay factory
// currently supports OpenAI); the canonical audio trace has no trusted
// provider metadata to verify this value independently. Model is checked
// against the captured provider handshake when supplied. No credentials,
// device selectors, or executable tool factories belong here.
type Request struct {
	BundlePath string
	Provider   string
	Model      string
}

// Prepared contains dependencies for a headless session runtime. Dialer and
// ToolExecutor are hermetic: they only consume validated bundle evidence.
type Prepared struct {
	Capture      testing.SessionCapture
	Dialer       transport.Dialer
	ToolExecutor messages.ToolExecutor
	Audio        *recording.Replay
	Clock        clock.Scheduler
	Scope        EvidenceScope
	WireEvents   int
	ToolCalls    int

	validate func() error
}

// EvidenceScope describes what an offline run can substantiate. Protocol and
// tool replay are exercised by Run; PCM fields describe recorded evidence that
// a headless runtime may feed into its own buffers. Device scheduling/DSP is
// never performed by this service, and a remote provider acknowledgement is
// not a local render receipt.
type EvidenceScope struct {
	Protocol             bool
	Tools                bool
	RecordedPCM          bool
	RecordedRender       bool
	RenderTapUnavailable bool
	DeviceExecution      bool
}

// Runtime is the headless core runtime invoked by Run. Implementations must
// use the prepared dialer, recorded executor, audio replay, and clock rather
// than constructing live capabilities.
type Runtime interface {
	Run(context.Context, io.Writer) error
}

// RuntimeFactory constructs one isolated headless runtime for one prepared
// bundle. Factories are injected by composition so replay never discovers a
// provider, device, or executable tool implementation on its own. A factory
// must use only the capabilities in Prepared: its Dialer is an in-memory
// strict replay transport and its ToolExecutor is an exact-once recorded
// executor.
type RuntimeFactory interface {
	New(Prepared) (Runtime, error)
}

// Result is returned after the runtime and strict evidence checks complete.
type Result struct {
	Capture    testing.SessionCapture
	Scope      EvidenceScope
	WireEvents int
	ToolCalls  int
}

// NewPrepared is reserved for the private composition implementation. Keeping
// construction here prevents it from depending on implementation-only fields.
func NewPrepared(capture testing.SessionCapture, dialer transport.Dialer, executor messages.ToolExecutor, audio *recording.Replay, scheduler clock.Scheduler, scope EvidenceScope, wireEvents, toolCalls int, validate func() error) Prepared {
	return Prepared{Capture: capture, Dialer: dialer, ToolExecutor: executor, Audio: audio, Clock: scheduler, Scope: scope, WireEvents: wireEvents, ToolCalls: toolCalls, validate: validate}
}

// ValidateComplete verifies that every recorded tool call was consumed once.
// A runtime should call this at its terminal boundary before reporting replay
// success.
func (p Prepared) ValidateComplete() error {
	if p.validate == nil {
		return ErrBundleIncomplete
	}
	return p.validate()
}

// Close validates completion. The prepared replay itself owns no live
// resources, so closing it cannot dial, stop hardware, or execute a tool.
func (p Prepared) Close() error { return p.ValidateComplete() }

// Service prepares strict offline replay dependencies from a bundle.
type Service interface {
	Prepare(context.Context, Request) (Prepared, error)
	Run(context.Context, io.Writer, Request) (Result, error)
}
