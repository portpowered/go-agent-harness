// Package sessionobservation exposes the host-neutral observation contract
// used by one session runtime invocation.
package sessionobservation

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

// SessionRuntimeObservationKind identifies an observable session boundary.
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

// SessionTokenUsageSemantics describes how provider usage contributes to the
// session total.
type SessionTokenUsageSemantics string

const (
	// SessionTokenUsageIncremental means each completed-turn usage value is
	// added once to the cumulative session total.
	SessionTokenUsageIncremental SessionTokenUsageSemantics = "incremental"
)

// SessionFinalAccounting is the production-owned terminal accounting result.
type SessionFinalAccounting struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
	ReasoningTokens  uint64
	UsageSemantics   SessionTokenUsageSemantics
	Metrics          metrics.Snapshot
}

// SessionRuntimeFinalAccounting is a descriptive terminal-accounting alias.
type SessionRuntimeFinalAccounting = SessionFinalAccounting

// SessionRuntimeObservation is one clock-stamped observation. Payload and
// FinalAccounting are copied by the implementation before delivery.
type SessionRuntimeObservation struct {
	Kind           SessionRuntimeObservationKind
	Tick           uint64
	Timestamp      time.Time
	Payload        []byte
	TurnsCompleted int
	// InputCommit is one-based for client-owned commits and zero for a
	// provider-originated server-VAD commit.
	InputCommit int
	// ResponseID and ResponsePurpose identify a response-create boundary.
	ResponseID      string
	ResponsePurpose messages.ResponsePurpose
	// StreamID and LoopPassID preserve provider/loop lineage. Epoch is a
	// playback generation and is not inferred from loop identity.
	StreamID   string
	LoopPassID int
	Epoch      uint64
	Clean      bool
	Error      string
	// FinalAccounting is populated only on the terminal observation.
	FinalAccounting *SessionFinalAccounting
}

// SessionRuntimeObserver receives observations. It is optional.
type SessionRuntimeObserver interface {
	ObserveSessionRuntime(SessionRuntimeObservation)
}

// PlaybackReceipt is the transport-neutral projection of a completed audio
// playback command. Err is retained for classification and is never used to
// infer that a rejected command was applied.
type PlaybackReceipt struct {
	CommandID  uint64
	Epoch      uint64
	AudioEndMS int
	Applied    bool
	Err        error
}

// AudioPlaybackReceipt is the descriptive alias used by audio-facing
// consumers.
type AudioPlaybackReceipt = PlaybackReceipt

// Service owns the mutable observation state for one session invocation.
// Construction and observer delivery remain separate from provider transport
// and from the CLI host.
type Service interface {
	EnableProviderBoundaryObservations()
	ProviderBoundaryObservationsEnabled() bool

	Observe(SessionRuntimeObservationKind, []byte, int, bool, error)
	ObserveWithInputCommit(SessionRuntimeObservationKind, []byte, int, int, bool, error)
	ObserveFinal(SessionRuntimeObservationKind, []byte, int, int, bool, error, *SessionFinalAccounting)
	ObserveWithMetadata(SessionRuntimeObservationKind, []byte, int, int, bool, error, string, messages.ResponsePurpose)
	ObserveWithMetadataAndIdentity(SessionRuntimeObservationKind, []byte, int, int, bool, error, string, messages.ResponsePurpose, string, int, uint64)
	ObserveFinalWithMetadata(SessionRuntimeObservationKind, []byte, int, int, bool, error, *SessionFinalAccounting, string, messages.ResponsePurpose)
	ObserveFinalWithMetadataAndIdentity(SessionRuntimeObservationKind, []byte, int, int, bool, error, *SessionFinalAccounting, string, messages.ResponsePurpose, string, int, uint64)

	AudioOutputMessage([]byte, messages.StreamMessage)
	AudioPlaybackReceipt(PlaybackReceipt)
	AudioInput([]byte)
	ProviderAudioSent([]byte)
	InputCommit()
	ProviderInputCommit()
	ResponseCreate(messages.StreamMessage)
	TurnCompleted(int)
	TerminalWithAccounting(int, error, *SessionFinalAccounting)
	ObserveToolCall(messages.ToolCall)
	ObserveToolResult(messages.ToolCall, messages.ToolCallResponse, bool)
}
