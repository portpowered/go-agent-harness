// Package sessiontrace owns the host-neutral audio trace lifecycle.
package sessiontrace

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// DiagnosticRecord is the bounded, transport-neutral session diagnostic.
type DiagnosticRecord struct {
	Event  string
	Fields map[string]string
}

type DiagnosticSink interface {
	RecordSessionDiagnostic(DiagnosticRecord)
}

type DiagnosticFunc func(DiagnosticRecord)

func (f DiagnosticFunc) RecordSessionDiagnostic(record DiagnosticRecord) {
	if f != nil {
		f(record)
	}
}

type ToolDiagnostic struct {
	ToolCallID string
	ToolName   string
	Source     string
	ErrorCode  string
	Error      error
}

type ToolDiagnosticSink interface {
	RecordSessionToolDiagnostic(ToolDiagnostic)
}

type ToolDiagnosticFunc func(ToolDiagnostic)

func (f ToolDiagnosticFunc) RecordSessionToolDiagnostic(diagnostic ToolDiagnostic) {
	if f != nil {
		f(diagnostic)
	}
}

const (
	SessionDiagnosticEventFailure                          = "session_failure"
	SessionDiagnosticEventTerminal                         = "session_terminal"
	SessionDiagnosticEventTurn                             = "session_turn_completed"
	SessionDiagnosticEventToolCall                         = "session_tool_call_unexecutable"
	SessionDiagnosticEventMetrics                          = "session_metrics"
	SessionDiagnosticEventRoomBound                        = "room_bound_shutdown"
	SessionDiagnosticEventPlaybackOverflow                 = "session_playback_overflow"
	SessionDiagnosticFieldUnresolvedToolResultCount        = "unresolved_tool_result_count"
	SessionDiagnosticFieldUnresolvedToolCallIDs            = "unresolved_tool_call_ids"
	SessionDiagnosticFieldPendingImageContinuationCount    = "pending_image_continuation_count"
	SessionDiagnosticFieldPendingImageContinuationIDs      = "pending_image_continuation_call_ids"
	SessionDiagnosticFieldPendingToolContinuationCount     = "pending_tool_continuation_count"
	SessionDiagnosticFieldPendingToolContinuationIDs       = "pending_tool_continuation_call_ids"
	SessionDiagnosticFieldScheduledInputCount              = "scheduled_input_count"
	SessionDiagnosticFieldDispatchedInputCount             = "dispatched_input_count"
	SessionDiagnosticFieldCompletedTurnCount               = "completed_turn_count"
	SessionDiagnosticFieldPendingContinuationStatuses      = "pending_continuation_statuses"
	SessionDiagnosticFieldPendingContinuationCodes         = "pending_continuation_codes"
	SessionDiagnosticFieldPendingContinuationDetails       = "pending_continuation_details"
	SessionDiagnosticFieldCancelledBy                      = "cancelled_by"
	SessionDiagnosticFieldCancelledScheduledInputCount     = "cancelled_scheduled_input_count"
	SessionDiagnosticFieldCancelledToolResultCount         = "cancelled_tool_result_count"
	SessionDiagnosticFieldCancelledToolResultCallIDs       = "cancelled_tool_result_call_ids"
	SessionDiagnosticFieldCancelledToolContinuationCount   = "cancelled_tool_continuation_count"
	SessionDiagnosticFieldCancelledToolContinuationCallIDs = "cancelled_tool_continuation_call_ids"
	SessionDiagnosticFieldPlaybackDeviceID                 = "device_id"
	SessionDiagnosticFieldPlaybackSampleRate               = "sample_rate"
	SessionDiagnosticFieldPlaybackChannels                 = "channels"
	SessionDiagnosticFieldPlaybackLatencyTargetMillis      = "latency_target_ms"
	SessionDiagnosticFieldPlaybackCapacitySamples          = "capacity_samples"
	SessionDiagnosticFieldPlaybackQueuedSamples            = "queued_samples"
	SessionDiagnosticFieldPlaybackPeakQueuedSamples        = "peak_queued_samples"
	SessionDiagnosticFieldPlaybackDroppedSamples           = "dropped_samples"
	SessionDiagnosticFieldPlaybackOverflowEvents           = "overflow_events"
	SessionDiagnosticFieldPlaybackParticipantID            = "participant_id"
)

