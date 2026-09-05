package agentruntime

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"time"
)

// SessionRuntimeObservationKind identifies an observable boundary in one
// agent session command. The observations are emitted from the runtime after
// the command has accepted the corresponding event, not from a test
// coordinator or a provider transcript parser.
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

// SessionTokenUsageSemantics identifies how provider usage values are consumed
// by the session accounting seam.
type SessionTokenUsageSemantics string

const (
	// SessionTokenUsageIncremental means every non-negative MESSAGE.END usage
	// value is the contribution for that completed turn and is added exactly
	// once to the session total. Providers that expose cumulative readings must
	// normalize them before they reach the session stream.
	SessionTokenUsageIncremental SessionTokenUsageSemantics = "incremental"
)

// SessionFinalAccounting is the production-owned terminal accounting result
// for one session. Token fields are session-cumulative totals, not the usage
// from only the last turn. Metrics is a complete deep-copied snapshot with all
// supported direction/modality series, including untouched zero series.
type SessionFinalAccounting struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
	ReasoningTokens  uint64
	UsageSemantics   SessionTokenUsageSemantics
	Metrics          metrics.Snapshot
}

// SessionRuntimeFinalAccounting is a descriptive alias for callers that want
// the runtime boundary named explicitly.
type SessionRuntimeFinalAccounting = SessionFinalAccounting

// SessionRuntimeObservation is one clock-stamped observation from a session
// command. Payload is present for audio and tool observations and is copied before
// delivery so an observer owns its bytes. Terminal observations contain the
// command's actual clean/error result, completed-turn count, and the
// production-owned final accounting value.
type SessionRuntimeObservation struct {
	Kind           SessionRuntimeObservationKind
	Tick           uint64
	Timestamp      time.Time
	Payload        []byte
	TurnsCompleted int
	// InputCommit is the one-based ordinal of an input commit accepted by the
	// session. It is populated for client-owned MESSAGE.END boundaries and may
	// be zero for a provider-originated server-VAD commit.
	InputCommit int
	// ResponseID and ResponsePurpose are populated on ResponseCreate
	// observations when the provider/session seam has those identities.
	ResponseID      string
	ResponsePurpose messages.ResponsePurpose
	// StreamID and LoopPassID preserve provider/loop lineage for audio output.
	// Epoch is reserved for a device playback generation and remains zero here
	// because a loop pass is not a playback generation. Empty/zero fields mean
	// the source did not expose that identity; callers must not infer one from
	// arrival order.
	StreamID   string
	LoopPassID int
	Epoch      uint64
	Clean      bool
	Error      string
	// FinalAccounting is populated only on the terminal observation. It is
	// copied before delivery and can be retained by the observer safely.
	FinalAccounting *SessionFinalAccounting
}

// SessionRuntimeObserver receives runtime observations. It is optional and
// observational: a nil observer preserves the normal session behavior.
type SessionRuntimeObserver interface {
	ObserveSessionRuntime(SessionRuntimeObservation)
}
