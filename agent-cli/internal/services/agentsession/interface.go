// Package agentsession defines the application-facing session execution seam.
// The request owns the orchestration inputs while the implementation remains
// private to the services composition root.
package agentsession

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type HoldToneConfig = audio.HoldToneConfig

type SessionCancellationIntent = sessiontrace.CancellationIntent
type SessionDiagnosticRecord = sessiontrace.DiagnosticRecord
type SessionDiagnosticSink = sessiontrace.DiagnosticSink
type SessionDiagnosticFunc = sessiontrace.DiagnosticFunc
type SessionStreamObserver = sessiontrace.StreamObserver
type SessionToolDiagnostic = sessiontrace.ToolDiagnostic
type SessionToolDiagnosticSink = sessiontrace.ToolDiagnosticSink
type SessionToolDiagnosticFunc = sessiontrace.ToolDiagnosticFunc

func NewSessionCancellationIntent() SessionCancellationIntent {
	return sessiontracewire.NewCancellationIntent()
}

// Request contains the admitted session values needed by the private runtime.
// It deliberately lists the request fields instead of embedding the legacy
// services.SessionRunOptions aggregate. This keeps the transport contract
// stable while the runtime implementation is moved behind this service.
type Request struct {
	RecordPath                    string
	ReplayPath                    string
	ReplayTiming                  string
	MetricsRecorder               metrics.Recorder
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
	CancellationIntent            sessiontrace.CancellationIntent
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
	HoldToneConfig                *audio.HoldToneConfig
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