type StreamObserver func(messages.StreamMessage)

// CancellationIntent is an opaque run-scoped SIGINT marker shared by the
// trace observer and host boundary. Wire supplies its stateful implementation.
type CancellationIntent interface {
	MarkSIGINT()
	SIGINTReceived() bool
}

type ScheduledAudioInput = audioio.ScheduledAudioInput

type ScheduledAudioDispatchPolicy string

const (
	ScheduledAudioDispatchCompletionGated ScheduledAudioDispatchPolicy = "completion-gated"
	ScheduledAudioDispatchActiveResponse  ScheduledAudioDispatchPolicy = "active-response"
)

type ScheduledInputSender interface {
	SendAudioInput(context.Context, []byte) error
	SendSessionEvent(context.Context, messages.StreamMessage) error
}

type LivenessTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type LivenessClock interface {
	NewTimer(time.Duration) LivenessTimer
}

type LivenessError struct {
	Classification     string
	ResponseID         string
	TerminalReason     messages.TerminalReason
	TerminalProvenance messages.TerminalProvenance
	OutputState        messages.TerminalOutputState
	Usage              messages.TokenUsage
}

type TerminalObservation struct {
	Failure            bool
	Classification     string
	TerminalReason     messages.TerminalReason
	TerminalProvenance messages.TerminalProvenance
	OutputState        messages.TerminalOutputState
	Err                error
	ResponseID         string
	Code               string
	FailingEvent       string
	RoomBound          bool
}

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	ErrClockRequired     errorCode = "session trace clock is required"
	ErrCloseTimeout      errorCode = "session trace close timed out"
	ErrDestinationExists errorCode = "audio trace destination exists"
	DefaultCloseTimeout            = time.Second
)

// CaptureSamplesObserver receives a copy-safe PCM tap notification.
type CaptureSamplesObserver func(rate int, samples []int16)

// PlaybackSamplesObserver receives the enqueued PCM tap and may preserve a
// pre-existing observer error.
type PlaybackSamplesObserver func(context.Context, int, []int16) error

// DeviceBinding contains only the callbacks needed by trace capture. Device
// ownership and all other device configuration remain in the gateway.
type DeviceBinding struct {
	PreGateSamplesObserver     CaptureSamplesObserver
	UploadedSamplesObserver    CaptureSamplesObserver
	PlaybackSamplesObserver    PlaybackSamplesObserver
	RenderedSamplesObserver    CaptureSamplesObserver
	RenderedSamplesUnavailable func()
}

// LiveRecorderOptions configures the invocation-scoped recorder adapter. The
// trace service owns observation and correlation; the host supplies only the
// already-open session recorder and negotiated media rates.
type LiveRecorderOptions struct {
	Inner      session.LiveRecorder
	Observer   RuntimeObserver
	InputRate  int
	OutputRate int
}

type PlaybackDiagnosticsOptions struct {
	Sink          DiagnosticSink
	MetricSampler observability.MetricSampler
	Logger        observability.Logger
	Runtime       RuntimeRecorder
}

type PlaybackDiagnostics interface {
	PlaybackObserver(devicert.RTCDevicePlaybackObserver) devicert.RTCDevicePlaybackObserver
	PlaybackReceiptObserver(devicert.RTCDevicePlaybackReceiptObserver) devicert.RTCDevicePlaybackReceiptObserver
	CaptureObserver(devicert.RTCDeviceCaptureObserver) devicert.RTCDeviceCaptureObserver
	RecordParticipantPlaybackOverflow(string, *devicegw.DeviceSink)
}

// SessionRuntimeObservationKind identifies an observable runtime boundary.
type SessionRuntimeObservationKind string

