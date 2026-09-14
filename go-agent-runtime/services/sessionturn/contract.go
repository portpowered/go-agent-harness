// Package sessionturn owns the complete runtime boundary for persistent
// session turns. Hosts provide already-resolved dependencies; turn state,
// prompt shaping, text-seed handling, tool execution policy, and publication
// lifecycle remain private to this service.
package sessionturn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type TurnDirection string
type TurnEventType string

type TurnEvent struct {
	Type                     TurnEventType
	Index                    uint64
	Direction                TurnDirection
	Tick, StartTick, EndTick uint64
}

type TurnEventSink func(TurnEvent)

type TurnInput struct {
	Text        string
	TextPresent bool
	Audio       []byte
	MediaType   string
}

func (in TurnInput) Empty() bool {
	return strings.TrimSpace(in.Text) == "" && len(in.Audio) == 0
}

type SessionTurn struct {
	Index, StartTick, EndTick uint64
	Direction                 TurnDirection
	Input                     TurnInput
	Response                  messages.Message
}

type TurnRequest struct {
	Input     TurnInput
	Direction TurnDirection
	StartTick uint64
	EndTick   uint64
}

type TurnResult struct {
	Turn SessionTurn
}

type Seed struct {
	Value   string
	Present bool
}

type Allocator interface{ Allocate() string }
type AllocatorFunc func() string

func (f AllocatorFunc) Allocate() string { return f() }

const (
	WirePromptPrefix = "\x00agent-cli-session-text-seed:"
	ReceiveCapacity  = 256
)

type Output interface {
	io.Writer
	Err() error
}

// ServiceOwnedToolExecutor marks the executor returned by a prepared
// session-turn runtime so hosts do not wrap its policy and lifecycle state a
// second time when constructing compatibility loops.
type ServiceOwnedToolExecutor interface {
	SessionTurnToolExecutor()
}

type CompleteMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}
type CompleteMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}
type CompleteMessageCapabilities interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}
type TerminalErrorSource interface{ TerminalError() error }

type Session interface {
	messages.Session
	messages.SessionSendOutcomeSender
	messages.SessionResponseRequester
	messages.SessionResponseCapability
	CompleteMessageSender
	CompleteMessageWithoutResponseSender
	CompleteMessageCapabilities
	TerminalErrorSource
}

type BrowserEventType string

const (
	BrowserEventSelectionChanged  BrowserEventType = "selection_changed"
	BrowserEventCatalogChanged    BrowserEventType = "catalog_changed"
	BrowserEventGenerationChanged BrowserEventType = "generation_changed"
)

type BrowserEvent struct {
	Type       BrowserEventType
	BrowserID  string
	TargetID   string
	Generation uint64
	Sequence   uint64
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}
type TimerFactory interface{ NewTimer(time.Duration) Timer }

type BrowserRequest struct {
	Watch        func(context.Context) <-chan BrowserEvent
	Refresh      func(context.Context) ([]messages.ToolDefinition, error)
	TimerFactory TimerFactory
}

type PublicationLifecycle string

const (
	PublicationStarting PublicationLifecycle = "starting"
	PublicationWaiting  PublicationLifecycle = "waiting"
	PublicationReady    PublicationLifecycle = "ready"
	PublicationStopped  PublicationLifecycle = "stopped"
	PublicationFailed   PublicationLifecycle = "failed"
)

type PublicationState struct {
	Lifecycle        PublicationLifecycle
	LastSequence     uint64
	PublicationCount uint64
	DefinitionDigest string
	BrowserID        string
	TargetID         string
	Generation       uint64
	Err              error
}

type PublicationRequest struct {
	BaseDefinitions    []messages.ToolDefinition
	InitialDefinitions []messages.ToolDefinition
	Browser            BrowserRequest
	Publish            func(context.Context, []messages.ToolDefinition) error
}

type Publication interface {
	MarkReady()
	Errors() <-chan error
	State() PublicationState
	Stop()
}

type sentinelError string

func (e sentinelError) Error() string { return string(e) }

const ErrPublication sentinelError = "session turn tool publication failed"

type PublicationError struct {
	Phase    string
	Sequence uint64
	Err      error
}

func (e *PublicationError) Error() string {
	if e == nil {
		return ErrPublication.Error()
	}
	return fmt.Sprintf("%s: phase=%s sequence=%d: %s", ErrPublication, e.Phase, e.Sequence, e.Err)
}
func (e *PublicationError) Unwrap() error {
	if e == nil {
		return ErrPublication
	}
	return errors.Join(ErrPublication, e.Err)
}

type InstructionRequest struct {
	Service     session.InstructionService
	Request     session.InstructionRequest
	Composition *session.InstructionComposition
}

type ImageCapabilities struct {
	Model                   string
	SupportsImageInput      bool
	SupportedInputMIMETypes []string
}

