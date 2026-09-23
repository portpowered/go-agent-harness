package rtc

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

type mediaError struct {
	direction devicegw.Direction
	err       error
}

func (e *mediaError) Error() string {
	if e.direction == "" {
		return "RTC session media unavailable: " + e.err.Error()
	}
	return "RTC " + string(e.direction) + " media unavailable: " + e.err.Error()
}

func (e *mediaError) Unwrap() error { return e.err }

type inferencer struct {
	binding *binding
	inner   messages.SessionInferencer
	errors  chan error
}

func (i *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.binding == nil {
		return nil, devices.ErrUnavailable
	}
	if i.inner == nil {
		return nil, errors.New("RTC provider inferencer is unavailable")
	}
	s, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	media, ok := rtcMedia(s)
	if !ok {
		return nil, errors.Join(&mediaError{err: ErrSessionMediaUnavailable}, s.Close())
	}
	if err := validateMedia(i.binding, media); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	pumpCtx, stopPumps := context.WithCancel(ctx)
	startMediaPumps(i, pumpCtx, media)
	return newBoundSession(s, i.binding, ctx, stopPumps), nil
}

func rtcMedia(session messages.Session) (audio.MediaEndpoints, bool) {
	owner, ok := session.(audio.MediaSession)
	if !ok {
		return audio.MediaEndpoints{}, false
	}
	return owner.RTCMedia(), true
}

func validateMedia(b *binding, media audio.MediaEndpoints) error {
	if b.source != nil && devicert.IsNilOutboundMedia(media.Outbound) {
		return &mediaError{direction: devicegw.DirectionInput, err: devicert.ErrNilRTCOutboundMedia}
	}
	if b.sink != nil && devicert.IsNilInboundMedia(media.Inbound) {
		return &mediaError{direction: devicegw.DirectionOutput, err: devicert.ErrNilRTCInboundMedia}
	}
	return nil
}

func startMediaPumps(i *inferencer, ctx context.Context, media audio.MediaEndpoints) {
	if i.binding.source != nil {
		go func() {
			i.report(devicert.PumpBufferedCaptureWithBuffer(ctx, i.binding.source, media.Outbound, i.binding.capture))
		}()
	}
	if i.binding.sink != nil {
		go func() {
			i.report(i.binding.sink.Pump(ctx, media.Inbound))
		}()
	}
}

func (i *inferencer) report(err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, contract.ErrClosed) || errors.Is(err, audio.ErrSessionMediaClosed) || errors.Is(err, devicert.ErrRTCDeviceSourceClosed) || errors.Is(err, devicert.ErrRTCDeviceSinkClosed) {
		return
	}
	select {
	case i.errors <- err:
	default:
	}
}
