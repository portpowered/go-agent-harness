// Package rooms is the application-facing room service contract. Room
// orchestration, provider sessions, and audio devices remain private to the
// service implementation; callers exchange admitted values and observations.
package rooms

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

type Service interface {
	Run(context.Context, io.Writer, RoomRunOptions) (RoomResult, error)
	ResolveLaunchPlan(RoomLaunchOptions) (RoomLaunchPlan, error)
	LoadReplayPlan(string) (RoomReplayPlan, error)
	ValidateReplayOutput(RoomReplayPlan, string) error
	ValidateEvidenceOutput(string) error
	CreateFreshRunDirectory(string) (string, error)
	NewEventBroker([]string) (EventBroker, error)
}

type EventBroker interface {
	http.Handler
	Close() error
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
	Kind          room.ParticipantKind
	InputDevice   string
	OutputDevice  string
	Provider      string
	Model         string
}

type BrowserCapabilities struct {
	Executor               messages.ToolExecutor
	Definitions            []messages.ToolDefinition
	ToolDefinitionBase     []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	BrowserWatch           func(context.Context) <-chan webmcp.BrokerEvent
	Initialize             func(context.Context) error
	Close                  func() error
}

type BrowserCapabilitiesFactory func(room.Participant) (BrowserCapabilities, error)

type RoomRunOptions struct {
	Manifest   room.Manifest
	ReplayPath string
	ReplayPlan *RoomReplayPlan
	LaunchPlan *RoomLaunchPlan
	OutputDir  string
	ConfigDir  string
	WorkDir    string
	AllowPaths []string
	Stream     EventBroker

	BrowserCapabilitiesFactory BrowserCapabilitiesFactory
	OnDiagnostic               func(string, agentsession.SessionDiagnosticRecord)
	OnParticipantReady         func(RoomParticipantReady)
	OnParticipantTerminated    func(RoomParticipantResult)
}

type RoomLaunchMode string

const (
	RoomLaunchModeBare       RoomLaunchMode = "bare"
	RoomLaunchModeConfigured RoomLaunchMode = "configured"
)

type RoomLaunchParticipantPlan struct {
	ID                   string
	Kind                 room.ParticipantKind
	InputDevice          string
	OutputDevice         string
	Provider             string
	Model                string
	CredentialReference  string
	CredentialProvenance string
}

type RoomLaunchPlan struct {
	Mode         RoomLaunchMode
	ConfigPath   string
	ConfigDir    string
	Manifest     room.Manifest
	Participants []RoomLaunchParticipantPlan
}

type RoomLaunchOptions struct {
	ConfigPath       string
	ManifestPath     string
	ConfigDir        string
	CredentialLookup func(string) (string, bool)
}

type RoomReplayPlan struct {
	BundlePath   string
	ManifestData room.Manifest
}

func (p RoomReplayPlan) Manifest() room.Manifest { return p.ManifestData }

var (
	ErrRoomReplaySourceConflict   = errors.New("room replay source conflict")
	ErrRoomReplayBundleIncomplete = errors.New("room replay bundle incomplete")
	ErrInvalidRoomReplayBundle    = errors.New("invalid room replay bundle")
	ErrRoomLaunchPathConflict     = errors.New("room launch config and manifest paths conflict")
)

const (
	DefaultRoomOutputDir           = "room-run"
	SessionDiagnosticEventTurn     = agentsession.SessionDiagnosticEventTurn
	SessionDiagnosticEventToolCall = agentsession.SessionDiagnosticEventToolCall
	SessionDiagnosticEventFailure  = agentsession.SessionDiagnosticEventFailure
)
