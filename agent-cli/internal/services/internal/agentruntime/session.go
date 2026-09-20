package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// RunSession validates and runs the session inference command surface.
func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntimeContext(ctx, opts)
	if err != nil {
		if strings.TrimSpace(opts.ReplayPath) != "" {
			return fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
		}
		return err
	}
	return plan.run(ctx, out)
}

func connectAudioOutputInferencer(i *sessionAudioOutputInferencer, ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		return nil, errors.New("audio output session inferencer is unavailable")
	}
	return i.inner.ConnectSession(context.WithoutCancel(ctx))
}

func openSessionLiveRecorder(opts SessionRunOptions, plan sessionRuntimePlan, destination string, maxDuration time.Duration) (runtimesession.LiveRecorder, error) {
	if strings.TrimSpace(destination) == "" {
		return nil, nil
	}
	service := sessionRecordingService(opts, plan)
	return service.OpenLiveEvidence(sessionLiveEvidenceOptions(opts, plan, destination, maxDuration))
}

func sessionRecordingService(opts SessionRunOptions, plan sessionRuntimePlan) runtimerecording.Service {
	if opts.recordingService != nil {
		return opts.recordingService
	}
	if plan.recordingService != nil {
		return plan.recordingService
	}
	return recordingwire.NewService(platformclock.Ensure(plan.clockSource))
}

func sessionLiveEvidenceOptions(opts SessionRunOptions, plan sessionRuntimePlan, destination string, maxDuration time.Duration) runtimerecording.LiveEvidenceOptions {
	model := strings.TrimSpace(plan.model)
	if model == "" {
		model = strings.TrimSpace(opts.Model)
	}
	options := runtimerecording.LiveEvidenceOptions{
		Destination:    destination,
		Provider:       plan.provider,
		Model:          model,
		ClockBase:      plan.clockSource.Now(),
		WallClockStart: time.Now().UTC(),
		Credentials:    sessionEvidenceCredentials(opts, plan.provider),
	}
	if strings.TrimSpace(opts.RecordPath) != "" {
		options.ProviderCapturePath = opts.RecordPath
		// Positive max-duration runs own the sibling JSONL artifact through
		// SessionDurationArtifactSet; the directory recorder must not create a
		// second exclusive sidecar at the same path.
		options.DisableProviderCaptureSidecar = maxDuration > 0
	}
	return options
}

func sessionRecordingCredentials(opts SessionRunOptions, plan sessionRuntimePlan) []string {
	return sessionEvidenceCredentials(opts, plan.provider)
}

// sessionInstructionsInferencer decorates caller-owned session seams without
// changing their provider construction. The provider-aware runtime factory
// handles the live provider path; injected sessions receive a generic session
// update after the provider announces that the session is open.
type sessionInstructionsInferencer struct {
	inner        messages.SessionInferencer
	instructions string
	tools        []messages.ToolDefinition
}

var _ messages.SessionInferencer = (*sessionInstructionsInferencer)(nil)

func newSessionInstructionsInferencer(inner messages.SessionInferencer, instructions string, toolDefinitions []messages.ToolDefinition) messages.SessionInferencer {
	return &sessionInstructionsInferencer{
		inner:        inner,
		instructions: instructions,
		tools:        cloneSessionToolDefinitions(toolDefinitions),
	}
}

func cloneSessionToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func (i *sessionInstructionsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newSessionInstructionsSession(inner, ctx, i.instructions, i.tools), nil
}

type sessionInstructionsSession struct {
	inner         messages.Session
	instructions  string
	tools         []messages.ToolDefinition
	receive       *messages.TypedBuffer[messages.StreamMessage]
	ctx           context.Context
	cancel        context.CancelFunc
	configureOnce sync.Once
	done          chan struct{}
	doneOnce      sync.Once
}

var _ messages.Session = (*sessionInstructionsSession)(nil)
var _ messages.SessionSendOutcomeSender = (*sessionInstructionsSession)(nil)

func newSessionInstructionsSession(inner messages.Session, parent context.Context, instructions string, toolDefinitions []messages.ToolDefinition) messages.Session {
	ctx, cancel := context.WithCancel(parent)
	session := &sessionInstructionsSession{
		inner:        inner,
		instructions: instructions,
		tools:        cloneSessionToolDefinitions(toolDefinitions),
		receive:      messages.NewTypedBuffer[messages.StreamMessage](inner.Receive().Cap()),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go session.relay()
	return session
}

func (s *sessionInstructionsSession) relay() {
	defer s.markDone()
	innerReceive := s.inner.Receive()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.inner.Done():
			s.drainAfterDone(innerReceive)
			return
		case msg := <-innerReceive.Chan():
			if !s.forward(msg) {
				return
			}
		}
	}
}

func (s *sessionInstructionsSession) drainAfterDone(innerReceive *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := innerReceive.Read()
		if !ok || !s.forward(msg) {
			return
		}
	}
}

func (s *sessionInstructionsSession) forward(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionOpen || msg.Type == messages.StreamTypeSessionCreated {
		var configureErr error
		s.configureOnce.Do(func() {
			outcome := messages.SendSessionWithOutcome(s.ctx, s.inner, messages.StreamMessage{
				Type: messages.StreamTypeSessionUpdate,
				Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
					Instructions: s.instructions,
					Tools:        s.tools,
				}),
			})
			if !outcome.OK() {
				configureErr = fmt.Errorf("send session instructions: %s", outcome.Status)
				if outcome.Err != nil {
					configureErr = fmt.Errorf("%w: %w", configureErr, outcome.Err)
				}
			}
		})
		if configureErr != nil {
			if closeErr := s.inner.Close(); closeErr != nil {
				configureErr = errors.Join(configureErr, fmt.Errorf("close session after instruction failure: %w", closeErr))
			}
			s.receive.Write(s.ctx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Value: messages.NewErrorValueWithError(configureErr),
			})
			return false
		}
	}
	return s.receive.Write(s.ctx, msg)
}

func (s *sessionInstructionsSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

// RequestResponse forwards the optional explicit response capability without
// changing the instruction-update lifecycle or replay behavior.
func (s *sessionInstructionsSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *sessionInstructionsSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

// SendMessage forwards the optional complete-message capability of the
// wrapped provider session. Instruction decoration must not hide the rich
// message path used to deliver a tool result on the next model turn.
func (s *sessionInstructionsSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards deferred complete messages for callers
// that batch more than one tool result before requesting the next response.
func (s *sessionInstructionsSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionInstructionsSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.inner)
	return complete
}

func (s *sessionInstructionsSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.inner)
	return withoutResponse
}

func (s *sessionInstructionsSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *sessionInstructionsSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionInstructionsSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionInstructionsSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}

func (s *sessionInstructionsSession) TerminalError() error {
	return terminalSessionError(s.inner)
}

func (s *sessionInstructionsSession) Close() error {
	s.cancel()
	err := s.inner.Close()
	s.markDone()
	return err
}

func (s *sessionInstructionsSession) markDone() {
	s.doneOnce.Do(func() { close(s.done) })
}
