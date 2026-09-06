package agentruntime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

var (
	// ErrRTCSessionMediaUnavailable identifies a session owner that cannot
	// provide the media endpoints required by a selected device binding.
	ErrRTCSessionMediaUnavailable = errors.New("RTC session media endpoints are unavailable")
)

// RTCMediaEndpoints aliases the shared transport capability for callers that
// already depend on the agent-cli service package. The device runtime never
// closes these endpoints; their session owner retains that responsibility.
type RTCMediaEndpoints = audio.MediaEndpoints

// RTCMediaSession aliases the shared optional RTC session capability.
// WebSocket-only sessions need not implement it, which preserves their
// existing behavior when no RTC device selector is present.
type RTCMediaSession = audio.MediaSession

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

// rtcDeviceBindingInferencer starts the device pumps after the underlying
// session has handed back its real RTC media endpoints. Its error channel is
// consumed by the session runtime so a media failure cannot disappear inside
// a background goroutine.
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
		errors.Is(err, devicert.ErrRTCDeviceSourceClosed) || errors.Is(err, devicert.ErrRTCDeviceSinkClosed) || errors.Is(err, audio.ErrClosed) ||
		errors.Is(err, audio.ErrSessionMediaClosed)
}

// rtcDeviceBoundSession keeps the session owner and local device binding in
// one lifecycle. Session.Close runs first so a provider-owned media read can
// stop, then binding.Close waits for both pumps before releasing devicegw.
type rtcDeviceBoundSession struct {
	messages.Session
	binding *RTCDeviceBinding
}

type playbackDrainingSession interface {
	DrainPlayback(context.Context) error
}

// Send keeps the legacy bool-only session path on the same cancellation
// boundary as SendWithOutcome. Without this explicit method, the promoted
// messages.Session.Send method would bypass the local playback flush.
func (s *rtcDeviceBoundSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
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

// DrainPlayback seals only the provider-owned inbound media, leaving the
// provider session lifecycle unchanged. The sink pump can then consume every
// frame already accepted before the clean response boundary and wait for the
// native callback queue without manufacturing a provider-close terminal.
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

// ensureRTCDeviceBindingBuffers creates all memory handoffs before loop
// construction and before either device worker starts. This also keeps
// programmatically assembled source-only bindings on the same production
// ownership path as bindings returned by PrepareRTCDeviceBindings.
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
