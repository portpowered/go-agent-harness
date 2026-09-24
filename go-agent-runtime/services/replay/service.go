// Package replay defines artifact admission and replay planning for hosts.
package replay

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
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

const CaptureTimingReportSchemaVersion = 1

// CaptureTimingReport is the replay service's analysis of response, audio,
// and tool timing in an admitted capture.
type CaptureTimingReport struct {
	SchemaVersion int                     `json:"schema_version"`
	Provider      string                  `json:"provider"`
	Model         string                  `json:"model"`
	DurationMS    int64                   `json:"duration_ms"`
	SampleRateHz  int                     `json:"sample_rate_hz"`
	Responses     []CaptureResponseTiming `json:"responses"`
	Tools         []CaptureToolTiming     `json:"tools"`
	Summary       CaptureTimingSummary    `json:"summary"`
}

type CaptureResponseTiming struct {
	ResponseID             string  `json:"response_id"`
	TurnIndex              int     `json:"turn_index,omitempty"`
	CreatedMS              int64   `json:"created_ms"`
	FirstOutputMS          *int64  `json:"first_output_ms,omitempty"`
	FirstAudioMS           *int64  `json:"first_audio_ms,omitempty"`
	AudioDoneMS            *int64  `json:"audio_done_ms,omitempty"`
	DoneMS                 *int64  `json:"done_ms,omitempty"`
	AudioDurationMS        float64 `json:"audio_duration_ms"`
	AudioDeliverySpanMS    int64   `json:"audio_delivery_span_ms,omitempty"`
	AudioBurstRatio        float64 `json:"audio_burst_ratio,omitempty"`
	EstimatedPlaybackStart *int64  `json:"estimated_playback_start_ms,omitempty"`
	EstimatedPlaybackEnd   *int64  `json:"estimated_playback_end_ms,omitempty"`
	EstimatedAudibleGapMS  int64   `json:"estimated_audible_gap_ms,omitempty"`
	EstimatedQueueDelayMS  int64   `json:"estimated_queue_delay_ms,omitempty"`
}

type CaptureToolTiming struct {
	CallID                    string `json:"call_id"`
	Name                      string `json:"name"`
	ResponseID                string `json:"response_id"`
	CallReadyMS               int64  `json:"call_ready_ms"`
	ResultSentMS              *int64 `json:"result_sent_ms,omitempty"`
	ExecutionMS               *int64 `json:"execution_ms,omitempty"`
	ContinuationRequestedMS   *int64 `json:"continuation_requested_ms,omitempty"`
	ContinuationResponseID    string `json:"continuation_response_id,omitempty"`
	ContinuationCreatedMS     *int64 `json:"continuation_created_ms,omitempty"`
	ContinuationFirstOutputMS *int64 `json:"continuation_first_output_ms,omitempty"`
	ContinuationFirstAudioMS  *int64 `json:"continuation_first_audio_ms,omitempty"`
	ResultToRequestMS         *int64 `json:"result_to_request_ms,omitempty"`
	RequestToCreatedMS        *int64 `json:"request_to_created_ms,omitempty"`
	CreatedToFirstOutputMS    *int64 `json:"created_to_first_output_ms,omitempty"`
	ResultToFirstOutputMS     *int64 `json:"result_to_first_output_ms,omitempty"`
	ResultToFirstAudioMS      *int64 `json:"result_to_first_audio_ms,omitempty"`
}

type CaptureTimingSummary struct {
	ResponseCount              int                    `json:"response_count"`
	AudioResponseCount         int                    `json:"audio_response_count"`
	ToolCallCount              int                    `json:"tool_call_count"`
	UnfinishedToolCallCount    int                    `json:"unfinished_tool_call_count"`
	InputToFirstOutputMS       CaptureDurationSummary `json:"input_to_first_output_ms"`
	ResponseToFirstOutputMS    CaptureDurationSummary `json:"response_created_to_first_output_ms"`
	ToolExecutionMS            CaptureDurationSummary `json:"tool_execution_ms"`
	ToolResultToRequestMS      CaptureDurationSummary `json:"tool_result_to_request_ms"`
	ToolRequestToCreatedMS     CaptureDurationSummary `json:"tool_request_to_response_created_ms"`
	ToolCreatedToFirstOutputMS CaptureDurationSummary `json:"tool_response_created_to_first_output_ms"`
	ToolResultToFirstOutputMS  CaptureDurationSummary `json:"tool_result_to_first_output_ms"`
	ToolResultToFirstAudioMS   CaptureDurationSummary `json:"tool_result_to_first_audio_ms"`
	EstimatedAudibleGapMS      CaptureDurationSummary `json:"estimated_audible_gap_ms"`
	MaxAudioBurstRatio         float64                `json:"max_audio_burst_ratio"`
	MaxEstimatedQueueDelayMS   int64                  `json:"max_estimated_queue_delay_ms"`
}

