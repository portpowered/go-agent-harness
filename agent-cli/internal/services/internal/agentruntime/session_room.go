package agentruntime

import (
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
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

// ParticipantTerminationTrigger identifies the event that caused a
// participant's terminal observation. Bound triggers gain a _mid_response
// suffix when the room bound arrived while that participant had an active
// provider response.
const (
	ParticipantTerminationTriggerStopped                       = "stopped"
	ParticipantTerminationTriggerMaxTurnsReached               = "max_turns_reached"
	ParticipantTerminationTriggerMaxTurnsReachedMidResponse    = "max_turns_reached_mid_response"
	ParticipantTerminationTriggerMaxDurationReached            = "max_duration_reached"
	ParticipantTerminationTriggerMaxDurationReachedMidResponse = "max_duration_reached_mid_response"
	ParticipantTerminationTriggerSessionFailure                = "session_failure"
	ParticipantTerminationTriggerParticipantCompletion         = "participant_completion"
	ParticipantTerminationTriggerProviderClose                 = "provider_close"
)

// ParticipantTerminationDisposition explains how the triggering event was
// resolved. In particular, bound responses either complete in grace or are
// cancelled after grace; those are intentionally distinct from failure.
const (
	ParticipantTerminationDispositionCompletedDuringGrace = "completed_during_grace"
	ParticipantTerminationDispositionCancelledAfterGrace  = "cancelled_after_grace"
	ParticipantTerminationDispositionCompleted            = "completed"
	ParticipantTerminationDispositionStopped              = "stopped"
	ParticipantTerminationDispositionFailed               = "failed"
	ParticipantTerminationDispositionDisconnected         = "disconnected"
)

// RoomBoundCancelledClassification is the stable participant classification
// for a response deliberately cancelled by the room after its bound grace
// budget. It is intentionally distinct from generic caller cancellation.
const RoomBoundCancelledClassification = providers.ErrorClassRoomBoundCancelled

// RoomParticipantResult contains the observable outcome for one participant.
// Error is already sanitized; the resolved API-key value is never retained in
// the result.
type RoomParticipantResult struct {
	// ID and TerminationReason are the joined run-manifest names. The
	// ParticipantID and Reason aliases keep the result convenient for runtime
	// callers that use the same terminology as RoomParticipantEvent.
	ID                     string                       `json:"id"`
	ParticipantID          string                       `json:"participant_id,omitempty"`
	TerminationReason      ParticipantTerminationReason `json:"termination_reason"`
	Reason                 ParticipantTerminationReason `json:"reason,omitempty"`
	TerminationTrigger     string                       `json:"termination_trigger"`
	TerminationDisposition string                       `json:"termination_disposition"`
	Classification         string                       `json:"classification"`
	TerminalReason         string                       `json:"terminal_reason"`
	TerminalProvenance     string                       `json:"terminal_provenance"`
	OutputState            string                       `json:"output_state"`
	TurnsCompleted         int                          `json:"turns_completed"`
	Connected              bool                         `json:"connected"`
	Error                  string                       `json:"error,omitempty"`
	// RecordingStatus is nil for a healthy evidence bundle and partial when
	// one or more participant-owned recording artifacts degraded. It is
	// independent from the participant runtime termination reason.
	RecordingStatus *transcript.RecordingStatus `json:"recording_status,omitempty"`
}

// RoomResult contains the room outcome and every participant outcome. The map
// is keyed by the manifest's stable participant ID.
type RoomResult struct {
	TerminationReason  RoomTerminationReason            `json:"termination_reason"`
	Reason             RoomTerminationReason            `json:"reason,omitempty"`
	Participants       map[string]RoomParticipantResult `json:"participants"`
	ActiveParticipants []string                         `json:"active_participants,omitempty"`
	Error              string                           `json:"error,omitempty"`
	// RecordingStatus reports evidence health separately from the room's live
	// termination taxonomy. Recording failures never change TerminationReason.
	RecordingStatus   *transcript.RecordingStatus `json:"recording_status,omitempty"`
	DegradedArtifacts map[string]string           `json:"degraded_artifacts,omitempty"`
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

// RoomParticipantReady is the credential-free startup projection for one
// participant. Human participants report their selected device IDs; provider
// participants report their provider/model. The resolved credential value is
// intentionally absent.
type RoomParticipantReady struct {
	ID            string               `json:"id"`
	ParticipantID string               `json:"participant_id"`
	Kind          room.ParticipantKind `json:"kind"`
	InputDevice   string               `json:"input_device,omitempty"`
	OutputDevice  string               `json:"output_device,omitempty"`
	Provider      string               `json:"provider,omitempty"`
	Model         string               `json:"model,omitempty"`
}

// RoomParticipantReadyObserver receives one event for each participant after
// all required human devices and provider sessions have passed admission.
type RoomParticipantReadyObserver func(RoomParticipantReady)

// RoomObserver receives the single room terminal event after all participant
// goroutines and mixers have been torn down.
type RoomObserver func(RoomResult)

// RoomRunOptions configures a manifest-defined N-participant room. A custom
// SessionFactory or SessionInferencers map is intended for deterministic tests;
// the default factory builds the repository's existing live session runtime.
type RoomRunOptions struct {
	Manifest room.Manifest
	// ReplayPath selects a finalized room evidence directory (or its
	// run-manifest.json) as the sole source of participant runtime settings.
	// Replay admission never resolves credentials, live config, host devices,
	// browser capabilities, or default provider dialers.
	ReplayPath string
	// ReplayPlan is the already-admitted form of ReplayPath. The CLI supplies
	// both values so startup can pass a validated, immutable plan through the
	// service boundary without reopening the source bundle.
	ReplayPlan *RoomReplayPlan
	// Clock is the shared room timestamp source used by runtime landmarks and
	// finalized evidence. Nil selects the host clock; deterministic callers
	// should inject one source for all participants.
	Clock platformclock.Source
	// LivenessClock supplies the participant-owned provider watchdog timers.
	// Nil derives timers from Clock when possible, otherwise each participant
	// uses the host timer. A shared deterministic clock keeps room tests and
	// participant watchdogs on one controllable timeline.
	LivenessClock SessionLivenessClock
	// BoundShutdownGrace is the fixed room-bound drain window. A zero value
	// selects the documented production default; tests may override it with a
	// small positive duration to make the bounded drain deterministic.
	BoundShutdownGrace time.Duration
	// LaunchPlan is the normalized decision produced by ResolveRoomLaunchPlan.
	// The CLI supplies it for bare launches so command/service composition tests
	// can observe the selected devices and credential provenance without
	// inspecting or reopening a config file. A nil value preserves direct
	// manifest-driven service callers.
	LaunchPlan *runtimeRooms.RoomLaunchPlan
	// OutputDir enables the durable room evidence bundle. An empty value keeps
	// the service's observational-only mode for callers that do not need
	// artifacts; the room CLI supplies a concrete, empty directory.
	OutputDir string
	// DeviceRegistry is the runtime registry used by human participants. Bare
	// launch resolution selects the defaults without opening them; the room
	// opens the selected input and output at startup and owns them until the
	// participant is torn down. Provider-only manifests do not require it.
	DeviceRegistry devicegw.DeviceRegistry

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

	PairFactory room.PairFactory
	BaseURL     string
	ConfigDir   string
	ModelCatalog runtimeProviders.ModelCatalog
	// WorkDir and AllowPaths are the canonical customer filesystem scope for
	// room participant tools. FilesystemPolicy is the immutable snapshot shared
	// by every participant that receives a filesystem tool.
	WorkDir                string
	AllowPaths             []string
	FilesystemPolicy       *tools.FilesystemPolicy
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
	OnParticipantReady      RoomParticipantReadyObserver
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
	// onParticipantMixerReady is an internal deterministic lifecycle seam used
	// by package tests to exercise mixer admission failures through the normal
	// room composition boundary. It is called after all peer inputs are added.
	onParticipantMixerReady func(participantID string, mixer *room.PCM16Mixer)
	// onParticipantAudioInput is an internal deterministic failure seam used by
	// package tests to exercise provider-input rejection after a mixed frame has
	// left the participant mixer. A nil hook uses AgentLoop.SendAudioInput.
	onParticipantAudioInput func(participantID string, pcm []byte) error
	// onParticipantStream is an internal observational seam used by package
	// tests to gate the next deterministic input on a specific normalized
	// provider event. It does not replace the real stream observer.
	onParticipantStream func(participantID string, msg messages.StreamMessage)
	// onRoomEvidenceReady is an internal deterministic test seam. It runs after
	// all room evidence sinks are opened and before participant work starts, so
	// package tests can inject a sink failure without changing live APIs.
	onRoomEvidenceReady func(*roomEvidence)
	// onRoomBoundShutdown is an internal deterministic lifecycle seam used by
	// package tests to release a response after bound admission has closed.
	onRoomBoundShutdown func(RoomTerminationReason)
}

// RoomOptions is a concise alias for RoomRunOptions.
type RoomOptions = RoomRunOptions
