package rooms

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

// Service is the public room lifecycle and admission contract. Implementations
// keep participant sessions, device handles, evidence writers, and schedulers
// private to the service graph.
type Service interface {
	Run(context.Context, io.Writer, RoomRunOptions) (RoomResult, error)
	ResolveLaunchPlan(RoomLaunchOptions) (RoomLaunchPlan, error)
	LoadReplayPlan(string) (RoomReplayPlan, error)
	ValidateReplayOutput(RoomReplayPlan, string) error
	ValidateEvidenceOutput(string) error
	CreateFreshRunDirectory(string) (string, error)
}

// RoomDiagnosticRecord is the transport-neutral diagnostic event forwarded to
// a host. Fields contain bounded, non-secret metadata.
type RoomDiagnosticRecord struct {
	Event  string
	Fields map[string]string
	At     time.Time
}

type BrowserEvent struct {
	Type               string
	Sequence           uint64
	At                 time.Time
	BrowserID          string
	TargetID           string
	Generation         uint64
	PreviousGeneration uint64
	InvocationID       string
	ToolName           string
	State              string
	Status             string
	ErrorCode          string
	Reason             string
	CatalogReady       bool
	ToolCount          int
	ToolCountKnown     bool
}

// BrowserCapabilities are injected by the host's browser composition. Room
// policy only admits and closes the capability; it never discovers a browser.
type BrowserCapabilities struct {
	Executor               messages.ToolExecutor
	Definitions            []messages.ToolDefinition
	ToolDefinitionBase     []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	BrowserWatch           func(context.Context) <-chan BrowserEvent
	Initialize             func(context.Context) error
	Close                  func() error
}

type BrowserCapabilitiesFactory func(Participant) (BrowserCapabilities, error)

// EventSink receives the ordered, bounded live observations for one room.
// Implementations must return promptly; lifecycle owns the bounded delivery
// queue and reports a sink error through the room terminal result.
type EventSink interface {
	Publish(context.Context, string, session.LiveEvent) error
}

// MediaCapture and MediaPlayback are local PCM worker capabilities. The
// implementations own framing, resampling, pacing, interruption epochs, and
// device callbacks; the room service only starts and joins their lifetimes.
// go-audio's canonical media endpoints are the provider side of these pumps.
type MediaCapture interface {
	audio.MediaEndpoint
	Pump(context.Context, audio.OutboundMedia) error
}

type MediaPlayback interface {
	audio.MediaEndpoint
	Pump(context.Context, audio.InboundMedia) error
}

// MediaPorts are the local PCM endpoints attached to one room participant.
// The room service owns the bridge lifetime; a host owns the concrete device
// implementation and returns all resources through CloseFunc.
//
// Capture and Playback are optional when a host intentionally provides
// one-way media.
type MediaPorts struct {
	Capture   MediaCapture
	Playback  MediaPlayback
	CloseFunc func() error
}

// AudioFormat is the negotiated provider-side cadence used by the room mesh.
// Device workers may convert from local hardware formats at MediaPorts; room
// orchestration never performs that conversion itself.
type AudioFormat = mixer.Format

func (p MediaPorts) Close() error {
	if p.CloseFunc == nil {
		return nil
	}
	return p.CloseFunc()
}

// MediaFactory resolves participant device selectors at the room admission
// boundary. The negotiated format is supplied for every admission so a host
// cannot silently attach workers at a stale process default. Implementations
// belong to a host or device service; rooms never discover devices or import a
// device backend.
type MediaFactory interface {
	OpenMedia(context.Context, Participant, AudioFormat) (MediaPorts, error)
}

// MediaFactoryFunc adapts a function to MediaFactory.
type MediaFactoryFunc func(context.Context, Participant, AudioFormat) (MediaPorts, error)

func (f MediaFactoryFunc) OpenMedia(ctx context.Context, participant Participant, format AudioFormat) (MediaPorts, error) {
	if f == nil {
		return MediaPorts{}, ErrRoomServiceUnavailable
	}
	return f(ctx, participant, format)
}

