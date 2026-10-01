package runtime

import (
	"context"
	"errors"
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// A provider write failure must not return while the capture worker is still
// running: the source is immediately reusable once the pump reports failure.
func TestPumpBufferedCaptureJoinsWorkerOnWriteFailure(t *testing.T) {
	wantErr := errors.New("track write failed")
	for attempt := range 10 {
		registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
		if err != nil {
			t.Fatal(err)
		}
		sink, err := devicegw.NewDeviceSink(registry, "virtual:output")
		if err != nil {
			t.Fatal(err)
		}
		source, err := NewRTCDeviceSource(registry, "virtual:input")
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.WriteFrame(context.Background(), make([]int16, audio.FrameSize)); err != nil {
			t.Fatal(err)
		}
		err = PumpBufferedCapture(context.Background(), source, &recordingRTCOutboundMedia{writeErr: wantErr})
		if !errors.Is(err, wantErr) {
			t.Fatalf("attempt %d: pump error = %v, want %v", attempt, err, wantErr)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := source.Pump(canceled, &recordingRTCOutboundMedia{}); errors.Is(err, ErrRTCDeviceSourceRunning) {
			t.Fatalf("attempt %d: capture worker still running after the failed pump returned", attempt)
		}
		closeForTest(t, "source", source)
		closeForTest(t, "sink", sink)
	}
}
