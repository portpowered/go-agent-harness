package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// holdingCaptureFilter models an echo gate: it holds the first frame, releases
// it with the second, and fails on the third.
type holdingCaptureFilter struct {
	held      []int16
	calls     int
	discarded int
	failure   error
}

func (f *holdingCaptureFilter) FilterCapture(_ context.Context, frame []int16) ([][]int16, error) {
	f.calls++
	switch f.calls {
	case 1:
		f.held = append([]int16(nil), frame...)
		return nil, nil
	case 2:
		released := [][]int16{f.held, append([]int16(nil), frame...)}
		f.held = nil
		return released, nil
	default:
		return nil, f.failure
	}
}

func (f *holdingCaptureFilter) DiscardHeld() { f.discarded++ }

// TestRTCDeviceSourceUploadsOnlyWhatTheCaptureFilterReleases keeps held
// capture away from the provider until the filter releases it, reports the raw
// and uploaded audio to their separate observers, fails the pump with a typed
// filter error, and discards held audio when the pump ends.
func TestRTCDeviceSourceUploadsOnlyWhatTheCaptureFilterReleases(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	speaker, err := devicegw.NewDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "speaker", speaker)
	source, err := NewRTCDeviceSource(registry, "virtual:input")
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "source", source)

	frames := [][]int16{playbackCommandFrame(71), playbackCommandFrame(72), playbackCommandFrame(73)}
	for _, frame := range frames {
		if err := speaker.WriteFrame(t.Context(), frame); err != nil {
			t.Fatalf("script capture: %v", err)
		}
	}
	failure := errors.New("echo gate failed")
	filter := &holdingCaptureFilter{failure: failure}
	source.SetCaptureFilter(filter)
	var preGate, uploaded [][]int16
	source.SetPreGateSamplesObserver(func(_ int, samples []int16) { preGate = append(preGate, append([]int16(nil), samples...)) })
	source.SetUploadedSamplesObserver(func(_ int, samples []int16) { uploaded = append(uploaded, append([]int16(nil), samples...)) })

	outbound := &recordingRTCOutboundMedia{}
	err = source.Run(t.Context(), outbound)
	var sourceErr *RTCDeviceSourceError
	if !errors.As(err, &sourceErr) || sourceErr.Operation != "filter" || !errors.Is(err, failure) || sourceErr.Error() == "" {
		t.Fatalf("pump error = %v, want a typed filter failure", err)
	}
	if !reflect.DeepEqual(preGate, frames) {
		t.Fatalf("pre-gate observer saw %d frames, want every captured frame", len(preGate))
	}
	if !reflect.DeepEqual(uploaded, frames[:2]) || len(outbound.frames) != 2 {
		t.Fatalf("uploaded %d frames (outbound %d), want the two released frames", len(uploaded), len(outbound.frames))
	}
	for index, frame := range outbound.frames {
		if !reflect.DeepEqual(frame.Samples, frames[index]) {
			t.Fatalf("outbound frame %d differs from released capture", index)
		}
	}
	if filter.discarded != 1 {
		t.Fatalf("held capture discarded %d times at pump end, want once", filter.discarded)
	}

	var nilSource *RTCDeviceSource
	nilSource.SetCaptureFilter(filter)
	nilSource.SetCaptureObserver(nil)
	nilSource.SetPreGateSamplesObserver(nil)
	nilSource.SetUploadedSamplesObserver(nil)
	if nilSource.DeviceID() != "" || nilSource.SourceSampleRate() != 0 || nilSource.ProviderSampleRate() != 0 {
		t.Fatal("nil source reports an identity")
	}
	if err := nilSource.Run(t.Context(), outbound); !errors.Is(err, ErrRTCDeviceSourceClosed) {
		t.Fatalf("nil source run = %v, want ErrRTCDeviceSourceClosed", err)
	}
}
