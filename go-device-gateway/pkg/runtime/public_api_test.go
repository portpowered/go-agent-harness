package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestBufferedCaptureCapabilitiesShareOneQueue(t *testing.T) {
	var unset *BufferedCapture
	if !unset.CaptureBufferSnapshot().Closed {
		t.Fatal("nil BufferedCapture snapshot is not closed")
	}
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened, err := devicegw.NewDeviceSourceAtRate(registry, "virtual:input", audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	source := NewRTCDeviceSourceFromOpened(opened, audio.SampleRate, audio.SampleRate)
	defer closeForTest(t, "source", source)
	buffer, err := NewBufferedCapture(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := buffer.Producer().TrySubmit(audio.PCMFrame{Samples: make([]int16, audio.FrameSize)}); err != nil {
		t.Fatalf("TrySubmit: %v", err)
	}
	if stats := buffer.Control().Snapshot(); stats.QueuedFrames != 1 {
		t.Fatalf("queued frames = %d after one submit, want 1", stats.QueuedFrames)
	}
	frame, ok, err := buffer.Consumer().TryReceive()
	if err != nil || !ok || len(frame.Samples) != audio.FrameSize {
		t.Fatalf("TryReceive() = %d samples, %v, %v", len(frame.Samples), ok, err)
	}
	if stats := buffer.CaptureBufferSnapshot(); stats.QueuedFrames != 0 || stats.Closed {
		t.Fatalf("snapshot after receive = %#v", stats)
	}
}

func TestNewRTCDeviceSinkFromOpenedAdoptsTheEndpoint(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	opened, err := devicegw.NewDeviceSinkAtRate(registry, "virtual:output", audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	sink := NewRTCDeviceSinkFromOpened(opened, audio.SampleRate, audio.SampleRate, "", nil)
	if sink.DeviceID() != "virtual:output" {
		t.Fatalf("DeviceID() = %q, want virtual:output", sink.DeviceID())
	}
	closeForTest(t, "sink", sink)
}

func TestPublicMediaHelpers(t *testing.T) {
	if got := ConsumedPlaybackSamples(audio.PlaybackQueueStats{RenderedSamples: 960, UnderflowSamples: 160}); got != 800 {
		t.Fatalf("ConsumedPlaybackSamples = %d, want rendered minus underflow", got)
	}
	if got := ConsumedPlaybackSamples(audio.PlaybackQueueStats{RenderedSamples: 1, UnderflowSamples: 2}); got != 0 {
		t.Fatalf("ConsumedPlaybackSamples with more underflow than rendered = %d, want 0", got)
	}
	var typedNilOut *recordingRTCOutboundMedia
	if !IsNilOutboundMedia(nil) || !IsNilOutboundMedia(typedNilOut) || IsNilOutboundMedia(&recordingRTCOutboundMedia{}) {
		t.Fatal("IsNilOutboundMedia misclassifies nil, typed nil or a live endpoint")
	}
	if !IsNilInboundMedia(nil) {
		t.Fatal("IsNilInboundMedia(nil) = false")
	}
}

type nowOnlyClock struct{}

func (nowOnlyClock) Now() time.Time { return time.Time{} }

func TestValidateTimingClockRequiresTimers(t *testing.T) {
	if err := ValidateTimingClock(nil); err != nil {
		t.Fatalf("ValidateTimingClock(nil) = %v", err)
	}
	if err := ValidateTimingClock(clock.Real{}); err != nil {
		t.Fatalf("ValidateTimingClock(Real) = %v", err)
	}
	if err := ValidateTimingClock(nowOnlyClock{}); !errors.Is(err, ErrInvalidSessionTimingClock) {
		t.Fatalf("ValidateTimingClock(now-only) = %v, want ErrInvalidSessionTimingClock", err)
	}
}