type CaptureDurationSummary struct {
	Count int   `json:"count"`
	P50MS int64 `json:"p50_ms"`
	P95MS int64 `json:"p95_ms"`
	MaxMS int64 `json:"max_ms"`
}

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
	Facts            CaptureFacts
	LivePlan         *session.LiveReplayPlan
	// InitialTools describes the recorded initial provider advertisement, not
	// execution authorization. Hosts must retain their current tool policy.
	InitialTools      []string
	InitialToolsKnown bool
}

// CaptureFacts contains bounded, service-derived metadata needed by replay
// consumers. It deliberately omits raw event payloads so capture parsing stays
// behind the replay contract.
type CaptureFacts struct {
	Version                     int
	EventCount                  int
	ClientAudioAppendCount      int
	RealtimeWebSocketReplayable bool
	MetricDeltas                []CaptureMetricDelta
}

// CaptureInspector is the narrow replay contract used by services that need
// admitted capture metadata without constructing replay execution behavior.
type CaptureInspector interface {
	InspectCapture(context.Context, string) (CaptureInspection, error)
}

// CaptureProbeRequest describes one deterministic offline probe over a
// recorded provider capture. AudioSamples, when non-nil, replace the fixture's
// placeholder audio append events with framed PCM before replay.
type CaptureProbeRequest struct {
	SourcePath           string
	ValidateSource       bool
	CorpusID             string
	AudioSamples         []int16
	SampleRateHz         int
	ExpectedSampleRateHz int
}

// CaptureDocumentRequest selects a validated source capture or a synthetic
// empty capture for an existing host evidence format.
type CaptureDocumentRequest struct {
	SourcePath         string
	SyntheticSessionID string
}

// CaptureProbeObservation is a bounded projection of replayed protocol
// behavior. It contains no captured payloads, so hosts do not need to decode
// or interpret provider events themselves.
type CaptureProbeObservation struct {
	Provider                 string
	Model                    string
	FixtureProvenance        string
	Observations             []CaptureProbeEvent
	InboundFrames            int
	OutboundTicks            int
	EndsWithDisconnect       bool
	Transcript               string
	TerminalReason           string
	TerminalMetadataReason   string
	TerminalProvenance       string
	OutputState              string
	ToolCalls                []string
	ToolResultsDelivered     []string
	ToolResultsDiscarded     []string
	HasInterruptTick         bool
	InterruptTick            int
	HasResponseCancel        bool
	ResponseCancelTick       int
	UserTurnsCommitted       int
	AssistantTurnsDelivered  int
	ResponsesCreated         int
	ResponsesCancelled       int
	ResponseCancels          int
	SpuriousCancels          int
	PostCancelDeltas         int
	InFlightAtEnd            bool
	BufferDisposition        string
	AssistantAudioStarted    bool
	AssistantAudioStartEvent int
	AssistantAudioStopped    bool
	AssistantAudioStopEvent  int
}

// CaptureProbeEvent is a payload-free protocol observation returned in
// capture order. Replay internals keep the captured event bodies private.
type CaptureProbeEvent struct {
	Sequence  int
	Direction string
	Type      string
}

// CaptureMetricDelta is a service-derived aggregate for one direction and
// modality. It contains byte counts only; captured payloads remain private.
type CaptureMetricDelta struct {
	Direction metrics.Direction
	Modality  metrics.Modality
	Bytes     int64
}

