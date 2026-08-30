package services

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// RoomTerminationReason is the room-level terminal taxonomy. A room has one
// reason even when individual participants finish at different times.
type RoomTerminationReason string

const (
	RoomTerminationStopped            RoomTerminationReason = "stopped"
	RoomTerminationMaxTurnsReached    RoomTerminationReason = "max_turns_reached"
	RoomTerminationMaxDurationReached RoomTerminationReason = "max_duration_reached"
	RoomTerminationFailed             RoomTerminationReason = "failed"
)

// RoomStopReason is a descriptive alias used by callers that name the room
// terminal state a stop reason.
type RoomStopReason = RoomTerminationReason

const (
	RoomStopStopped            = RoomTerminationStopped
	RoomStopMaxTurnsReached    = RoomTerminationMaxTurnsReached
	RoomStopMaxDurationReached = RoomTerminationMaxDurationReached
	RoomStopFailed             = RoomTerminationFailed
	RoomStopped                = RoomTerminationStopped
	RoomMaxTurnsReached        = RoomTerminationMaxTurnsReached
	RoomMaxDurationReached     = RoomTerminationMaxDurationReached
	RoomFailed                 = RoomTerminationFailed
)

// ParticipantTerminationReason is the participant-level terminal taxonomy.
// It intentionally remains independent of the room reason.
type ParticipantTerminationReason string

const (
	ParticipantTerminationEnded        ParticipantTerminationReason = "ended"
	ParticipantTerminationDisconnected ParticipantTerminationReason = "disconnected"
	ParticipantTerminationError        ParticipantTerminationReason = "error"
)

// RoomParticipantResult contains the observable outcome for one participant.
// Error is already sanitized; the resolved API-key value is never retained in
// the result.
type RoomParticipantResult struct {
	// ID and TerminationReason are the joined run-manifest names. The
	// ParticipantID and Reason aliases keep the result convenient for runtime
	// callers that use the same terminology as RoomParticipantEvent.
	ID                string                       `json:"id"`
	ParticipantID     string                       `json:"participant_id,omitempty"`
	TerminationReason ParticipantTerminationReason `json:"termination_reason"`
	Reason            ParticipantTerminationReason `json:"reason,omitempty"`
	TurnsCompleted    int                          `json:"turns_completed"`
	Connected         bool                         `json:"connected"`
	Error             string                       `json:"error,omitempty"`
}

// RoomResult contains the room outcome and every participant outcome. The map
// is keyed by the manifest's stable participant ID.
type RoomResult struct {
	TerminationReason  RoomTerminationReason            `json:"termination_reason"`
	Reason             RoomTerminationReason            `json:"reason,omitempty"`
	Participants       map[string]RoomParticipantResult `json:"participants"`
	ActiveParticipants []string                         `json:"active_participants,omitempty"`
	Error              string                           `json:"error,omitempty"`
}

// RoomRunResult is the descriptive result name used by callers that model a
// room execution as a value rather than a generic room state.
type RoomRunResult = RoomResult

// RoomSessionInferencerFactory constructs one independently configured
// participant session. The manifest participant contains only credential
// metadata; the resolved credential is available only in sessionOptions.
type RoomSessionInferencerFactory func(room.Participant, SessionRunOptions) (messages.SessionInferencer, error)

// RoomParticipantSessionFactory is the explicit composition-root name for
// RoomSessionInferencerFactory.
type RoomParticipantSessionFactory = RoomSessionInferencerFactory

// RoomSessionFactory is a concise alias for the participant factory.
type RoomSessionFactory = RoomSessionInferencerFactory

// RoomParticipantAudioObserver receives a copied provider AUDIO.DELTA before
// it is fanned into the other participants' mixers. It is observational and
// may be used by the evidence writer in a later composition layer.
type RoomParticipantAudioObserver func(participantID string, pcm []byte) error