type ImageRequest struct {
	Parts               []messages.ImagePart
	DeferResponse       bool
	FirstTurn           chan error
	PromptSentinel      string
	DeferredInstruction string
}

const (
	ImageOnlyPrompt          = "\x00agent-session-image-turn\x00"
	DeferredImageInstruction = "Use the attached image to answer the user's next spoken question."
)

const (
	ErrImageMissingFile     sentinelError = "session image file is missing"
	ErrImageUnreadableFile  sentinelError = "session image file is unreadable"
	ErrImageUnsupportedMIME sentinelError = "session image MIME type is unsupported"
	ErrImageInvalidContent  sentinelError = "session image content is invalid"
	ErrImageEmptyFile       sentinelError = "session image file is empty"
	ErrImageCapability      sentinelError = "session image capability is unsupported"
	ErrImageSend            sentinelError = "session image turn could not be sent"
)

type ImageCapabilityError struct{ Model, Capability string }

func (e *ImageCapabilityError) Error() string {
	return fmt.Sprintf("model %q does not support %s capability", e.Model, e.Capability)
}
func (*ImageCapabilityError) Unwrap() error { return ErrImageCapability }

type ImageFileError struct {
	Path, DetectedMIME string
	SupportedMIME      []string
	Cause, Kind        error
	Message            string
}

func (e *ImageFileError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}
func (e *ImageFileError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.Kind, e.Cause}
}

type ImageEmptyFileError struct{ Path string }

func (e *ImageEmptyFileError) Error() string { return fmt.Sprintf("session image %q is empty", e.Path) }
func (*ImageEmptyFileError) Unwrap() error   { return ErrImageEmptyFile }

type Request struct {
	SessionInferencer messages.SessionInferencer
	EventSink         TurnEventSink

	Seed             Seed
	SeedAllocator    Allocator
	Instructions     InstructionRequest
	InstructionsText string
	Image            *ImageRequest

	ToolExecutor          messages.ToolExecutor
	ToolDefinitions       []messages.ToolDefinition
	ToolDefinitionBase    []messages.ToolDefinition
	InteractiveToolPolicy tools.InteractiveToolPolicy
	ToolExecutionTimeout  time.Duration
	ToolCallObserver      func(messages.ToolCall)
	ToolResultObserver    func(messages.ToolCall, messages.ToolCallResponse, bool)
	ToolDiagnostic        func(messages.ToolCall, error)
	ToolFailurePresenter  func(messages.ToolCall, error) messages.ToolCallResponse

	Browser BrowserRequest
	Output  io.Writer
}

type Runtime interface {
	Inferencer() messages.SessionInferencer
	WirePrompt() string
	ToolExecutor() messages.ToolExecutor
	ToolDefinitions() []messages.ToolDefinition
	InteractiveToolPolicy() tools.InteractiveToolPolicy
	RunTurn(context.Context, TurnRequest) (TurnResult, error)
	History() []SessionTurn
	PublicationState() PublicationState
	StartPublication(context.Context, PublicationRequest) (Publication, error)
	NewOutput(io.Writer) Output
	Close() error
}

type Service interface {
	Prepare(context.Context, Request) (Runtime, error)
	PrepareImageParts([]string, ImageCapabilities) ([]messages.ImagePart, error)
	SendImageTurn(context.Context, messages.Session, string, []messages.ImagePart, bool) error
}

const (
	TurnDirectionUser           TurnDirection = "user"
	TurnDirectionAssistant      TurnDirection = "assistant"
	TurnDirectionClientToServer TurnDirection = "client_to_server"
	TurnDirectionServerToClient TurnDirection = "server_to_client"
	TurnEventStart              TurnEventType = "turn-start"
	TurnEventEnd                TurnEventType = "turn-end"
)

type ErrorCode string

func (e ErrorCode) Error() string { return string(e) }

const (
	ErrTurnAlreadyActive          ErrorCode = "turn start while another turn is active"
	ErrTurnEndWithoutStart        ErrorCode = "turn end without start: no active turn"
	ErrEmptyTurn                  ErrorCode = "turn content must not be empty"
	ErrInvalidTurnDirection       ErrorCode = "turn direction is invalid"
	ErrInvalidTurnTick            ErrorCode = "turn tick must be strictly increasing"
	ErrSessionEndedWithActiveTurn ErrorCode = "session ended with active turn"
	ErrSessionClosed              ErrorCode = "session is closed"
	ErrTurnMismatch               ErrorCode = "turn does not match the active turn"
	ErrMissingTurnInferencer      ErrorCode = "session turn inferencer is not configured"
	ErrMissingTurnSession         ErrorCode = "session turn provider returned no session"
	ErrSessionResponse            ErrorCode = "session returned an error"
	ErrTurnInputRejected          ErrorCode = "session rejected turn input"
	ErrTurnInputCommitRejected    ErrorCode = "session rejected turn input commit"
)
