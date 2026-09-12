package agentruntime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

var (
	// ErrRTCSessionMediaUnavailable identifies a missing provider media boundary.
	ErrRTCSessionMediaUnavailable = errors.New("RTC session media endpoints are unavailable")
)

const rtcDevicePlaybackDrainTimeout = 5 * time.Second

// RTCMediaEndpoints aliases provider-owned RTC media endpoints.
type RTCMediaEndpoints = audio.MediaEndpoints

// RTCMediaSession aliases the optional RTC session capability.
type RTCMediaSession = audio.MediaSession

type rtcMediaSessionForwarder interface {
	rtcMedia() (RTCMediaEndpoints, bool)
}

// RTCDeviceMediaError identifies missing session media capability.
type RTCDeviceMediaError struct {
	Direction devicegw.Direction
	Err       error
}

func (e *RTCDeviceMediaError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Direction == "" {
		return fmt.Sprintf("RTC session media unavailable: %v", e.Err)
	}
	return fmt.Sprintf("RTC %s media unavailable: %v", e.Direction, e.Err)
}

func (e *RTCDeviceMediaError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type rtcDeviceBindingInferencer struct {
	inner   messages.SessionInferencer
	binding *RTCDeviceBinding
	errors  chan error
}

func newRTCDeviceBindingInferencer(inner messages.SessionInferencer, binding *RTCDeviceBinding) (*rtcDeviceBindingInferencer, <-chan error) {
	wrapped := &rtcDeviceBindingInferencer{
		inner:   inner,
		binding: binding,
		errors:  make(chan error, 2),
	}
	return wrapped, wrapped.errors
}

func (i *rtcDeviceBindingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}

	media, ok := rtcMediaFromSession(session)
	if !ok {
		_ = session.Close()
		return nil, &RTCDeviceMediaError{Err: ErrRTCSessionMediaUnavailable}
	}
	if err := validateRTCDeviceMedia(i.binding, media); err != nil {
		_ = session.Close()
		return nil, err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	i.startPumps(ctx, media)
	bound := &rtcDeviceBoundSession{Session: session, binding: i.binding, lifecycleCtx: ctx}
	bound.startReceiveForwarder()
	return bound, nil
}

func rtcMediaFromSession(session messages.Session) (RTCMediaEndpoints, bool) {
	if owner, ok := session.(RTCMediaSession); ok {
		return owner.RTCMedia(), true
	}
	if forwarder, ok := session.(rtcMediaSessionForwarder); ok {
		return forwarder.rtcMedia()
	}
	return RTCMediaEndpoints{}, false
}

func closeRTCDeviceBinding(binding *RTCDeviceBinding) error {
	if binding == nil {
		return nil
	}
	return binding.Close()
}

func (i *rtcDeviceBindingInferencer) startPumps(ctx context.Context, media RTCMediaEndpoints) {
	if i.binding.Source != nil {
		if i.binding.Capture == nil {
			if err := ensureRTCDeviceBindingBuffers(i.binding); err != nil {
				i.report(err)
				return
			}
		}
		go func() {
			i.report(devicert.PumpBufferedCaptureWithBuffer(ctx, i.binding.Source, media.Outbound, i.binding.Capture))
		}()
	}
	if i.binding.Sink != nil {
		go func() { i.report(i.binding.Sink.Pump(ctx, media.Inbound)) }()
	}
}

func (i *rtcDeviceBindingInferencer) report(err error) {
	if rtcDevicePumpStopped(err) {
		return
	}
	i.errors <- err
}

func rtcDevicePumpStopped(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, devicert.ErrRTCDeviceSourceClosed) || errors.Is(err, devicert.ErrRTCDeviceSinkClosed) || errors.Is(err, contract.ErrClosed) ||
		errors.Is(err, audio.ErrSessionMediaClosed)
}

// rtcDeviceBoundSession joins session and device lifecycles.
type rtcDeviceBoundSession struct {
	messages.Session
	binding                                  *RTCDeviceBinding
	lifecycleCtx                             context.Context
	receive                                  *messages.TypedBuffer[messages.StreamMessage]
	forwardStop, forwardDone                 chan struct{}
	forwardOnce                              sync.Once
	terminalObserved, gracefulCloseRequested atomic.Bool
}

type playbackDrainingSession interface {
	DrainPlayback(context.Context) error
}

func (s *rtcDeviceBoundSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *rtcDeviceBoundSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	if s == nil {
		return nil
	}
	if s.receive != nil {
		return s.receive
	}
	return s.Session.Receive()
}

func (s *rtcDeviceBoundSession) startReceiveForwarder() {
	source := s.Session.Receive()
	if source == nil {
		return
	}
	s.receive = messages.NewTypedBuffer[messages.StreamMessage](source.Cap())
	s.forwardStop = make(chan struct{})
	s.forwardDone = make(chan struct{})
	go func() {
		defer close(s.forwardDone)
		s.forwardReceive(source)
	}()
}

func (s *rtcDeviceBoundSession) forwardReceive(source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		select {
		case msg := <-source.Chan():
			if !s.forwardSessionMessage(msg) {
				return
			}
		case <-s.Session.Done():
			s.forwardRemaining(source)
			return
		case <-s.forwardStop:
			return
		}
	}
}

func (s *rtcDeviceBoundSession) forwardRemaining(source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := source.Read()
		if !ok || !s.forwardSessionMessage(msg) {
			return
		}
	}
}

func (s *rtcDeviceBoundSession) forwardSessionMessage(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionClose {
		s.terminalObserved.Store(true)
	}
	return s.receive != nil && s.receive.WriteWaitContextOrDone(context.Background(), s.forwardStop, msg).OK()
}

