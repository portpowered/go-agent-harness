// Package sessionlive owns the provider-neutral live session loop boundary.
// Hosts select providers, tools, media endpoints, and presentation; this
// service owns the loop, timing, drain, and terminal join ordering.
package sessionlive

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var (
	// ErrMaxDurationExpired is the private-loop deadline sentinel retained for
	// host adapters that classify an internal retry boundary.
	ErrMaxDurationExpired = errors.New("session max duration expired")
	// ErrScheduledAudioIncomplete intentionally aliases the established public
	// session contract so older hosts retain errors.Is identity.
	ErrScheduledAudioIncomplete    = session.ErrLiveScheduledAudioIncomplete
	ErrScheduledAudioConfigTimeout = errors.New("scheduled audio session timed out awaiting session.updated")
)

type ScheduledAudioIncompleteError = session.LiveScheduledAudioIncompleteError

// Loop is the concrete agent-loop surface used by the live service. The alias
// keeps embedders from importing the loop module merely to consume a loop
// returned by this service.
type Loop = agentloop.AgentLoop

type Message = messages.Message
type StreamMessage = messages.StreamMessage
type StreamMessageType = messages.StreamMessageType
type Session = messages.Session
type SessionInferencer = messages.SessionInferencer
type ToolDefinition = messages.ToolDefinition
type ToolExecutor = messages.ToolExecutor
type SessionUpdateConfig = messages.SessionUpdateConfig
type SessionSendOutcome = messages.SessionSendOutcome
type StreamBuffer = messages.TypedBuffer[StreamMessage]

const (
	StreamTypeSessionOpen    = messages.StreamTypeSessionOpen
	StreamTypeSessionCreated = messages.StreamTypeSessionCreated
	StreamTypeSessionUpdated = messages.StreamTypeSessionUpdated
	StreamTypeTextDelta      = messages.StreamTypeTextDelta
	StreamTypeMessageEnd     = messages.StreamTypeMessageEnd
)

// StreamBuffers is the host-neutral factory for typed stream buffers. A
// method keeps the root contract declarative while still allowing an external
// consumer to construct a messages-compatible session fixture.
type StreamBuffers struct{}

func (StreamBuffers) New(capacity int) *StreamBuffer {
	return messages.NewTypedBuffer[StreamMessage](capacity)
}

// ToolAcknowledgementPolicy controls the optional progress acknowledgement
// for long-running tool batches. The runtime receives only a value policy; it
// does not discover or construct tools.
type ToolAcknowledgementPolicy struct {
	Threshold     time.Duration
	IsLongRunning func(string) bool
}

// SessionCapabilities exposes optional provider capabilities without adding
// free functions to the service contract root.
type SessionCapabilities struct{}

func (SessionCapabilities) RequestResponse(ctx context.Context, session Session) SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, session)
}

func (SessionCapabilities) SupportsResponseRequests(session Session) bool {
	return messages.SupportsSessionResponseRequests(session)
}

func (SessionCapabilities) WaitForFirstTurn(ctx context.Context, ack <-chan error, source platformclock.Source, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if source == nil {
		source = platformclock.Real{}
	}
	timerSource, err := platformclock.RequireTimerSource(source)
	if err != nil {
		return err
	}
	timer := timerSource.NewTimer(timeout)
	if timer == nil {
		return errors.New("session live clock returned a nil timer")
	}
	defer timer.Stop()
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return errors.New("timed out awaiting session first user turn acceptance")
	}
}

// LoopOptions are the provider-neutral inputs needed to construct one duplex
// loop. Slices are cloned before they reach the loop so callers may reuse or
// mutate their request-owned values after construction.
type LoopOptions struct {
	Inferencer               SessionInferencer
	Audio                    *audiosubsystem.Subsystem
	SessionConfig            *SessionUpdateConfig
	ToolExecutor             ToolExecutor
	ToolDefinitions          []ToolDefinition
	AdvertiseToolDefinitions bool
	ToolAcknowledgement      *ToolAcknowledgementPolicy
}

// Lifecycle is the small set of provider-side observations needed by the
// generic termination boundary. Provider construction and session ownership
// remain outside this package.
type Lifecycle struct {
	Done          <-chan struct{}
	ConnectError  func() error
	SessionError  func() error
	DrainPlayback func(context.Context) error
}

// MessageContext carries lifecycle state that is otherwise easy for a host
// adapter to infer incorrectly during a terminal race.
type MessageContext struct {
	SessionDone      <-chan struct{}
	Deadline         <-chan time.Time
	AwaitingResponse bool
}

// MessageResult is returned by a host's message observer. The observer owns
// rendering and caller-specific policy; the service owns the resulting
// terminal cleanup and optional playback drain.
type MessageResult struct {
	Stop          bool
	DrainPlayback bool
}

// MessageHandler observes one published stream delta. It must not perform
// terminal cleanup itself; returning Stop asks the service to enter its one
// shared termination boundary.
type MessageHandler func(context.Context, *Loop, StreamMessage, MessageContext) (MessageResult, error)

// Action is a request-scoped event already adapted by the host. The service
// serializes actions with provider deltas so tool-result admission and audio
// input cannot race the terminal boundary.
type Action func(context.Context, *Loop) error

// ActionSource creates a bounded action stream in the service's run context.
// Implementations must stop producing when ctx is canceled.
type ActionSource func(context.Context) <-chan Action

// RunOptions describes one live loop invocation. All callbacks are optional
// except Loop and Handler. Callbacks are invocation-owned and are never stored
// by the service, so separate runs have independent state.
type RunOptions struct {
	Loop        *Loop
	Lifecycle   Lifecycle
	Handler     MessageHandler
	Output      io.Writer
	Clock       platformclock.Source
	MaxDuration time.Duration
	// DeadlineError lets a host retain an established clean terminal policy;
	// nil returns ErrMaxDurationExpired for direct service consumers.
	DeadlineError func() error

	// Bind runs after the loop is constructed and before Run starts. It is the
	// only setup hook and receives separate run/input cancellation scopes.
	Bind func(runCtx, inputCtx context.Context, loop *Loop) error

	// StartInput is called once after the first SESSION.OPEN delta has been
	// handled. Its channel is joined before terminal cleanup completes.
	StartInput                func(context.Context, *Loop) (<-chan error, error)
	InputDone                 func(error)
	InitiallyAwaitingResponse bool

	FirstTurnAck     <-chan error
	FirstTurnTimeout time.Duration

	RequireSessionUpdated bool
	SessionUpdatedTimeout time.Duration
	SessionUpdatedReady   func() bool
	SessionUpdatedError   func(time.Duration) error

	Done    <-chan struct{}
	DoneErr func() error

	AdmissionClosed   <-chan struct{}
	BoundCancellation <-chan struct{}
	Actions           ActionSource
	ToolLifecycle     ActionSource
	Errors            <-chan error

	OnAdmissionClosed func()
	QuiesceUpstream   func() error
	WaitForStragglers func(context.Context) error
	// StopOwnedResources runs after input cancellation, optional playback
	// drain, and loop cancellation. It closes provider/device bindings owned by
	// the host and returns those cleanup errors for joining.
	StopOwnedResources func(context.Context) error
	JoinProducerErrors func(runErr, inputErr error) error
	AfterFlush         func() error
	// TerminationError preserves host-specific provider classifications while
	// the service retains caller cancellation identity.
	TerminationError func(context.Context, error) error
}

// Service constructs and runs provider-neutral live loops.
type Service interface {
	NewLoop(LoopOptions) (*Loop, error)
	Run(context.Context, RunOptions) error
}
