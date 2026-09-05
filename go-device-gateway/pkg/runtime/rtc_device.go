package runtime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
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
	DeviceID  devicegw.DeviceID
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
	DeviceID     devicegw.DeviceID
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
	source                  *devicegw.DeviceSource
	id                      devicegw.DeviceID
	filter                  CaptureFilter
	sourceRate              int
	providerRate            int
	captureObserver         RTCDeviceCaptureObserver
	preGateSamplesObserver  RTCDeviceCaptureSamplesObserver
	uploadedSamplesObserver RTCDeviceCaptureSamplesObserver

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
func NewRTCDeviceSource(registry devicegw.DeviceRegistry, id devicegw.DeviceID) (*RTCDeviceSource, error) {
	return NewRTCDeviceSourceAtRate(registry, id, audio.SampleRate)
}

// NewRTCDeviceSourceAtRate opens an input device for mono PCM16 at rate. A
// zero rate retains the compatibility default used by NewRTCDeviceSource.
func NewRTCDeviceSourceAtRate(registry devicegw.DeviceRegistry, id devicegw.DeviceID, rate int) (*RTCDeviceSource, error) {
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

func newRTCDeviceSourceFromOpened(source *devicegw.DeviceSource, sourceRate, providerRate int) *RTCDeviceSource {
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
func openRTCDeviceSourceAtRate(registry devicegw.DeviceRegistry, id devicegw.DeviceID, providerRate int) (*devicegw.DeviceSource, int, error) {
	source, err := devicegw.NewDeviceSourceAtRate(registry, id, providerRate)
	if err == nil {
		return source, source.SampleRate(), nil
	}

	var formatErr *devicegw.DeviceFormatError
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

		fallback, fallbackErr := devicegw.NewDeviceSourceWithFormat(registry, id, available)
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
func NewDefaultRTCDeviceSource(registry devicegw.DeviceRegistry) (*RTCDeviceSource, error) {
	return NewRTCDeviceSource(registry, "")
}

// NewRTCDeviceSourceFromOpened adopts an already-opened input endpoint.
// Ownership transfers to the returned worker, which releases it on Close.
func NewRTCDeviceSourceFromOpened(source *devicegw.DeviceSource, sourceRate, providerRate int) *RTCDeviceSource {
	return newRTCDeviceSourceFromOpened(source, sourceRate, providerRate)
}

func (s *RTCDeviceSource) SetCaptureFilter(filter CaptureFilter) {
	if s != nil {
		s.filter = filter
	}
}

func (s *RTCDeviceSource) SetCaptureObserver(observer func(devicegw.DeviceID, audio.CaptureQueueStats)) {
	if s != nil {
		s.captureObserver = observer
	}
}

func (s *RTCDeviceSource) SetPreGateSamplesObserver(observer func(int, []int16)) {
	if s != nil {
		s.preGateSamplesObserver = observer
	}
}

func (s *RTCDeviceSource) SetUploadedSamplesObserver(observer func(int, []int16)) {
	if s != nil {
		s.uploadedSamplesObserver = observer
	}
}

// DeviceID returns the stable ID acquired by this source.
func (s *RTCDeviceSource) DeviceID() devicegw.DeviceID {
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
func (s *RTCDeviceSource) Pump(ctx context.Context, outbound audio.OutboundMedia) error {
	return s.pumpWithUploadedObserver(ctx, outbound, s.uploadedSamplesObserver)
}

// pumpWithUploadedObserver is the runtime-internal form used when the
// provider handoff itself owns the post-admission upload observation. Keeping
// the callback as an argument avoids racing a live worker by mutating the
// source configuration after Pump has started.
func (s *RTCDeviceSource) pumpWithUploadedObserver(ctx context.Context, outbound audio.OutboundMedia, uploadedObserver RTCDeviceCaptureSamplesObserver) error {
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

	processor, err := audio.NewProcessor(audio.PCM16DeviceFormat(s.sourceRate), audio.PCM16DeviceFormat(s.providerRate), max(1, audio.FrameSize*s.providerRate/s.sourceRate))
	if err != nil {
		return &RTCDeviceSourceError{DeviceID: s.id, Operation: "configure", Err: err}
	}
	writeProcessed := func(samples []int16, final bool) error {
		frames, err := processor.ProcessAvailable(audio.PCMFrame{Samples: samples, EndOfResponse: final})
		if err != nil {
			return &RTCDeviceSourceError{DeviceID: s.id, Operation: "resample", Err: err}
		}
		for _, converted := range frames {
			if len(converted.Samples) == 0 {
				continue
			}
			if err := outbound.WriteFrame(operationCtx, converted); err != nil {
				return &RTCDeviceSourceError{DeviceID: s.id, Operation: "write", Err: err}
			}
			if uploadedObserver != nil {
				uploadedObserver(s.providerRate, converted.Samples)
			}
		}
		return nil
	}
	frame := make([]int16, audio.FrameSize)
	for {
		clear(frame)
		if err := s.source.ReadFrame(operationCtx, frame); err != nil {
			if errors.Is(err, io.EOF) {
				return writeProcessed(nil, true)
			}
			return &RTCDeviceSourceError{DeviceID: s.id, Operation: "read", Err: err}
		}
		if s.preGateSamplesObserver != nil {
			s.preGateSamplesObserver(s.sourceRate, frame)
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
			if err := writeProcessed(samples, false); err != nil {
				return err
			}
		}
	}
}

// Run is an alias for Pump for callers that model the binding as a lifecycle
// worker.
func (s *RTCDeviceSource) Run(ctx context.Context, outbound audio.OutboundMedia) error {
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
		if s.captureObserver != nil {
			s.captureObserver(s.id, s.source.CaptureStats())
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

func nilRTCOutboundMedia(media audio.OutboundMedia) bool {
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

// IsNilOutboundMedia reports whether an outbound endpoint is nil, including a
// typed nil hidden behind the media interface.
func IsNilOutboundMedia(media audio.OutboundMedia) bool { return nilRTCOutboundMedia(media) }