func (s *rtcDeviceBoundSession) stopReceiveForwarder() {
	if s == nil || s.forwardStop == nil {
		return
	}
	s.forwardOnce.Do(func() { close(s.forwardStop) })
	<-s.forwardDone
}

func (s *rtcDeviceBoundSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeResponseCreate}) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	outcome := messages.RequestSessionResponse(ctx, s.Session)
	if outcome.OK() && s.binding != nil && s.binding.Sink != nil {
		if err := s.binding.Sink.PlaybackCommand(ctx, audio.PlaybackResume); err != nil {
			return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
		}
	}
	return outcome
}

func (s *rtcDeviceBoundSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *rtcDeviceBoundSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllowsCompleteMessage(msg) {
		return false
	}
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *rtcDeviceBoundSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllowsCompleteMessage(msg) {
		return false
	}
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *rtcDeviceBoundSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *rtcDeviceBoundSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *rtcDeviceBoundSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s == nil || s.Session == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllows(msg) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	outcome := messages.SendSessionWithOutcome(ctx, s.Session, msg)
	s.noteGracefulClose(msg, outcome)
	if outcome.OK() && s.binding != nil && s.binding.Sink != nil {
		switch msg.Type {
		case messages.StreamTypeResponseCancel:
			// The provider-facing cancellation is the accepted local boundary.
			// The playback generation and device queue lock make a racing pump
			// frame either get discarded here or stale before local admission.
			if err := s.binding.Sink.PlaybackCommand(ctx, audio.PlaybackDiscard); err != nil {
				return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
			}
		case messages.StreamTypeResponseCreate:
			if err := s.binding.Sink.PlaybackCommand(ctx, audio.PlaybackResume); err != nil {
				return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
			}
		}
	}
	return outcome
}

func (s *rtcDeviceBoundSession) noteGracefulClose(msg messages.StreamMessage, outcome messages.SessionSendOutcome) {
	if outcome.OK() && msg.Type == messages.StreamTypeSessionClose {
		s.gracefulCloseRequested.Store(true)
	}
}

func (s *rtcDeviceBoundSession) SessionAdmissionClosed() bool {
	controller, ok := s.Session.(interface{ SessionAdmissionClosed() bool })
	return ok && controller.SessionAdmissionClosed()
}

func (s *rtcDeviceBoundSession) SessionAdmissionAllows(msg messages.StreamMessage) bool {
	controller, ok := s.Session.(interface {
		SessionAdmissionAllows(messages.StreamMessage) bool
	})
	if ok {
		return controller.SessionAdmissionAllows(msg)
	}
	return !s.SessionAdmissionClosed()
}

func (s *rtcDeviceBoundSession) SessionAdmissionAllowsCompleteMessage(msg messages.Message) bool {
	controller, ok := s.Session.(interface {
		SessionAdmissionAllowsCompleteMessage(messages.Message) bool
	})
	if ok {
		return controller.SessionAdmissionAllowsCompleteMessage(msg)
	}
	return !s.SessionAdmissionClosed()
}

func (s *rtcDeviceBoundSession) Close() error {
	if s == nil {
		return nil
	}
	var drainErr error
	if s.cleanTerminal() {
		// A clean terminal drains with a bounded context after owner cancellation.
		drainParent := s.lifecycleCtx
		if drainParent == nil {
			drainParent = context.Background()
		}
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(drainParent), rtcDevicePlaybackDrainTimeout)
		drainErr = s.DrainPlayback(drainCtx)
		cancel()
	}
	var sessionErr error
	if s.Session != nil {
		sessionErr = s.Session.Close()
	}
	s.stopReceiveForwarder()
	return errors.Join(drainErr, sessionErr, s.binding.Close())
}

func (s *rtcDeviceBoundSession) cleanTerminal() bool {
	return s.terminalObserved.Load() || s.gracefulCloseRequested.Load()
}

func (s *rtcDeviceBoundSession) DrainPlayback(ctx context.Context) error {
	if s == nil || s.binding == nil || s.binding.Sink == nil {
		return nil
	}
	media, ok := rtcMediaFromSession(s.Session)
	if ok && !devicert.IsNilInboundMedia(media.Inbound) {
		if err := media.Inbound.Close(); err != nil {
			return err
		}
	}
	return s.binding.Sink.WaitForPump(ctx)
}

func validateRTCDeviceMedia(binding *RTCDeviceBinding, media RTCMediaEndpoints) error {
	if binding == nil {
		return nil
	}
	if binding.Source != nil && devicert.IsNilOutboundMedia(media.Outbound) {
		return &RTCDeviceMediaError{Direction: devicegw.DirectionInput, Err: devicert.ErrNilRTCOutboundMedia}
	}
	if binding.Sink != nil && devicert.IsNilInboundMedia(media.Inbound) {
		return &RTCDeviceMediaError{Direction: devicegw.DirectionOutput, Err: devicert.ErrNilRTCInboundMedia}
	}
	return nil
}

func bindRTCDeviceSessionInferencer(inner messages.SessionInferencer, binding *RTCDeviceBinding) (messages.SessionInferencer, <-chan error) {
	if binding == nil {
		return inner, nil
	}
	return newRTCDeviceBindingInferencer(inner, binding)
}

func ensureRTCDeviceBindingBuffers(binding *RTCDeviceBinding) error {
	if binding == nil || binding.Source == nil || binding.Capture != nil {
		return nil
	}
	capture, err := devicert.NewBufferedCapture(binding.Source)
	if err != nil {
		return fmt.Errorf("initialize RTC capture buffer: %w", err)
	}
	binding.Capture = capture
	return nil
}
