// Package providersession owns provider-specific realtime session planning.
// Hosts provide resolved values and transport seams; CLI/config policy is not
// part of this contract.
package providersession

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	ProviderOpenAI   = "openai"
	ProviderGrok     = "grok"
	ModeRecordOpenAI = "record-openai"
	ModeRecordGrok   = "record-grok"
	ModeReplayOpenAI = "replay-openai-websocket"
	ModeReplayGrok   = "replay-grok-websocket"
)

// ErrorCode is a comparable, immutable error identity suitable for wrapping
// from provider-session implementations and matching with errors.Is.
type ErrorCode string

func (e ErrorCode) Error() string { return string(e) }
func (e ErrorCode) Is(target error) bool {
	return target != nil && target.Error() == e.Error()
}

const (
	// ErrMissingDialer identifies a request that cannot select an owned
	// websocket transport.
	ErrMissingDialer ErrorCode = "session runtime requires an injected websocket dialer"
	// ErrAudioSampleRateConflict identifies a capture whose declared input and
	// output rates cannot be represented by one provider session.
	ErrAudioSampleRateConflict ErrorCode = "session input and output sample rates conflict"
)

// RecordingDialer is the provider wire recorder needed by a record plan.
type RecordingDialer interface {
	transport.Dialer
	FlushToFile(string) error
}

// ReplayDialer is the strict capture transport needed by a replay plan.
type ReplayDialer interface {
	transport.Dialer
	Done() <-chan struct{}
	Err() error
	Model() string
}

// Dependencies are transport seams supplied by the application composition
// root. Nil factories select the built-in provider/testing implementations.
type Dependencies struct {
	NewDefaultDialer     func(string) transport.Dialer
	NewRecordingDialer   func(transport.Dialer, string, string) RecordingDialer
	NewReplayDialer      func(string, string) (ReplayDialer, error)
	NewInferencer        func(BuildRequest) (messages.SessionInferencer, error)
	WrapReplayInferencer func(messages.SessionInferencer) messages.SessionInferencer
}

// Service plans and builds provider sessions from explicit values.
type Service interface {
	PlanRecord(context.Context, RecordRequest) (Plan, error)
	PlanReplay(context.Context, ReplayRequest) (Plan, error)
	BuildOpenAI(context.Context, BuildRequest) (messages.SessionInferencer, error)
	BuildGrok(context.Context, BuildRequest) (messages.SessionInferencer, error)
}

// AudioInput is one finite PCM turn owned by a session plan.
type AudioInput struct {
	AfterCompletedTurns int
	PCM                 []byte
	SourceSampleRate    int
	EndOfTurn           bool
}

// RecordRequest contains resolved live-session values. AudioInputAvailable
// describes a device-backed input; AudioInputs describes finite scheduled
// turns. The service combines both when selecting transcription and turn
// detection policy.
type RecordRequest struct {
	Provider                      string
	Model                         string
	APIKey                        string
	BaseURL                       string
	ReasoningEffort               string
	Voice                         string
	Prompt                        string
	RecordPath                    string
	WaitForClose                  bool
	AudioInputs                   []AudioInput
	ToolDefinitions               []messages.ToolDefinition
	AudioInputAvailable           bool
	ClientOwnsAudioTurnBoundaries bool
	NoInputTranscription          bool
	InputAudioTranscription       *models.InputAudioTranscriptionConfig
	TurnDetection                 *models.TurnDetectionConfig
	WebSocketDialer               transport.Dialer
	ObserveDialer                 func(transport.Dialer) transport.Dialer
}

// ReplayRequest contains only explicit replay inputs and caller-owned tool
// definitions. The capture's provider handshake and self-driving user turn
// data remain authoritative when the request is bare.
type ReplayRequest struct {
	Provider                      string
	ReplayPath                    string
	ReplayTiming                  string
	Prompt                        string
	PromptProvided                bool
	Voice                         string
	WaitForClose                  bool
	AudioInputs                   []AudioInput
	ClientOwnsAudioTurnBoundaries bool
	RoomReplay                    bool
	ToolDefinitions               []messages.ToolDefinition
}

// BuildRequest is the provider-neutral input to one inferencer constructor.
// Dialer is always selected by a plan or explicitly injected by the host.
type BuildRequest struct {
	Provider                      string
	Model                         string
	APIKey                        string
	BaseURL                       string
	ReasoningEffort               string
	Voice                         string
	Dialer                        transport.Dialer
	ToolDefinitions               []messages.ToolDefinition
	InputAudioTranscription       models.InputAudioTranscriptionConfig
	ClientOwnsAudioTurnBoundaries bool
	InitialSessionUpdate          []byte
}

// Plan is the provider-session decision record consumed by a host loop. The
// callbacks are lifecycle hooks, not hidden goroutines or process state.
type Plan struct {
	Mode                     string
	Provider                 string
	Model                    string
	CapturePath              string
	Announcement             string
	Build                    BuildRequest
	Inferencer               messages.SessionInferencer
	Dialer                   transport.Dialer
	InitialSessionUpdate     []byte
	InputAudioSampleRate     int
	OutputAudioSampleRate    int
	TurnDetection            *models.TurnDetectionConfig
	Prompt                   string
	PromptProvided           bool
	CloseAfterOpen           bool
	WaitForClose             bool
	CloseAfterScheduledAudio bool
	RequireSessionUpdated    bool
	MaxDuration              time.Duration
	Done                     <-chan struct{}
	DoneErr                  func() error
	AudioInputs              []AudioInput
	AnnounceTools            []messages.ToolDefinition
	ReplayComplete           bool
	FlushCapture             func() error
	FlushCaptureTo           func(string) error
	Finalize                 func(context.Context, io.Writer) error
}

// ApplyTurnDetection transfers the plan's provider policy through the narrow
// optional inferencer seam used by the realtime adapters.
func (p Plan) ApplyTurnDetection(inferencer messages.SessionInferencer) {
	if configurer, ok := inferencer.(interface {
		SetSessionTurnDetection(*models.TurnDetectionConfig)
	}); ok {
		configurer.SetSessionTurnDetection(cloneTurnDetection(p.TurnDetection))
	}
}

func cloneTurnDetection(policy *models.TurnDetectionConfig) *models.TurnDetectionConfig {
	if policy == nil {
		return nil
	}
	copy := *policy
	if policy.CreateResponse != nil {
		value := *policy.CreateResponse
		copy.CreateResponse = &value
	}
	if policy.InterruptResponse != nil {
		value := *policy.InterruptResponse
		copy.InterruptResponse = &value
	}
	return &copy
}
