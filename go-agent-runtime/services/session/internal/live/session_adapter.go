package live

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// capturingInferencer exposes provider media to the handle only after the
// agent loop has established its session. A factory can return any
// messages.Session implementation; media is an optional capability.
type capturingInferencer struct {
	inner             messages.SessionInferencer
	media             *mediagate.Gate
	continuous        bool
	onDispatch        func(messages.StreamMessage)
	onToolResult      func(string, string, bool)
	onContinuation    func()
	onOpeningAdmitted func()
	onProviderDone    func(error)
	onMediaAttached   func(bool)
	captureMu         sync.Mutex
	captureFlush      func() error
}

func (i *capturingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.media.Fail(err)
		return nil, err
	}
	if flusher, ok := i.inner.(interface{ FlushCapture() error }); ok {
		i.captureMu.Lock()
		i.captureFlush = flusher.FlushCapture
		i.captureMu.Unlock()
	}
	mediaAttached := false
	if providerMedia, ok := s.(sharedaudio.MediaSession); ok {
		var endpoints sharedaudio.MediaEndpoints
		if i.continuous {
			if configurable, ok := s.(sharedaudio.ConfigurableMediaSession); ok {
				endpoints = configurable.RTCMediaWithOptions(sharedaudio.MediaSessionOptions{InboundContinuous: true})
			} else {
				endpoints = providerMedia.RTCMedia()
			}
		} else {
			endpoints = providerMedia.RTCMedia()
		}
		mediaAttached = endpoints.Inbound != nil
		i.media.Attach(ctx, endpoints)
	}
	if i.onMediaAttached != nil {
		i.onMediaAttached(mediaAttached)
	}
	if !mediaAttached {
		i.media.Fail(mediagate.ErrMediaUnavailable)
	}
	// AgentLoop owns the participant goroutine, while a persistent provider
	// owns the transport lifetime. When the latter closes, notify the live
	// owner only after the model runner has had a chance to publish its
	// synthesized SessionClose boundary. The provider Done contract is the
	// cleanup join point, so keep watching it even when the runner context is
	// canceled; otherwise a replay mismatch can be lost to teardown's
	// context.Canceled.
	if done := s.Done(); done != nil && i.onProviderDone != nil {
		go func() {
			<-done
			var terminalErr error
			// The loop intentionally treats Session.Done as a lifecycle
			// boundary and may return nil after draining its receive buffer.
			// Provider implementations expose the actionable transport or
			// replay mismatch through this optional method; preserve it before
			// teardown turns the runner context into context.Canceled.
			if provider, ok := s.(interface{ TerminalError() error }); ok {
				terminalErr = provider.TerminalError()
			}
			i.onProviderDone(terminalErr)
		}()
	}
	return &orderedSession{
		inner:             s,
		media:             i.media,
		onDispatch:        i.onDispatch,
		onToolResult:      i.onToolResult,
		onContinuation:    i.onContinuation,
		onOpeningAdmitted: i.onOpeningAdmitted,
	}, nil
}

// FlushCapture forwards the optional provider capture finalization seam. The
// handle calls it after the loop has joined its session, so recording owners
// can finalize their evidence only after the provider artifact is durable.
func (i *capturingInferencer) FlushCapture() error {
	if i == nil {
		return nil
	}
	i.captureMu.Lock()
	flush := i.captureFlush
	i.captureMu.Unlock()
	if flush == nil {
		return nil
	}
	return flush()
}

// orderedSession serializes every provider ingress operation with the public
// media bridge. Explicit controls register an admission barrier before they
// enter AgentLoop. Automatic provider sends may pass a control that has not
// reached the runner yet, which avoids a same-runner wait cycle; once the
// control is dispatched, later media and provider traffic wait for its wire
// acknowledgement.
type orderedSession struct {
	inner             messages.Session
	media             *mediagate.Gate
	onDispatch        func(messages.StreamMessage)
	onToolResult      func(string, string, bool)
	onContinuation    func()
	onOpeningAdmitted func()
}

func (s *orderedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

// InitialSessionConfigSent preserves the provider-owned startup configuration
// marker across the runtime's ordering/media wrapper. The model runner uses
// this optional capability to avoid echoing the initial session.update when a
// native provider already sent it during ConnectSession.
func (s *orderedSession) InitialSessionConfigSent() bool {
	if s == nil || s.inner == nil {
		return false
	}
	marker, ok := s.inner.(interface{ InitialSessionConfigSent() bool })
	return ok && marker.InitialSessionConfigSent()
}

func (s *orderedSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s == nil || s.inner == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	if ctx == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: errors.New("session send context is required")}
	}
	if s.media == nil {
		return s.sendAutomatic(ctx, msg)
	}
	ackID := msg.ActorProvidedID
	if present, canceled := s.media.ControlState(ackID); present {
		return s.sendMarkedControl(ctx, msg, ackID, canceled)
	}
	if mediagate.IsControlID(ackID) {
		// Teardown may remove a pending marker before the agent-loop runner
		// observes its event. Never reinterpret that private control as an
		// ordinary provider message or leak it downstream.
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	return s.sendAutomatic(ctx, msg)
}