// RoomParticipantDiagnosticObserver receives the credential-free diagnostic
// projection for one participant. It is intended for bounded terminal
// progress; raw stream deltas and audio remain unavailable through this seam.
type RoomParticipantDiagnosticObserver func(participantID string, record SessionDiagnosticRecord)

// RoomParticipantObserver receives one event after a participant leaves the
// room. It is called only after that participant's own mixer has been stopped.
type RoomParticipantObserver func(RoomParticipantResult)

// RoomObserver receives the single room terminal event after all participant
// goroutines and mixers have been torn down.
type RoomObserver func(RoomResult)

// RoomRunOptions configures a manifest-defined N-participant room. A custom
// SessionFactory or SessionInferencers map is intended for deterministic tests;
// the default factory builds the repository's existing live session runtime.
type RoomRunOptions struct {
	Manifest room.Manifest
	// OutputDir enables the durable room evidence bundle. An empty value keeps
	// the service's observational-only mode for callers that do not need
	// artifacts; the room CLI supplies a concrete, empty directory.
	OutputDir string

	SessionFactory     RoomSessionInferencerFactory
	SessionInferencers map[string]messages.SessionInferencer
	// ToolCapabilitiesFactory supplies an isolated tool executor and matching
	// provider definitions for each participant that names tools. A nil value
	// uses the normal config-backed registry when tools are requested; an
	// explicit empty tools list never constructs or advertises tools.
	ToolCapabilitiesFactory RoomParticipantToolCapabilitiesFactory
	// BrowserCapabilitiesFactory supplies an isolated WebMCP capability set
	// for each participant that has an explicit browserTools manifest object.
	// The room CLI installs the production factory; service tests and embedders
	// can inject a hermetic factory. It is never called for browser-disabled
	// participants.
	BrowserCapabilitiesFactory RoomParticipantBrowserCapabilitiesFactory

	// Validation is applied before any session factory is called. Setting
	// CredentialLookup is a convenience override for Validation.LookupCredential.
	Validation       room.ValidationOptions
	CredentialLookup func(string) (string, bool)

	PairFactory            room.PairFactory
	BaseURL                string
	ConfigDir              string
	WebSocketDialer        transport.Dialer
	WebSocketDialerFactory func(room.Participant) transport.Dialer
	// FrameSamples is a compact deterministic cadence override. Zero leaves
	// PCMFormat/MixerConfig unchanged; otherwise it uses the default 24 kHz
	// mono format with this many samples per frame.
	FrameSamples int

	// MixerConfig defaults to the room PCM16 contract when zero. PCMFormat is
	// retained as a concise override for callers that only need to change the
	// format and not queue limits.
	MixerConfig room.PCM16MixerConfig
	PCMFormat   room.PCM16Format

	OnAudioOutput           RoomParticipantAudioObserver
	OnAudioInput            RoomParticipantAudioObserver
	OnDiagnostic            RoomParticipantDiagnosticObserver
	OnParticipantTerminated RoomParticipantObserver
	OnRoomTerminated        RoomObserver
	// onParticipantSessionOpen is an internal deterministic lifecycle seam used
	// by package tests to release transport controls only after admission has
	// actually observed SESSION.OPEN.
	onParticipantSessionOpen func(string)
	// onParticipantAudioFanned is an internal observational seam used by
	// deterministic room tests to release a cadence only after source PCM has
	// been accepted by the target mixer. It is called after the real mixer write
	// and never changes the audio path.
	onParticipantAudioFanned func(sourceID, targetID string, pcm []byte)
	// onParticipantStream is an internal observational seam used by package
	// tests to gate the next deterministic input on a specific normalized
	// provider event. It does not replace the real stream observer.
	onParticipantStream func(participantID string, msg messages.StreamMessage)
	// Stream optionally receives the room's diagnostic, transcript, and
	// lifecycle projections. The broker is observational and never carries raw
	// audio. Callers that expose it over HTTP own the listener lifecycle.
	Stream *RoomEventBroker
}

// RoomOptions is a concise alias for RoomRunOptions.
type RoomOptions = RoomRunOptions
