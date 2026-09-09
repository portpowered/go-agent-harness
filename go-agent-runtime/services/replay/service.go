// Package replay defines artifact admission and replay planning for hosts.
package replay

import (
	"context"
	"fmt"
	"io"
	"sync"

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

// StrictPrepared contains hermetic dependencies and an opaque completion
// witness for one headless replay. Only a prepared value with an internal
// completion witness can validate successfully.
type StrictPrepared struct {
	Capture      testing.SessionCapture
	Dialer       transport.Dialer
	ToolExecutor messages.ToolExecutor
	Audio        *recording.Replay
	Clock        clock.Scheduler
	Scope        StrictEvidenceScope
	WireEvents   int
	ToolCalls    int
	completion   *strictCompletion
}

type strictCompletion struct {
	mu            sync.Mutex
	expectedWire  int
	consumedWire  int
	expectedTools int
	consumedTools int
	dialed        bool
	invalid       bool
	err           error
}

func (c *strictCompletion) beginDial() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dialed {
		c.err = fmt.Errorf("%w: replay bundle permits one session connection", ErrBundleMismatch)
		return c.err
	}
	c.dialed = true
	return nil
}

func (c *strictCompletion) fail(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

func (c *strictCompletion) failWire(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	if c.err == nil && c.consumedWire < c.expectedWire {
		c.err = fmt.Errorf("%w: %w", ErrBundleIncomplete, err)
	}
	c.mu.Unlock()
}

func (c *strictCompletion) markWire() {
	c.mu.Lock()
	c.consumedWire++
	c.mu.Unlock()
}

func (c *strictCompletion) markTool() {
	c.mu.Lock()
	c.consumedTools++
	c.mu.Unlock()
}

func (c *strictCompletion) validate() error {
	if c == nil {
		return ErrBundleIncomplete
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.invalid || !c.dialed || c.consumedWire != c.expectedWire || c.consumedTools != c.expectedTools {
		return fmt.Errorf("%w: provider wire consumed %d/%d and tools consumed %d/%d", ErrBundleIncomplete, c.consumedWire, c.expectedWire, c.consumedTools, c.expectedTools)
	}
	return nil
}

type strictToolCallCounter interface {
	ExpectedToolCalls() int
}

// StrictPreparedBuilder tracks successful public dialer and tool operations
// and is the only construction path for a prepared completion witness. It
// derives expected operation counts from the supplied evidence dependencies;
// callers cannot provide or override those counts. Runtime hosts should obtain
// prepared values from StrictService.
type StrictPreparedBuilder struct{}

// Build creates one prepared replay with operation-based completion tracking.
// The capture and executor own the expected evidence counts. An empty capture
// or an executor without service-owned count metadata remains incomplete even
// if a caller supplies no-op dependencies.
func (StrictPreparedBuilder) Build(
	capture testing.SessionCapture,
	dialer transport.Dialer,
	toolExecutor messages.ToolExecutor,
	audio *recording.Replay,
	scheduler clock.Scheduler,
	scope StrictEvidenceScope,
) StrictPrepared {
	expectedTools, hasToolCount := 0, false
	if counter, ok := toolExecutor.(strictToolCallCounter); ok {
		expectedTools = counter.ExpectedToolCalls()
		hasToolCount = expectedTools >= 0
	}
	completion := &strictCompletion{
		expectedWire:  len(capture.Records),
		expectedTools: expectedTools,
		invalid:       len(capture.Records) == 0 || toolExecutor == nil || !hasToolCount,
	}
	return StrictPrepared{
		Capture:      capture,
		Dialer:       &completionDialer{inner: dialer, state: completion},
		ToolExecutor: &completionToolExecutor{inner: toolExecutor, state: completion},
		Audio:        audio,
		Clock:        scheduler,
		Scope:        scope,
		WireEvents:   len(capture.Records),
		ToolCalls:    expectedTools,
		completion:   completion,
	}
}

type completionDialer struct {
	inner transport.Dialer
	state *strictCompletion
}

func (d *completionDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.state == nil || d.inner == nil {
		return nil, fmt.Errorf("%w: replay dialer is unavailable", ErrBundleIncomplete)
	}
	if err := d.state.beginDial(); err != nil {
		return nil, err
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		d.state.fail(err)
		return nil, err
	}
	if conn == nil {
		err := fmt.Errorf("%w: replay dialer returned a nil connection", ErrBundleIncomplete)
		d.state.fail(err)
		return nil, err
	}
	return &completionConn{inner: conn, state: d.state}, nil
}

type completionConn struct {
	inner transport.Conn
	state *strictCompletion
}

func (c *completionConn) ReadMessage() (int, []byte, error) {
	if c == nil || c.inner == nil {
		err := fmt.Errorf("%w: replay connection is unavailable", ErrBundleIncomplete)
		if c != nil {
			c.state.fail(err)
		}
		return 0, nil, err
	}
	messageType, payload, err := c.inner.ReadMessage()
	if err == nil {
		c.state.markWire()
	} else {
		c.state.failWire(err)
	}
	return messageType, payload, err
}

func (c *completionConn) WriteMessage(messageType int, payload []byte) error {
	if c == nil || c.inner == nil {
		err := fmt.Errorf("%w: replay connection is unavailable", ErrBundleIncomplete)
		if c != nil {
			c.state.fail(err)
		}
		return err
	}
	err := c.inner.WriteMessage(messageType, payload)
	if err == nil {
		c.state.markWire()
	} else {
		c.state.fail(err)
	}
	return err
}

func (c *completionConn) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	err := c.inner.Close()
	c.state.fail(err)
	return err
}

type completionToolExecutor struct {
	inner messages.ToolExecutor
	state *strictCompletion
}

func (e *completionToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e == nil || e.inner == nil {
		err := fmt.Errorf("%w: replay tool executor is unavailable", ErrToolFailure)
		if e != nil {
			e.state.fail(err)
		}
		return messages.ToolCallResponse{}, err
	}
	response, err := e.inner.Execute(ctx, call)
	if err == nil {
		e.state.markTool()
	}
	return response, err
}

func (e *completionToolExecutor) ExpectedToolCalls() int {
	if e == nil || e.state == nil {
		return -1
	}
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	return e.state.expectedTools
}

var _ transport.Dialer = (*completionDialer)(nil)
var _ transport.Conn = (*completionConn)(nil)
var _ messages.ToolExecutor = (*completionToolExecutor)(nil)

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

// ValidateComplete verifies that every recorded wire and tool event was
// consumed exactly once at the strict runtime's terminal boundary.
func (p StrictPrepared) ValidateComplete() error {
	if p.completion == nil {
		return ErrBundleIncomplete
	}
	return p.completion.validate()
}

// Close validates completion. Strict preparation owns no live resources, so
// closing it cannot dial, stop hardware, or execute a tool.
func (p StrictPrepared) Close() error { return p.ValidateComplete() }

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
