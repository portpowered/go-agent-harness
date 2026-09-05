package runtime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// RTCDeviceSinkRateError describes a provider-to-playback conversion that
// cannot be satisfied at the local device boundary.
type RTCDeviceSinkRateError struct {
	DeviceID     devicegw.DeviceID
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
func openRTCDeviceSinkAtRate(registry devicegw.DeviceRegistry, id devicegw.DeviceID, providerRate int) (*devicegw.DeviceSink, int, error) {
	if _, err := wavio.Resample(nil, providerRate, providerRate); err != nil {
		return nil, 0, &RTCDeviceSinkRateError{DeviceID: id, ProviderRate: providerRate, Err: err}
	}
	sink, err := devicegw.NewDeviceSinkAtRate(registry, id, providerRate)
	if err == nil {
		return sink, sink.SampleRate(), nil
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

		fallback, fallbackErr := devicegw.NewDeviceSinkWithFormat(registry, id, available)
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