// CaptureTraceEvent is the bounded, ordered provider-wire projection used by
// host trace attachment. Replay validates the source before returning these
// copied bytes; hosts do not open or decode capture files themselves.
type CaptureTraceEvent struct {
	Sequence  int
	Direction string
	Type      string
	Payload   []byte
}

// LiveRequest selects one credential-free realtime capture for preparation.
// Timing is interpreted by the replay service rather than by a host adapter.
type LiveRequest struct {
	SourcePath string
	Timing     session.LiveReplayTiming
}

// LivePrepared is the opaque, invocation-owned result of realtime replay
// admission. Concrete cursors, wrappers, and mutable completion state remain
// private to the replay service.
type LivePrepared interface {
	Inspection() CaptureInspection
	WrapDialer(transport.Dialer) transport.Dialer
	WrapInferencer(messages.SessionInferencer) messages.SessionInferencer
	Done() <-chan struct{}
	Err() error
	Close() error
}

// CaptureReplay is the bounded message stream for a turn-oriented capture.
// The replay service owns the cursor and its completion/error state; hosts only
// render the admitted messages and observe completion.
type CaptureReplay interface {
	// Drain consumes every admitted message in order until completion,
	// cancellation, or consumer failure. Buffer draining and terminal errors
	// remain private to the replay service.
	Drain(context.Context, func(messages.StreamMessage) error) error
	Receive() <-chan messages.StreamMessage
	Done() <-chan struct{}
	Err() error
	Close() error
}

// StreamMessageCodec is the replay-owned codec for one canonical transcript
// message. It exposes no capture files, cursors, or mutable replay state.
type StreamMessageCodec interface {
	EncodeStreamMessage(messages.StreamMessage) ([]byte, error)
	DecodeStreamMessage([]byte) (messages.StreamMessage, error)
}

// IsRealtime reports whether the admitted capture can drive a continuous
// provider session.
func (i CaptureInspection) IsRealtime() bool { return i.Kind == CaptureKindRealtime }

// Service constructs bounded replay actions from explicit capture artifacts.
// Execution and device attachment remain owned by the session service.
type Service interface {
	StreamMessageCodec
	CaptureInspector
	// InspectCapture validates and classifies a raw capture or finalized
	// recording directory, returning provider metadata and any self-driving
	// live plan. The returned paths are safe for the provider replay adapter.
	TraceCapture(context.Context, string) ([]CaptureTraceEvent, error)
	LoadLivePlan(context.Context, string) (session.LiveReplayPlan, error)
	// ResolveCapturePath admits either a raw provider capture or a finalized
	// recording directory. Directory admission verifies the manifest, complete
	// status, every declared artifact (including recorded PCM), and the
	// provider artifact path before returning the raw capture path to the
	// provider service.
	ResolveCapturePath(context.Context, string) (string, error)
	PrepareLive(context.Context, LiveRequest) (LivePrepared, error)
	Replay(context.Context, string) (CaptureReplay, error)
	// NewSessionInferencer returns the session-oriented replay boundary used by
	// provider and session services. Capture parsing and replay admission remain
	// owned by replay internals.
	NewSessionInferencer(context.Context, string) (messages.SessionInferencer, error)
	// AnalyzeTiming returns service-derived response, audio, and tool timing
	// information for one admitted capture path.
	AnalyzeTiming(context.Context, string) (CaptureTimingReport, error)
	// AnalyzeProbe validates and replays one provider capture, returning only
	// stable protocol observations needed by offline probe consumers. Optional
	// PCM input is framed and injected by replay internals.
	AnalyzeProbe(context.Context, CaptureProbeRequest) (CaptureProbeObservation, error)
	// AnalyzeProbeDocument validates and replays a capture document supplied by
	// an artifact reader, returning the same bounded projection as AnalyzeProbe.
	AnalyzeProbeDocument(context.Context, string, []byte) (CaptureProbeObservation, error)
	// InspectProbeDocument validates a capture document and returns its bounded
	// projection without executing the replay transport.
	InspectProbeDocument(context.Context, string, []byte) (CaptureProbeObservation, error)
	// WriteCaptureDocument emits the canonical capture representation used by
	// existing host evidence output.
	WriteCaptureDocument(context.Context, CaptureDocumentRequest, io.Writer) error
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