type RoomRunOptions struct {
	Manifest    Manifest
	ReplayPath  string
	ReplayPlan  *RoomReplayPlan
	LaunchPlan  *RoomLaunchPlan
	OutputDir   string
	ConfigDir   string
	WorkDir     string
	AllowPaths  []string
	AudioFormat AudioFormat

	BrowserCapabilitiesFactory BrowserCapabilitiesFactory
	// LiveCapabilitiesFactory creates participant-local tool bindings for the
	// continuous session owner. It is invoked only for admitted agent
	// participants and its returned Close function is owned by the live handle.
	LiveCapabilitiesFactory session.LiveCapabilityFactory
	OnDiagnostic            func(string, RoomDiagnosticRecord)
	EventSink               EventSink
	OnParticipantReady      func(RoomParticipantReady)
	OnParticipantTerminated func(RoomParticipantResult)
}

type RoomTerminationReason string

const (
	RoomTerminationStopped            RoomTerminationReason = "stopped"
	RoomTerminationMaxTurnsReached    RoomTerminationReason = "max_turns_reached"
	RoomTerminationMaxDurationReached RoomTerminationReason = "max_duration_reached"
	RoomTerminationFailed             RoomTerminationReason = "failed"
)

type ParticipantTerminationReason string

const (
	ParticipantTerminationEnded        ParticipantTerminationReason = "ended"
	ParticipantTerminationDisconnected ParticipantTerminationReason = "disconnected"
	ParticipantTerminationError        ParticipantTerminationReason = "error"
)

type RoomParticipantResult struct {
	ID                     string
	ParticipantID          string
	TerminationReason      ParticipantTerminationReason
	Reason                 ParticipantTerminationReason
	TerminationTrigger     string
	TerminationDisposition string
	Classification         string
	TerminalReason         string
	TerminalProvenance     string
	OutputState            string
	TurnsCompleted         int
	Connected              bool
	Error                  string
	RecordingStatus        *transcript.RecordingStatus
}

type RoomResult struct {
	TerminationReason  RoomTerminationReason
	Reason             RoomTerminationReason
	Participants       map[string]RoomParticipantResult
	ActiveParticipants []string
	Error              string
	RecordingStatus    *transcript.RecordingStatus
	DegradedArtifacts  map[string]string
}

type RoomParticipantReady struct {
	ID            string
	ParticipantID string
	Kind          ParticipantKind
	InputDevice   string
	OutputDevice  string
	Provider      string
	Model         string
}

type RoomLaunchMode string

const (
	RoomLaunchModeBare       RoomLaunchMode = "bare"
	RoomLaunchModeConfigured RoomLaunchMode = "configured"
)

type RoomCredentialProvenance string

const (
	RoomCredentialFromEnvironment RoomCredentialProvenance = "environment"
	RoomCredentialFromConfig      RoomCredentialProvenance = "config"
)

type RoomLaunchParticipantPlan struct {
	ID                   string
	Kind                 ParticipantKind
	InputDevice          string
	OutputDevice         string
	Provider             string
	Model                string
	CredentialReference  string
	CredentialProvenance RoomCredentialProvenance
}

type RoomLaunchPlan struct {
	Mode         RoomLaunchMode
	ConfigPath   string
	ConfigDir    string
	Manifest     Manifest
	Participants []RoomLaunchParticipantPlan
}

func (p RoomLaunchPlan) Participant(id string) (RoomLaunchParticipantPlan, bool) {
	for _, participant := range p.Participants {
		if participant.ID == id {
			return participant, true
		}
	}
	return RoomLaunchParticipantPlan{}, false
}

type RoomLaunchOptions struct {
	ConfigPath       string
	ManifestPath     string
	ConfigDir        string
	CredentialLookup func(string) (string, bool)
}

const DefaultRoomOutputDir = "room-run"
