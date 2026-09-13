// Package sessionfailure owns provider/session failure projection for a live
// stream. The contract contains only bounded values and explicit callbacks;
// hosts do not need to import the CLI observer or its private state.
package sessionfailure

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

const (
	ErrorClassUnknown            = "unknown"
	ErrorClassUnsupportedRequest = "unsupported_request"
	ErrorClassCancellation       = "cancellation"
	ErrorClassRoomBoundCancelled = "room_bound_cancelled"

	ClassificationScheduledAudio    = "scheduled_audio_incomplete"
	ClassificationUnresolvedTool    = "unresolved_tool_result"
	ClassificationImageContinuation = "image_tool_continuation"
	ClassificationToolContinuation  = "tool_continuation"

	DiagnosticEventToolCall       = "session_tool_call_unexecutable"
	DiagnosticFieldToolName       = "tool_name"
	DiagnosticFieldToolCallID     = "tool_call_id"
	DiagnosticFieldFailureClass   = "failure_classification"
	DiagnosticFieldFailureReason  = "failure_reason"
	DiagnosticFieldTurnIndex      = "turn_index"
	DiagnosticFailureNoToolRunner = "no_tool_executor_in_session_runtime"
)

// Facts is the immutable normalized taxonomy for one terminal failure.
type Facts struct {
	Classification string
	TerminalReason string
	Provenance     string
	OutputState    string
	ErrorType      string
	Code           string
	FailingEvent   string
}

// Observation is a normalized failure and its original in-process cause.
// Err is retained without wrapping so errors.Is/errors.As continue to work.
type Observation struct {
	Facts Facts
	Err   error
}

// Progress is the bounded stream state needed to derive output state.
type Progress struct {
	SessionOpened  bool
	TurnsCompleted int
}

// Projection identifies a session-owned continuation failure.
type Projection string

const (
	ProjectionScheduledAudio    Projection = ClassificationScheduledAudio
	ProjectionUnresolvedTool    Projection = ClassificationUnresolvedTool
	ProjectionImageContinuation Projection = ClassificationImageContinuation
	ProjectionToolContinuation  Projection = ClassificationToolContinuation
)

// ToolCall contains the bounded fields needed for an unsupported-tool record.
type ToolCall struct {
	Name      string
	ID        string
	TurnIndex int
}

// DiagnosticRecord is the transport-neutral structured event emitted by the
// service. Fields are owned by the record and are not retained after delivery.
type DiagnosticRecord struct {
	Event  string
	Fields map[string]string
}

// DiagnosticSink receives service-owned diagnostic records.
type DiagnosticSink interface {
	Record(DiagnosticRecord)
}

// DiagnosticSinkFunc adapts a callback to DiagnosticSink.
type DiagnosticSinkFunc func(DiagnosticRecord)

func (f DiagnosticSinkFunc) Record(record DiagnosticRecord) {
	if f != nil {
		f(record)
	}
}

// Dependencies are explicit application callbacks. Publish must return false
// when the surrounding lifecycle rejects an observation.
type Dependencies struct {
	Publish func(Observation) bool
	Sink    DiagnosticSink
}

// Service normalizes provider/session terminal values and owns the first
// accepted observation, immutable snapshots, and diagnostic projection.
type Service interface {
	NormalizeErrorValue(*messages.ErrorValue) (Facts, error)
	FactsFromSessionRunError(error) *Facts
	NormalizeClose(*messages.SessionCloseValue, Progress) Facts
	Accept(Facts, error) bool
	Snapshot() *Observation
	Clear()
	Projection(Projection, string, Progress) Facts
	EmitUnsupportedTool(ToolCall)
	OutputState(Progress) string
}
