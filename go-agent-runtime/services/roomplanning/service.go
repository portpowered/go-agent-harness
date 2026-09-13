// Package roomplanning owns the policy boundary for room participant
// construction and admission.  It deliberately exposes data and function
// ports rather than CLI or provider implementation types.
package roomplanning

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	ErrFilesystemScope      errorCode = "room planning filesystem scope is unavailable"
	ErrSessionFactory       errorCode = "room planning session factory is unavailable"
	ErrReplayPlanner        errorCode = "room planning replay planner is unavailable"
	ErrParticipantTools     errorCode = "room participant tools are unavailable"
	ErrParticipantToolMatch errorCode = "room participant tool capabilities do not match the manifest"
	ErrParticipantBrowser   errorCode = "room participant browser capabilities are unavailable"
	ErrBrowserCapability    errorCode = "room participant browser capabilities do not match the contract"
)

const (
	DefaultAdmissionTimeout = 5 * time.Second
	DefaultCleanupTimeout   = time.Second
)

// FilesystemScope is the immutable, credential-free projection of the host's
// filesystem policy.  A CLI can retain its richer policy object privately
// while the room planner only needs the canonical roots to build session
// options.
type FilesystemScope struct {
	PrimaryRoot     string
	AdditionalRoots []string
}

// FilesystemResolver constructs one immutable scope from the customer input.
// The resolver is a host seam: the planner owns when and how often it is
// called, while a host owns platform-specific protected-root policy.
type FilesystemResolver func(workDir string, allowPaths []string) (FilesystemScope, error)

// SessionOptions is the host-neutral portion of a participant session
// request.  The resolved credential is intentionally absent; it is supplied
// only to LiveSessionFactory for the duration of construction.
type SessionOptions struct {
	Provider                 string
	Model                    string
	ModelProvided            bool
	BaseURL                  string
	ConfigDir                string
	WorkDir                  string
	AllowPaths               []string
	Filesystem               FilesystemScope
	Prompt                   string
	PromptProvided           bool
	Voice                    string
	ReplayPath               string
	RecordSessionCapturePath string
	RoomReplay               bool
	WaitForClose             bool

	WebSocketDialer transport.Dialer
	ToolExecutor    messages.ToolExecutor
	ToolDefinitions []messages.ToolDefinition

	// BrowserWatch and BrowserEventWatch are intentionally opaque to keep this
	// contract independent of a particular browser implementation.  The CLI
	// adapter restores its concrete event types before handing the options to
	// the existing session runtime.
	BrowserWatch       any
	BrowserEventWatch  any
	BrowserTools       bool
	RefreshTools       func(context.Context) ([]messages.ToolDefinition, error)
	ToolDefinitionBase []messages.ToolDefinition
	CapabilityClose    func() error
}

// LiveSessionRequest is the only boundary at which a resolved credential is
// visible.  It is never retained in a ParticipantPlan or returned by Plan.
type LiveSessionRequest struct {
	Participant rooms.Participant
	Options     SessionOptions
	Credential  string
}

type LiveSessionFactory func(LiveSessionRequest) (messages.SessionInferencer, error)

// ReplaySession contains the strict replay lifecycle signals needed by room
// orchestration.  The planner never consults live credentials or factories
// on this path.
type ReplaySession struct {
	Inferencer  messages.SessionInferencer
	Done        <-chan struct{}
	DoneErr     func() error
	MaxDuration time.Duration
}

type ReplayRequest struct {
	Participant rooms.Participant
	Recorded    rooms.RoomReplayParticipant
	Options     SessionOptions
}

type ReplaySessionPlanner func(context.Context, ReplayRequest) (ReplaySession, error)

type ToolCapabilities struct {
	Executor    messages.ToolExecutor
	Definitions []messages.ToolDefinition
}

type ToolCapabilitiesFactory func(rooms.Participant) (ToolCapabilities, error)

type BrowserCapabilities struct {
	Executor               messages.ToolExecutor
	Definitions            []messages.ToolDefinition
	ToolDefinitionBase     []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	BrowserWatch           any
	BrowserEventWatch      any
	Initialize             func(context.Context) error
	Close                  func() error
}

// BrowserCapabilitiesFactory receives the already validated static tool
// surface.  Returning a composed surface keeps browser state participant-
// local and prevents dynamic page tools from discarding static tools.
type BrowserCapabilitiesFactory func(rooms.Participant, ToolCapabilities) (BrowserCapabilities, error)

type ParticipantPlan struct {
	Participant          rooms.Participant
	Options              SessionOptions
	Inferencer           messages.SessionInferencer
	StartupErr           error
	Replay               bool
	ReplaySession        ReplaySession
	InputAudioSampleRate int
}

type PlanResult struct {
	Plans []*ParticipantPlan
}

// Options contains all policy inputs for one planning operation.  Callbacks
// are copied by value and are never retained after Plan returns.
type Options struct {
	Manifest   rooms.Manifest
	ReplayPlan *rooms.RoomReplayPlan

	LookupCredential       func(string) (string, bool)
	Filesystem             *FilesystemScope
	ResolveFilesystem      FilesystemResolver
	WorkDir                string
	AllowPaths             []string
	ConfigDir              string
	BaseURL                string
	WebSocketDialer        transport.Dialer
	WebSocketDialerFactory func(rooms.Participant) transport.Dialer

	SessionFactory     LiveSessionFactory
	SessionInferencers map[string]messages.SessionInferencer
	ReplayPlanner      ReplaySessionPlanner
	ToolFactory        ToolCapabilitiesFactory
	BrowserFactory     BrowserCapabilitiesFactory
	ResolveSampleRate  func(SessionOptions, messages.SessionInferencer) (int, error)
	ResolveCapturePath func(participantID string) (string, bool)

	// ParticipantError lets the host retain its typed, redacted error wrapper
	// without moving that wrapper or its secrets into the public runtime.
	ParticipantError             func(participantID string, err error) error
	ResolveSampleRateForInjected bool
}

// Service is the public room-planning contract.  Implementations are safe to
// construct independently and keep all planning policy private.
type Service interface {
	Plan(context.Context, Options) (PlanResult, error)
	Await(context.Context, AwaitOptions) error
}