const (
	SessionRuntimeObservationAudioOutput               SessionRuntimeObservationKind = "audio_output"
	SessionRuntimeObservationAudioInput                SessionRuntimeObservationKind = "audio_input"
	SessionRuntimeObservationAudioPlaybackReceipt      SessionRuntimeObservationKind = "audio_playback_receipt"
	SessionRuntimeObservationAudioRenderTapUnavailable SessionRuntimeObservationKind = "audio_render_tap_unavailable"
	SessionRuntimeObservationInputCommit               SessionRuntimeObservationKind = "input_commit"
	SessionRuntimeObservationResponseCreate            SessionRuntimeObservationKind = "response_create"
	SessionRuntimeObservationTurnCompleted             SessionRuntimeObservationKind = "turn_completed"
	SessionRuntimeObservationTerminal                  SessionRuntimeObservationKind = "terminal"
)

type SessionTokenUsageSemantics string

const SessionTokenUsageIncremental SessionTokenUsageSemantics = "incremental"

type SessionFinalAccounting struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
	ReasoningTokens  uint64
	UsageSemantics   SessionTokenUsageSemantics
	Metrics          metrics.Snapshot
}

type SessionRuntimeFinalAccounting = SessionFinalAccounting

// SessionRuntimeObservation is one clock-stamped observation from a session.
type SessionRuntimeObservation struct {
	Kind            SessionRuntimeObservationKind
	Tick            uint64
	Timestamp       time.Time
	Payload         []byte
	TurnsCompleted  int
	InputCommit     int
	ResponseID      string
	ResponsePurpose messages.ResponsePurpose
	StreamID        string
	LoopPassID      int
	Epoch           uint64
	Clean           bool
	Error           string
	FinalAccounting *SessionFinalAccounting
}

type RuntimeObservation = SessionRuntimeObservation
type FinalAccounting = SessionFinalAccounting

// SessionRuntimeObserver receives ordered runtime events.
type SessionRuntimeObserver interface {
	ObserveSessionRuntime(SessionRuntimeObservation)
}

type RuntimeObserver = SessionRuntimeObserver

// RuntimeObserverFunc adapts a function to RuntimeObserver.
type RuntimeObserverFunc func(RuntimeObservation)

func (f RuntimeObserverFunc) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	f(observation)
}

// MetricsCollector is the public replay-metrics contract. The runner is
// supplied by the host; fixture accounting and reconciliation remain owned by
// sessiontrace.
type MetricsCollector interface {
	Collect(context.Context, string, string) ([]probe.MetricsSeries, error)
}

type MetricsRunner func(context.Context, string, string) (metrics.Snapshot, error)

type MetricsCollectorOptions struct {
	Clock           clock.Source
	Runner          MetricsRunner
	FactoryReady    func() bool
	ReplayInspector replay.CaptureInspector
}

// ProviderBoundaryObserver is an optional runtime policy preference.
type ProviderBoundaryObserver interface {
	ObserveProviderBoundaries() bool
}

// ProviderDialer is the trace-decorated provider transport role. It is a
// distinct Wire type so a host can supply the undecorated transport and
// receive the service-owned decorated transport without a duplicate binding.
type ProviderDialer interface {
	transport.Dialer
}

// CommitPayloadObserver is an optional runtime policy preference.
type CommitPayloadObserver interface {
	RetainCommitPayload() bool
}

// Request admits one trace invocation. A nil Clock is rejected whenever
// trace capture is enabled; the service never substitutes a wall clock.
type Request struct {
	TraceAudio      bool
	RecordDirectory string
	Clock           clock.Source
	Credentials     []string
	Device          DeviceBinding
	RuntimeObserver RuntimeObserver
	CloseTimeout    time.Duration
}

// Prepared owns one staged trace until Finish publishes or retains it.
type Prepared interface {
	DeviceBinding() DeviceBinding
	RuntimeObserver() RuntimeObserver
	StagedPath() string
	Finish(context.Context, string, bool) error
	WrapLiveRecorder(session.LiveRecorder, session.LiveRequest) session.LiveRecorder
	WrapDeviceService(runtimeDevices.Service) runtimeDevices.Service
	WrapAudioSource(audio.AudioSource, int) audio.AudioSource
}

// Service creates one independent prepared trace per request.
type Service interface {
	Prepare(Request) (Prepared, error)
}
