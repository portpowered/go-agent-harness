package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var (
	// ErrRTCDeviceSourceClosed identifies a pump that was stopped by closing
	// its device endpoint.
	ErrRTCDeviceSourceClosed = errors.New("RTC device source is closed")
	// ErrRTCDeviceSourceRunning prevents two pumps from reading one exclusive
	// device concurrently.
	ErrRTCDeviceSourceRunning = errors.New("RTC device source is already running")
	// ErrNilRTCOutboundMedia identifies a missing outgoing track seam.
	ErrNilRTCOutboundMedia = errors.New("RTC outbound media endpoint is nil")
)

// RTCDeviceSourceError identifies a runtime operation in the device-to-RTC
// boundary while preserving the underlying device, context, and media error.
type RTCDeviceSourceError struct {
	DeviceID  audio.DeviceID
	Operation string
	Err       error
}

func (e *RTCDeviceSourceError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("RTC device source %q %s failed: %v", e.DeviceID, e.Operation, e.Err)
}

func (e *RTCDeviceSourceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// RTCDeviceSource owns a registry-backed input device and pumps its fixed-size
// PCM frames into an outgoing RTC media endpoint. The RTC endpoint remains
// caller-owned; Close only stops this source and releases its device.
type RTCDeviceSource struct {
	source *audio.DeviceSource
	id     audio.DeviceID

	lifeCtx    context.Context
	lifeCancel context.CancelCauseFunc

	mu        sync.Mutex
	closed    bool
	running   bool
	runDone   chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// NewRTCDeviceSource opens an input device through the shared audio registry.
// An empty id selects the registry's input default; a non-empty id is passed
// through as an exact stable device ID.
func NewRTCDeviceSource(registry audio.DeviceRegistry, id audio.DeviceID) (*RTCDeviceSource, error) {
	return NewRTCDeviceSourceAtRate(registry, id, audio.SampleRate)
}

// NewRTCDeviceSourceAtRate opens an input device for mono PCM16 at rate. A
// zero rate retains the compatibility default used by NewRTCDeviceSource.
func NewRTCDeviceSourceAtRate(registry audio.DeviceRegistry, id audio.DeviceID, rate int) (*RTCDeviceSource, error) {
	if rate == 0 {
		rate = audio.SampleRate
	}
	source, err := audio.NewDeviceSourceAtRate(registry, id, rate)
	if err != nil {
		return nil, err
	}
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	return &RTCDeviceSource{
		source:     source,
		id:         source.DeviceID(),
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
	}, nil
}

// NewDefaultRTCDeviceSource opens the directional input default from registry.
func NewDefaultRTCDeviceSource(registry audio.DeviceRegistry) (*RTCDeviceSource, error) {
	return NewRTCDeviceSource(registry, "")
}

// DeviceID returns the stable ID acquired by this source.
func (s *RTCDeviceSource) DeviceID() audio.DeviceID {
	if s == nil {
		return ""
	}
	return s.id
}

// Pump reads audio.FrameSize samples at audio.SampleRate from the device and
// synchronously hands an owned copy of each frame to outbound. A finite source
// ending with io.EOF is a clean pump completion. The method does not close the
// RTC endpoint because that endpoint belongs to its caller.
func (s *RTCDeviceSource) Pump(ctx context.Context, outbound rtc.OutboundMedia) error {
	if s == nil {
		return ErrRTCDeviceSourceClosed
	}
	if nilRTCOutboundMedia(outbound) {
		return ErrNilRTCOutboundMedia
	}
	if ctx == nil {
		ctx = context.Background()
	}

	lifeCtx, finish, err := s.beginPump()
	if err != nil {
		return err
	}
	defer finish()

	operationCtx, cancel := context.WithCancelCause(ctx)
	stopLifeHook := context.AfterFunc(lifeCtx, func() {
		cancel(ErrRTCDeviceSourceClosed)
	})
	defer func() {
		stopLifeHook()
		cancel(nil)
	}()

	frame := make([]int16, audio.FrameSize)
	for {
		clear(frame)
		if err := s.source.ReadFrame(operationCtx, frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return &RTCDeviceSourceError{DeviceID: s.id, Operation: "read", Err: err}
		}

		// OutboundMedia's contract makes the caller responsible for the frame
		// storage only until WriteFrame returns. Keep the source buffer private
		// so a future media implementation cannot retain or mutate it.
		samples := append([]int16(nil), frame...)
		if err := outbound.WriteFrame(operationCtx, rtc.PCMFrame{Samples: samples}); err != nil {
			return &RTCDeviceSourceError{DeviceID: s.id, Operation: "write", Err: err}
		}
	}
}

// Run is an alias for Pump for callers that model the binding as a lifecycle
// worker.
func (s *RTCDeviceSource) Run(ctx context.Context, outbound rtc.OutboundMedia) error {
	return s.Pump(ctx, outbound)
}

// Close stops an active pump and releases the acquired input handle exactly
// once. The call waits for an in-flight ReadFrame or WriteFrame to observe the
// cancellation before returning.
func (s *RTCDeviceSource) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		done := s.runDone
		s.mu.Unlock()

		s.lifeCancel(ErrRTCDeviceSourceClosed)
		s.closeErr = s.source.Close()
		if done != nil {
			<-done
		}
	})
	return s.closeErr
}

func (s *RTCDeviceSource) beginPump() (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrRTCDeviceSourceClosed
	}
	if s.running {
		s.mu.Unlock()
		return nil, nil, ErrRTCDeviceSourceRunning
	}
	s.running = true
	s.runDone = make(chan struct{})
	lifeCtx := s.lifeCtx
	s.mu.Unlock()

	return lifeCtx, func() {
		s.mu.Lock()
		if s.running {
			s.running = false
			close(s.runDone)
			s.runDone = nil
		}
		s.mu.Unlock()
	}, nil
}

func nilRTCOutboundMedia(media rtc.OutboundMedia) bool {
	if media == nil {
		return true
	}
	value := reflect.ValueOf(media)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