func (s *orderedSession) sendMarkedControl(ctx context.Context, msg messages.StreamMessage, ackID string, canceled bool) messages.SessionSendOutcome {
	if canceled {
		s.media.CancelAck(ackID)
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	release, err := s.media.BeginControl(ctx, ackID)
	if err != nil {
		s.media.CancelAck(ackID)
		return sessionSendOutcomeForContext(err)
	}
	if _, canceled := s.media.ControlState(ackID); canceled {
		release()
		s.media.Acknowledge(ackID, false)
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	// The marker is an admission detail owned by this adapter. Do not expose
	// it to an external provider implementation.
	msg.ActorProvidedID = ""
	outcome := s.sendInner(ctx, msg)
	release()
	s.media.Acknowledge(ackID, outcome.OK())
	return outcome
}

func (s *orderedSession) sendAutomatic(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return s.runAdmission(ctx, func() messages.SessionSendOutcome {
		return s.sendInner(ctx, msg)
	})
}

func sessionSendOutcomeForContext(err error) messages.SessionSendOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func (s *orderedSession) runAdmission(ctx context.Context, operation func() messages.SessionSendOutcome) messages.SessionSendOutcome {
	if operation == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	if s == nil || s.media == nil {
		return operation()
	}
	release, err := s.media.BeginAutomatic(ctx)
	if err != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed, Err: err}
	}
	outcome := operation()
	release()
	return outcome
}

func (s *orderedSession) runAdmissionBool(ctx context.Context, operation func() bool) bool {
	if operation == nil {
		return false
	}
	if ctx == nil {
		return false
	}
	outcome := s.runAdmission(ctx, func() messages.SessionSendOutcome {
		if operation() {
			return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
		}
		if err := ctx.Err(); err != nil {
			return sessionSendOutcomeForContext(err)
		}
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	})
	return outcome.OK()
}

func (s *orderedSession) sendInner(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	var outcome messages.SessionSendOutcome
	if sender, ok := s.inner.(messages.SessionSendOutcomeSender); ok {
		outcome = sender.SendWithOutcome(ctx, msg)
	} else if s.inner.Send(ctx, msg) {
		outcome = messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	} else if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
		} else {
			outcome = messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
		}
	} else {
		outcome = messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	if outcome.OK() && s.onDispatch != nil {
		// The callback is owned by the session handle and only observes a copy
		// of the provider admission metadata. In particular, ActorProvidedID
		// has already been stripped for marked controls above.
		s.onDispatch(msg)
	}
	if outcome.OK() {
		s.observeAdmission(msg, false)
	}
	return outcome
}

func (s *orderedSession) observeAdmission(msg messages.StreamMessage, completeMessage bool) {
	if s == nil {
		return
	}
	if msg.Type == messages.StreamTypeToolCallEnd {
		if s.onToolResult == nil {
			return
		}
		value, ok := msg.Value.(*messages.ToolCallEndValue)
		if !ok || value == nil {
			return
		}
		callID := value.ToolCallID
		if callID == "" {
			callID = msg.ToolCallId
		}
		s.onToolResult(callID, value.Name, completeMessage)
		return
	}
	if msg.Type == messages.StreamTypeResponseCreate && s.onContinuation != nil {
		s.onContinuation()
	}
}

func (s *orderedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Receive()
}

func (s *orderedSession) Done() <-chan struct{} {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Done()
}

func (s *orderedSession) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

func (s *orderedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if s == nil || s.inner == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	requester, ok := s.inner.(messages.SessionResponseRequester)
	if !ok {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return s.runAdmission(ctx, func() messages.SessionSendOutcome {
		outcome := requester.RequestResponse(ctx)
		if outcome.OK() && s.onDispatch != nil {
			s.onDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		}
		if outcome.OK() {
			s.observeAdmission(messages.StreamMessage{Type: messages.StreamTypeResponseCreate}, false)
		}
		return outcome
	})
}

func (s *orderedSession) SupportsResponseRequests() bool {
	if s == nil || s.inner == nil {
		return false
	}
	capability, ok := s.inner.(messages.SessionResponseCapability)
	if ok {
		return capability.SupportsResponseRequests()
	}
	_, ok = s.inner.(messages.SessionResponseRequester)
	return ok
}

type completeMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

type completeMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

func (s *orderedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	sender, ok := s.inner.(completeMessageSender)
	return ok && s.runAdmissionBool(ctx, func() bool {
		accepted := sender.SendMessage(ctx, msg)
		if accepted && s.onOpeningAdmitted != nil {
			s.onOpeningAdmitted()
		}
		if accepted && s.onDispatch != nil {
			s.onDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		}
		if accepted {
			s.observeAdmission(messages.StreamMessage{
				Type:       messages.StreamTypeToolCallEnd,
				ToolCallId: msg.ToolCallID,
				Value:      messages.NewToolCallEndValue(msg.ToolCallID, msg.Name, ""),
			}, true)
		}
		return accepted
	})
}

func (s *orderedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	sender, ok := s.inner.(completeMessageWithoutResponseSender)
	return ok && s.runAdmissionBool(ctx, func() bool {
		accepted := sender.SendMessageWithoutResponse(ctx, msg)
		if accepted && s.onOpeningAdmitted != nil {
			s.onOpeningAdmitted()
		}
		if accepted {
			s.observeAdmission(messages.StreamMessage{
				Type:       messages.StreamTypeToolCallEnd,
				ToolCallId: msg.ToolCallID,
				Value:      messages.NewToolCallEndValue(msg.ToolCallID, msg.Name, ""),
			}, false)
		}
		return accepted
	})
}

func (s *orderedSession) SupportsCompleteMessages() bool {
	if s == nil || s.inner == nil {
		return false
	}
	capability, ok := s.inner.(interface{ SupportsCompleteMessages() bool })
	return ok && capability.SupportsCompleteMessages()
}

func (s *orderedSession) SupportsCompleteMessagesWithoutResponse() bool {
	if s == nil || s.inner == nil {
		return false
	}
	capability, ok := s.inner.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	return ok && capability.SupportsCompleteMessagesWithoutResponse()
}
