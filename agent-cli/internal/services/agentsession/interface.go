// Package agentsession defines the application-facing session execution seam.
// The request owns the orchestration inputs while the implementation remains
// private to the services composition root.
package agentsession

import (
	"context"
	"io"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionCancellationIntent is the run-scoped operator cancellation marker.
type SessionCancellationIntent struct{ sigint atomic.Bool }

func NewSessionCancellationIntent() *SessionCancellationIntent { return &SessionCancellationIntent{} }
func (i *SessionCancellationIntent) MarkSIGINT() {
	if i != nil {
		i.sigint.Store(true)
	}
}
func (i *SessionCancellationIntent) SIGINTReceived() bool { return i != nil && i.sigint.Load() }

// SessionDiagnosticRecord is the transport-neutral structured session event.
type SessionDiagnosticRecord struct {
	Event  string
	Fields map[string]string
}

type SessionDiagnosticSink interface {
	RecordSessionDiagnostic(SessionDiagnosticRecord)
}

type SessionDiagnosticFunc func(SessionDiagnosticRecord)

func (f SessionDiagnosticFunc) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	if f != nil {
		f(record)
	}
}

type SessionStreamObserver func(messages.StreamMessage)

type SessionToolDiagnostic struct {
	ToolCallID string
	ToolName   string
	Source     string
	ErrorCode  string
	Error      error
}

type SessionToolDiagnosticSink interface {
	RecordSessionToolDiagnostic(SessionToolDiagnostic)
}

type SessionToolDiagnosticFunc func(SessionToolDiagnostic)

func (f SessionToolDiagnosticFunc) RecordSessionToolDiagnostic(diagnostic SessionToolDiagnostic) {
	if f != nil {
		f(diagnostic)
	}
}

// Request contains the admitted session values needed by the private runtime.
// It deliberately lists the request fields instead of embedding the legacy
// services.SessionRunOptions aggregate. This keeps the transport contract
// stable while the runtime implementation is moved behind this service.
type Request struct {
	RecordPath                    string
	ReplayPath                    string
	ReplayTiming                  string
	Provider                      string
	ProviderProvided              bool
	Model                         string
	ModelProvided                 bool
	NoInputTranscription          bool
	APIKey                        string
	BaseURL                       string
	ConfigDir                     string
	WorkDir                       string
	AllowPaths                    []string
	Prompt                        string
	PromptProvided                bool
	Voice                         string
	ReasoningEffort               string
	AudioOutputRequested          bool
	RecordSessionCapturePath      string
	Transport                     string
	TransportProvided             bool
	BareLive                      bool
	Signaling                     string
	SignalingEndpoint             string
	MediaSource                   string
	BrowserToolsEnabled           bool
	BrowserToolsInteractive       bool
	LoadedConfig                  *config.Config
	CancellationIntent            *SessionCancellationIntent
	ToolExecutionTimeout          time.Duration
	Diagnostics                   SessionDiagnosticSink
	ToolDiagnostics               SessionToolDiagnosticSink
	StreamObserver                SessionStreamObserver
	AudioInTurnBarge              bool
	ClientOwnsAudioTurnBoundaries bool
	SessionUpdatedTimeout         time.Duration
	WaitForClose                  bool
	AudioInput                    AudioInput
	AudioTurns                    []string
	AudioInterrupts               []string
	AudioInterruptTool            string
	AudioInputDevice              string
	AudioOutputDevice             string
	AudioInputDevicePresent       bool
	AudioOutputDevicePresent      bool
	AudioDeviceServer             string
	InteractiveDevices            bool
	FeedbackWarningWriter         io.Writer
	TraceAudio                    bool
	RecordDirectory               string
	AudioOutputPath               string
	MaxDuration                   time.Duration
	TextSeed                      TextSeed
	SystemPrompt                  string
	ImagePaths                    []string
	ComputerUse                   bool
	ExperimentalTools             bool
	NoTerminalTools               bool
}

// AudioInput describes the user supplied finite audio source. It contains
// transport values and presence bits only; source and loop callbacks remain
// private runtime seams owned by the session implementation.
type AudioInput struct {
	Path               string
	Stdin              io.Reader
	SourceSampleRate   int
	CloseStdinOnCancel bool
	MaxDuration        time.Duration
	Present            bool
	DevicePresent      bool
}

// TextSeed preserves whether the caller supplied an explicit prompt, including
// an intentionally empty value.
type TextSeed struct {
	Value   string
	Present bool
}

// SessionService executes one admitted session request and writes command-visible
// output to out. Implementations own the session mode dispatch and runtime
// orchestration; callers only provide request values and an output sink.
type SessionService interface {
	Run(context.Context, io.Writer, Request) error
}

// Service is retained as the concise spelling used by service providers.
type Service = SessionService
