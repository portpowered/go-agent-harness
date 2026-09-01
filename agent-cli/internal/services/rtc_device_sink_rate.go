package services

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// RTCDeviceSinkRateError describes a provider-to-playback conversion that
// cannot be satisfied at the local device boundary.
type RTCDeviceSinkRateError struct {
	DeviceID     audio.DeviceID
	ProviderRate int
	DeviceRate   int
	Err          error
}

func (e *RTCDeviceSinkRateError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.DeviceRate > 0 {
		return fmt.Sprintf("RTC device sink %q cannot convert provider output from %d Hz to device rate %d Hz: %v", e.DeviceID, e.ProviderRate, e.DeviceRate, e.Err)
	}
	return fmt.Sprintf("RTC device sink %q cannot accept provider output rate %d Hz: %v", e.DeviceID, e.ProviderRate, e.Err)
}

func (e *RTCDeviceSinkRateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// openRTCDeviceSinkAtRate first requests provider-native playback. If the
// selected device reports another supported PCM16 rate, the sink opens that
// exact format and leaves one conversion for RTCDeviceSink.Pump.
func openRTCDeviceSinkAtRate(registry audio.DeviceRegistry, id audio.DeviceID, providerRate int) (*audio.DeviceSink, int, error) {
	if _, err := wavio.Resample(nil, providerRate, providerRate); err != nil {
		return nil, 0, &RTCDeviceSinkRateError{DeviceID: id, ProviderRate: providerRate, Err: err}
	}
	sink, err := audio.NewDeviceSinkAtRate(registry, id, providerRate)
	if err == nil {
		return sink, sink.SampleRate(), nil
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
		if validateErr := available.Validate(); validateErr != nil {
			fallbackErrs = append(fallbackErrs, validateErr)
			continue
		}
		if _, resampleErr := wavio.Resample(nil, providerRate, available.SampleRate); resampleErr != nil {
			fallbackErrs = append(fallbackErrs, &RTCDeviceSinkRateError{
				DeviceID:     id,
				ProviderRate: providerRate,
				DeviceRate:   available.SampleRate,
				Err:          resampleErr,
			})
			continue
		}

		fallback, fallbackErr := audio.NewDeviceSinkWithFormat(registry, id, available)
		if fallbackErr == nil {
			return fallback, fallback.SampleRate(), nil
		}
		fallbackErrs = append(fallbackErrs, fallbackErr)
	}
	if len(fallbackErrs) == 0 {
		return nil, 0, err
	}

	return nil, 0, &RTCDeviceSinkRateError{
		DeviceID:     id,
		ProviderRate: providerRate,
		DeviceRate:   observedRate,
		Err:          errors.Join(err, errors.Join(fallbackErrs...)),
	}
}

func (s *RTCDeviceSink) deviceFrame(samples []int16) ([]int16, error) {
	if s.deviceRate <= 0 || s.providerRate <= 0 {
		return nil, &RTCDeviceSinkRateError{
			DeviceID:     s.id,
			ProviderRate: s.providerRate,
			DeviceRate:   s.deviceRate,
			Err:          errors.New("provider and device rates must be positive"),
		}
	}
	converted, err := wavio.Resample(samples, s.providerRate, s.deviceRate)
	if err != nil {
		return nil, &RTCDeviceSinkRateError{
			DeviceID:     s.id,
			ProviderRate: s.providerRate,
			DeviceRate:   s.deviceRate,
			Err:          err,
		}
	}
	return converted, nil
}

type rtcDevicePlaybackBuffer struct {
	samples    []int16
	generation uint64
	blocked    bool
}

func (b *rtcDevicePlaybackBuffer) add(samples []int16, generation uint64, blocked bool) [][]int16 {
	if blocked {
		b.samples = b.samples[:0]
		b.generation = generation
		b.blocked = true
		return nil
	}
	if b.generation != generation || b.blocked {
		b.samples = b.samples[:0]
	}
	b.generation = generation
	b.blocked = false
	b.samples = append(b.samples, samples...)

	frameCount := len(b.samples) / audio.FrameSize
	frames := make([][]int16, 0, frameCount)
	for range frameCount {
		frames = append(frames, append([]int16(nil), b.samples[:audio.FrameSize]...))
		b.samples = b.samples[audio.FrameSize:]
	}
	if len(b.samples) == 0 {
		b.samples = nil
	} else {
		b.samples = append([]int16(nil), b.samples...)
	}
	return frames
}

func (b *rtcDevicePlaybackBuffer) flush(generation uint64, blocked bool) []int16 {
	if blocked || b.blocked || b.generation != generation {
		b.samples = nil
		b.generation = generation
		b.blocked = blocked
		return nil
	}
	final := append([]int16(nil), b.samples...)
	b.samples = nil
	return final
}
