package services

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var (
	// ErrRTCSessionMediaUnavailable identifies a session owner that cannot
	// provide the media endpoints required by a selected device binding.
	ErrRTCSessionMediaUnavailable = errors.New("RTC session media endpoints are unavailable")
)

// RTCMediaEndpoints aliases the shared transport capability for callers that
// already depend on the agent-cli service package. The device runtime never
// closes these endpoints; their session owner retains that responsibility.
type RTCMediaEndpoints = rtc.MediaEndpoints

// RTCMediaSession aliases the shared optional RTC session capability.
// WebSocket-only sessions need not implement it, which preserves their
// existing behavior when no RTC device selector is present.
type RTCMediaSession = rtc.MediaSession

// rtcMediaSessionForwarder lets service-side session decorators preserve the
// optional provider capability without changing the public messages.Session
// contract. The public rtc.MediaSession assertion remains the provider-owned
// boundary; this private seam only walks through local wrappers.
type rtcMediaSessionForwarder interface {
	rtcMedia() (RTCMediaEndpoints, bool)
}

// RTCDeviceMediaError identifies a missing session media capability or one
// missing directional endpoint while preserving the typed cause.
type RTCDeviceMediaError struct {
	Direction audio.Direction
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

// rtcDeviceBindingInferencer starts the device pumps after the underlying
// session has handed back its real RTC media endpoints. Its error channel is
// consumed by the session runtime so a media failure cannot disappear inside
// a background goroutine.
type rtcDeviceBindingInferencer struct {
	inner   messages.SessionInferencer
	binding *RTCDeviceBinding
	errors  chan error

	pumpInitDone chan struct{}
	pumpInitOnce sync.Once
	pumpMu       sync.Mutex
	pumpsStarted bool
	pumpsDone    chan struct{}
}

func newRTCDeviceBindingInferencer(inner messages.SessionInferencer, binding *RTCDeviceBinding) (*rtcDeviceBindingInferencer, <-chan error, func() error) {
	wrapped := &rtcDeviceBindingInferencer{
		inner:        inner,
		binding:      binding,
		errors:       make(chan error, 2),
		pumpInitDone: make(chan struct{}),
		pumpsDone:    make(chan struct{}),
	}
	return wrapped, wrapped.errors, wrapped.drainPumpErrors
}

func (i *rtcDeviceBindingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	defer i.pumpInitOnce.Do(func() { close(i.pumpInitDone) })
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

	i.startPumps(ctx, media)
	return &rtcDeviceBoundSession{Session: session, binding: i.binding}, nil
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
	i.pumpMu.Lock()
	i.pumpsStarted = true
	i.pumpMu.Unlock()
	var pumps sync.WaitGroup
	if i.binding.Source != nil {
		pumps.Add(1)
		go func() {
			defer pumps.Done()
			i.report(i.binding.Source.Pump(ctx, media.Outbound))
		}()
	}
	if i.binding.Sink != nil {
		pumps.Add(1)
		go func() {
			defer pumps.Done()
			i.report(i.binding.Sink.Pump(ctx, media.Inbound))
		}()
	}
	go func() {
		pumps.Wait()
		close(i.pumpsDone)
	}()
}

func (i *rtcDeviceBindingInferencer) report(err error) {
	if rtcDevicePumpStopped(err) {
		return
	}
	i.errors <- err
}

// drainPumpErrors waits until every started pump has reported before joining
// errors left in the notification channel. The session loop may consume the
// first pump failure while another output path is still unwinding; draining at
// the shared cleanup boundary preserves that sibling failure instead of letting
// it disappear behind the initiating error.
func (i *rtcDeviceBindingInferencer) drainPumpErrors() error {
	if i == nil {
		return nil
	}
	<-i.pumpInitDone
	i.pumpMu.Lock()
	started := i.pumpsStarted
	i.pumpMu.Unlock()
	if !started {
		return nil
	}
	<-i.pumpsDone
	var errs []error
	for {
		select {
		case err := <-i.errors:
			if err != nil {
				errs = append(errs, err)
			}
		default:
			return errors.Join(errs...)
		}
	}
}

func rtcDevicePumpStopped(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrRTCDeviceSourceClosed) || errors.Is(err, ErrRTCDeviceSinkClosed) || errors.Is(err, audio.ErrClosed) ||
		errors.Is(err, rtc.ErrSessionMediaClosed)
}

// rtcDeviceBoundSession keeps the session owner and local device binding in
// one lifecycle. Session.Close runs first so a provider-owned media read can
// stop, then binding.Close waits for both pumps before releasing devices.
type rtcDeviceBoundSession struct {
	messages.Session
	binding *RTCDeviceBinding
}

func (s *rtcDeviceBoundSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeResponseCreate}) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	return messages.RequestSessionResponse(ctx, s.Session)
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
	return messages.SendSessionWithOutcome(ctx, s.Session, msg)
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
	return errors.Join(s.Session.Close(), s.binding.Close())
}

func validateRTCDeviceMedia(binding *RTCDeviceBinding, media RTCMediaEndpoints) error {
	if binding == nil {
		return nil
	}
	if binding.Source != nil && nilRTCOutboundMedia(media.Outbound) {
		return &RTCDeviceMediaError{Direction: audio.DirectionInput, Err: ErrNilRTCOutboundMedia}
	}
	if binding.Sink != nil && nilRTCInboundMedia(media.Inbound) {
		return &RTCDeviceMediaError{Direction: audio.DirectionOutput, Err: ErrNilRTCInboundMedia}
	}
	return nil
}

func bindRTCDeviceSessionInferencer(inner messages.SessionInferencer, binding *RTCDeviceBinding) (messages.SessionInferencer, <-chan error, func() error) {
	if binding == nil {
		return inner, nil, nil
	}
	return newRTCDeviceBindingInferencer(inner, binding)
}
