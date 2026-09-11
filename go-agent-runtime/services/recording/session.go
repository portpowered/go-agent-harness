package recording

import (
	"context"
	"encoding/json"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// SessionMessageDirection identifies the runtime boundary at which a stream
// message was observed. It is deliberately transport-neutral.
type SessionMessageDirection = runtimesession.LiveRecordDirection

const (
	SessionMessageFromClient SessionMessageDirection = runtimesession.LiveRecordClient
	SessionMessageFromAgent  SessionMessageDirection = runtimesession.LiveRecordAgent
	// Short aliases keep adapters readable without introducing a second
	// direction vocabulary.
	SessionClientMessage SessionMessageDirection = SessionMessageFromClient
	SessionAgentMessage  SessionMessageDirection = SessionMessageFromAgent
)

// BrowserTool is the provider-neutral portion of a browser catalog event.
// InputSchema is copied before the event is retained.
type BrowserTool struct {
	Name        string
	InputSchema json.RawMessage
}

// BrowserEvent is the host-neutral browser lifecycle projection accepted by a
// recording session. Hosts adapt their browser broker at this boundary; the
// recording package never imports a CLI browser implementation.
type BrowserEvent struct {
	Version            string
	Type               string
	Sequence           uint64
	At                 time.Time
	BrowserID          string
	TargetID           string
	FrameID            string
	Generation         uint64
	PreviousGeneration uint64
	CatalogReady       bool
	ToolCount          int
	ToolCountKnown     bool
	ToolName           string
	Tools              []BrowserTool
	RemovedToolNames   []string
	InvocationID       string
	Status             string
	Input              json.RawMessage
	Output             json.RawMessage
	ErrorCode          string
	Reason             string
}

// BrowserOptions selects the semantic browser evidence projection. EventSource
// is called once with a cancellation context and must return a bounded broker
// subscription. Events is a convenience for already-created finite streams.
type BrowserOptions struct {
	Enabled            bool
	EventSource        func(context.Context) <-chan BrowserEvent
	Events             <-chan BrowserEvent
	IncludeArguments   bool
	IncludeResults     bool
	RedactURLQuery     bool
	RedactURLFragment  bool
	ToolArguments      []string
	ResultJSONPointers []string
	DigestTools        []string
}

// SessionOptions contains only resolved, value-shaped inputs for one
// recording invocation. Destination admission and publication remain owned by
// the runtime recording service.
type SessionOptions struct {
	Destination         string
	SessionID           string
	ParticipantID       string
	Provider            string
	Model               string
	Transport           string
	ClockBase           time.Time
	WallClockStart      time.Time
	Credentials         []string
	Limits              ResourceLimits
	ProviderCapturePath string
	Metadata            transcript.RecordingMetadata
	Browser             BrowserOptions
	AdditionalArtifacts []transcript.RecordingArtifact
}

// SessionRecorder is the complete runtime-owned recording façade. It is
// suitable for both websocket and embedded hosts and contains no CLI policy.
type SessionRecorder interface {
	Wrap(messages.SessionInferencer) messages.SessionInferencer
	ObserveMessage(context.Context, messages.StreamMessage, SessionMessageDirection) error
	ObserveToolCall(context.Context, messages.ToolCall) error
	ObserveToolResult(context.Context, messages.ToolCall, messages.ToolCallResponse, bool) error
	RecordTerminalSummary(transcript.RecordingTerminalSummary) error
	Finalize(context.Context, error) error
}

// RecordingSession and Session are descriptive aliases for the façade.
type RecordingSession = SessionRecorder
type Session = SessionRecorder

// SessionService creates one invocation-owned recording session. Implementations
// must validate and claim the destination during OpenSession.
type SessionService interface {
	OpenSession(SessionOptions) (SessionRecorder, error)
}
