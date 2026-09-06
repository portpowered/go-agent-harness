package session

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// LiveEvent is a structured lifecycle or transcript observation. Kind values
// use the LiveEventKind names above; the string field keeps provider-specific
// observations forward compatible. Sequence is monotonic per session.
//
// Observation delivery is best effort and bounded. A live implementation must
// never wait for this channel from an audio, provider-read, or control path.
// When a consumer falls behind, the implementation coalesces or drops
// observations and publishes one LiveEventOverflow record once capacity is
// available. Recording owners must mark the capture incomplete when overflow
// affects recorded evidence. Terminal state is reported by Wait and a
// reserved terminal delivery path, so it cannot be lost behind observations.
type LiveEvent struct {
	Sequence      uint64
	Timestamp     time.Time
	Kind          string
	SessionID     string
	ParticipantID string
	// Role preserves the author of transcript observations. Audio providers
	// emit both user input and assistant output transcripts; retaining the
	// role lets hosts render or persist them without guessing from event type.
	Role messages.Role
	// Browser correlation is retained when a participant capability binding
	// exposes BrowserWatch. These fields keep browser invocation, page
	// generation, and session evidence joinable without exposing the concrete
	// broker event type through the session contract.
	BrowserID    string
	TargetID     string
	Generation   uint64
	InvocationID string
	State        string
	Reason       string
	// Capability retains the complete provider-neutral browser/tool event when
	// one was observed. The scalar fields above are convenient for sinks that
	// only need correlation; this typed copy prevents projection from losing
	// lifecycle metadata needed by recorders and replay.
	Capability *LiveCapabilityEvent
	// Message retains the original provider-neutral stream observation when one
	// exists. It lets a host adapter preserve typed tool, image, audio, and
	// terminal payloads while the scalar fields above remain convenient for
	// event sinks. The runtime does not mutate the message after publication;
	// consumers that retain it across goroutines should copy any mutable payload.
	Message    *messages.StreamMessage
	ResponseID string
	ItemID     string
	ToolCallID string
	Text       string
	Error      error
	// Liveness carries the typed participant fault when the session detects an
	// empty provider response or a provider progress timeout. The runtime
	// publishes this event before its terminal event so room owners can retire
	// only the affected participant while preserving the causal fields.
	Liveness *LiveLivenessFailure
	// Terminal carries the provider/session terminal taxonomy when the
	// observation is a SESSION.CLOSE or when the live owner publishes its
	// final lifecycle event. Hosts can render or persist the typed value
	// without parsing human-readable errors.
	Terminal *messages.SessionCloseValue
	// Dropped is populated for LiveEventOverflow and reports the number of
	// observations omitted since the previous overflow report.
	Dropped uint64
	// Critical marks an event that must be retained by a recorder. Terminal
	// and overflow events are critical by definition; callers may use this bit
	// when forwarding provider-specific lifecycle events.
	Critical bool
}

// LiveLivenessFailure is the provider-neutral terminal evidence for a
// participant-owned liveness fault. It deliberately contains taxonomy and
// bounded usage only; provider credentials and raw payloads remain private to
// the provider service.
type LiveLivenessFailure struct {
	Classification     string
	ResponseID         string
	TerminalReason     messages.TerminalReason
	TerminalProvenance messages.TerminalProvenance
	OutputState        messages.TerminalOutputState
	Usage              messages.TokenUsage
}

// LiveEventSink is an optional observer port for room and diagnostics owners.
// Implementations must preserve terminal events and report overflow instead
// of silently dropping evidence.
type LiveEventSink interface {
	Publish(context.Context, LiveEvent) error
}

// LiveEventSinkFunc adapts a function to LiveEventSink. The callback is
// invoked by the session owner while it drains the bounded observation stream;
// it must return promptly and must not call back into the live handle.
type LiveEventSinkFunc func(context.Context, LiveEvent) error

func (f LiveEventSinkFunc) Publish(ctx context.Context, event LiveEvent) error {
	if f == nil {
		return nil
	}
	return f(ctx, event)
}
