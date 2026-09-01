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
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
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

// RTCDeviceSourceRateError describes a capture-rate conversion that cannot
// be satisfied at the session media boundary. SourceRate is the rate the
// local device actually supplies; ProviderRate is the rate declared by the
// provider session.
type RTCDeviceSourceRateError struct {
	DeviceID     audio.DeviceID
	SourceRate   int
	ProviderRate int
	Err          error
}

func (e *RTCDeviceSourceRateError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.SourceRate > 0 {
		return fmt.Sprintf("RTC device source %q cannot convert captured audio from %d Hz to provider input rate %d Hz: %v", e.DeviceID, e.SourceRate, e.ProviderRate, e.Err)
	}
	return fmt.Sprintf("RTC device source %q cannot provide provider input rate %d Hz: %v", e.DeviceID, e.ProviderRate, e.Err)
}

func (e *RTCDeviceSourceRateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// RTCDeviceSource owns a registry-backed input device and pumps its fixed-size
// PCM frames into an outgoing RTC media endpoint. The RTC endpoint remains
// caller-owned; Close only stops this source and releases its device.
type RTCDeviceSource struct {
	source       *audio.DeviceSource
	id           audio.DeviceID
	filter       rtcDeviceCaptureFilter
	sourceRate   int
	providerRate int

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
	if _, err := wavio.Resample(nil, rate, rate); err != nil {
		return nil, &RTCDeviceSourceRateError{DeviceID: id, ProviderRate: rate, Err: err}
	}
	source, sourceRate, err := openRTCDeviceSourceAtRate(registry, id, rate)
	if err != nil {
		return nil, err
	}
	return newRTCDeviceSourceFromOpened(source, sourceRate, rate), nil
}

func newRTCDeviceSourceFromOpened(source *audio.DeviceSource, sourceRate, providerRate int) *RTCDeviceSource {
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	return &RTCDeviceSource{
		source:       source,
		id:           source.DeviceID(),
		sourceRate:   sourceRate,
		providerRate: providerRate,
		lifeCtx:      lifeCtx,
		lifeCancel:   lifeCancel,
	}
}

// openRTCDeviceSourceAtRate first requests the provider rate natively. If a
// device reports a different supported PCM16 rate, it opens that rate and
// leaves one conversion for RTCDeviceSource.Pump. This keeps device setup
// honest while allowing a 16 kHz-only microphone to feed a 24 kHz provider.
func openRTCDeviceSourceAtRate(registry audio.DeviceRegistry, id audio.DeviceID, providerRate int) (*audio.DeviceSource, int, error) {
	source, err := audio.NewDeviceSourceAtRate(registry, id, providerRate)
	if err == nil {
		return source, source.SampleRate(), nil
	}

	var formatErr *audio.DeviceFormatError
	if !errors.As(err, &formatErr) {
		return nil, 0, err
	}

	var fallbackErrs []error
	observedRate := 0
	for _, available := range formatErr.Available {
		if observedRate == 0 && available.SampleRate > 0 {
			observedRate = available.SampleRate
		}
		if available.SampleRate == providerRate {
			continue
		}
		if formatErr := available.Validate(); formatErr != nil {
			fallbackErrs = append(fallbackErrs, formatErr)
			continue
		}
		if _, resampleErr := wavio.Resample(nil, available.SampleRate, providerRate); resampleErr != nil {
			fallbackErrs = append(fallbackErrs, &RTCDeviceSourceRateError{
				DeviceID:     id,
				SourceRate:   available.SampleRate,
				ProviderRate: providerRate,
				Err:          resampleErr,
			})
			continue
		}

		fallback, fallbackErr := audio.NewDeviceSourceWithFormat(registry, id, available)
		if fallbackErr == nil {
			return fallback, fallback.SampleRate(), nil
		}
		fallbackErrs = append(fallbackErrs, fallbackErr)
	}
	if len(fallbackErrs) == 0 {
		return nil, 0, err
	}

	return nil, 0, &RTCDeviceSourceRateError{
		DeviceID:     id,
		SourceRate:   observedRate,
		ProviderRate: providerRate,
		Err:          errors.Join(err, errors.Join(fallbackErrs...)),
	}
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

// SourceSampleRate reports the rate supplied by the opened capture device.
func (s *RTCDeviceSource) SourceSampleRate() int {
	if s == nil {
		return 0
	}
	return s.sourceRate
}

// ProviderSampleRate reports the rate used for frames handed to the provider
// media endpoint.
func (s *RTCDeviceSource) ProviderSampleRate() int {
	if s == nil {
		return 0
	}
	return s.providerRate
}

// Pump reads audio.FrameSize samples at the device's selected rate and
// synchronously hands provider-rate samples to outbound. When the device and
// provider rates differ, each frame is converted once while retaining its
// wall-clock duration. A finite source ending with io.EOF is a clean pump
// completion. The method does not close the RTC endpoint because that endpoint
// belongs to its caller.
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
	if s.filter != nil {
		defer s.filter.DiscardHeld()
	}

	frame := make([]int16, audio.FrameSize)
	for {
		clear(frame)
		if err := s.source.ReadFrame(operationCtx, frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return &RTCDeviceSourceError{DeviceID: s.id, Operation: "read", Err: err}
		}

		samplesToSend := [][]int16{append([]int16(nil), frame...)}
		if s.filter != nil {
			var filterErr error
			samplesToSend, filterErr = s.filter.FilterCapture(operationCtx, frame)
			if filterErr != nil {
				return &RTCDeviceSourceError{DeviceID: s.id, Operation: "filter", Err: filterErr}
			}
		}
		for _, samples := range samplesToSend {
			if len(samples) == 0 {
				continue
			}
			providerSamples, resampleErr := s.providerFrame(samples)
			if resampleErr != nil {
				return &RTCDeviceSourceError{DeviceID: s.id, Operation: "resample", Err: resampleErr}
			}
			// OutboundMedia's contract makes the caller responsible for the frame
			// storage only until WriteFrame returns. The filter returns owned
			// storage, and the unfiltered path copied the source buffer above.
			if err := outbound.WriteFrame(operationCtx, rtc.PCMFrame{Samples: providerSamples}); err != nil {
				return &RTCDeviceSourceError{DeviceID: s.id, Operation: "write", Err: err}
			}
		}
	}
}

func (s *RTCDeviceSource) providerFrame(frame []int16) ([]int16, error) {
	if s.sourceRate <= 0 || s.providerRate <= 0 {
		return nil, &RTCDeviceSourceRateError{
			DeviceID:     s.id,
			SourceRate:   s.sourceRate,
			ProviderRate: s.providerRate,
			Err:          errors.New("capture and provider rates must be positive"),
		}
	}
	if s.sourceRate == s.providerRate {
		return append([]int16(nil), frame...), nil
	}
	converted, err := wavio.Resample(frame, s.sourceRate, s.providerRate)
	if err != nil {
		return nil, &RTCDeviceSourceRateError{
			DeviceID:     s.id,
			SourceRate:   s.sourceRate,
			ProviderRate: s.providerRate,
			Err:          err,
		}
	}
	return converted, nil
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
