package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestVirtualPlaybackCapacityAdversarial exercises the pacing contract at
// hostile boundaries. Each named subtest is an independent scenario so a
// failure identifies the exact wakeup, capacity, lifecycle, or fidelity rule
// that regressed.
func TestVirtualPlaybackCapacityAdversarial(t *testing.T) {
	for _, scenario := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"01 zero request is a no-op", capacityZeroRequestIsANoOp},
		{"02 negative request is a no-op", capacityNegativeRequestIsANoOp},
		{"03 nil stream request is a no-op", capacityNilStreamRequestIsANoOp},
		{"04 oversized request is rejected", capacityOversizedRequestIsRejected},
		{"05 below high watermark is admitted", capacityBelowHighWatermarkIsAdmitted},
		{"06 exact high watermark is admitted", capacityExactHighWatermarkIsAdmitted},
		{"07 one sample above high watermark blocks", capacityOneSampleAboveHighWatermarkBlocks},
		{"08 waiter remains blocked above low watermark", capacityWaiterRemainsBlockedAboveLowWatermark},
		{"09 context cancellation wakes waiter", capacityContextCancellationWakesWaiter},
		{"10 deadline wakes waiter", capacityDeadlineWakesWaiter},
		{"11 discard wakes waiter", capacityDiscardWakesWaiter},
		{"12 output close wakes waiter", capacityOutputCloseWakesWaiter},
		{"13 peer close wakes waiter", capacityPeerCloseWakesWaiter},
		{"14 output removal wakes waiter", capacityOutputRemovalWakesWaiter},
		{"15 peer removal wakes waiter", capacityPeerRemovalWakesWaiter},
		{"16 exact final remainder round trips", capacityExactFinalRemainderRoundTrips},
		{"17 cancelled exact read returns context error", capacityCancelledExactReadReturnsContextError},
		{"18 sustained paced FIFO has no drops", capacitySustainedPacedFIFOHasNoDrops},
		{"19 48 kHz watermarks scale by duration", capacity48kHzWatermarksScaleByDuration},
		{"20 malformed format rejects watermarks", capacityMalformedFormatRejectsWatermarks},
	} {
		t.Run(scenario.name, scenario.run)
	}
}

func capacityZeroRequestIsANoOp(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	if err := output.WaitForPlaybackCapacity(context.Background(), 0); err != nil {
		t.Fatalf("zero capacity request: %v", err)
	}
}

func capacityNegativeRequestIsANoOp(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	if err := output.WaitForPlaybackCapacity(context.Background(), -1); err != nil {
		t.Fatalf("negative capacity request: %v", err)
	}
}

func capacityNilStreamRequestIsANoOp(t *testing.T) {
	var output *VirtualStream
	if err := output.WaitForPlaybackCapacity(context.Background(), audio.FrameSize); err != nil {
		t.Fatalf("nil stream capacity request: %v", err)
	}
}

func capacityOversizedRequestIsRejected(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	_, high, err := audio.PlaybackQueueWatermarks(output.DeviceFormat())
	if err != nil {
		t.Fatal(err)
	}
	if err := output.WaitForPlaybackCapacity(context.Background(), high+1); !errors.Is(err, audio.ErrInvalidPlaybackQueue) {
		t.Fatalf("oversized request error = %v, want ErrInvalidPlaybackQueue", err)
	}
}

func capacityBelowHighWatermarkIsAdmitted(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	if err := output.WaitForPlaybackCapacity(context.Background(), audio.FrameSize); err != nil {
		t.Fatalf("below-high request: %v", err)
	}
}

func capacityExactHighWatermarkIsAdmitted(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	_, high, err := audio.PlaybackQueueWatermarks(output.DeviceFormat())
	if err != nil {
		t.Fatal(err)
	}
	primeVirtualPlayback(t, output, high-audio.FrameSize)
	if err := output.WaitForPlaybackCapacity(context.Background(), audio.FrameSize); err != nil {
		t.Fatalf("exact-high request: %v", err)
	}
}

func capacityOneSampleAboveHighWatermarkBlocks(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	_, high, err := audio.PlaybackQueueWatermarks(output.DeviceFormat())
	if err != nil {
		t.Fatal(err)
	}
	primeVirtualPlayback(t, output, high)
	ctx, cancel := context.WithCancel(context.Background())
	wait := startCapacityWait(output, ctx, 1)
	assertCapacityWaitBlocked(t, wait)
	cancel()
	if err := awaitCapacityWait(t, wait); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked wait cancellation = %v", err)
	}
}

func capacityWaiterRemainsBlockedAboveLowWatermark(t *testing.T) {
	_, output, input := adversarialVirtualPair(t, audio.SampleRate)
	low, high, err := audio.PlaybackQueueWatermarks(output.DeviceFormat())
	if err != nil {
		t.Fatal(err)
	}
	primeVirtualPlayback(t, output, high)
	wait := startCapacityWait(output, context.Background(), audio.FrameSize)
	for output.PlaybackStats().QueuedSamples-audio.FrameSize > low {
		if err := input.ReadSamples(context.Background(), make([]int16, audio.FrameSize)); err != nil {
			t.Fatal(err)
		}
		assertCapacityWaitBlocked(t, wait)
	}
	if err := input.ReadSamples(context.Background(), make([]int16, audio.FrameSize)); err != nil {
		t.Fatal(err)
	}
	if err := awaitCapacityWait(t, wait); err != nil {
		t.Fatalf("wait at low watermark: %v", err)
	}
}

func capacityContextCancellationWakesWaiter(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	high := playbackHighWatermark(t, output)
	primeVirtualPlayback(t, output, high)
	ctx, cancel := context.WithCancel(context.Background())
	wait := startCapacityWait(output, ctx, audio.FrameSize)
	cancel()
	if err := awaitCapacityWait(t, wait); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled capacity wait = %v", err)
	}
}

func capacityDeadlineWakesWaiter(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	high := playbackHighWatermark(t, output)
	primeVirtualPlayback(t, output, high)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := output.WaitForPlaybackCapacity(ctx, audio.FrameSize); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline capacity wait = %v", err)
	}
}

func capacityDiscardWakesWaiter(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	high := playbackHighWatermark(t, output)
	primeVirtualPlayback(t, output, high)
	wait := startCapacityWait(output, context.Background(), audio.FrameSize)
	if got := output.DiscardPlayback(); got != high {
		t.Fatalf("discard = %d, want %d", got, high)
	}
	if err := awaitCapacityWait(t, wait); err != nil {
		t.Fatalf("capacity wait after discard: %v", err)
	}
}

func capacityOutputCloseWakesWaiter(t *testing.T) {
	_, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	high := playbackHighWatermark(t, output)
	primeVirtualPlayback(t, output, high)
	wait := startCapacityWait(output, context.Background(), audio.FrameSize)
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if err := awaitCapacityWait(t, wait); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("capacity wait after output close = %v, want ErrClosed", err)
	}
}

func capacityPeerCloseWakesWaiter(t *testing.T) {
	_, output, input := adversarialVirtualPair(t, audio.SampleRate)
	high := playbackHighWatermark(t, output)
	primeVirtualPlayback(t, output, high)
	wait := startCapacityWait(output, context.Background(), audio.FrameSize)
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := awaitCapacityWait(t, wait); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("capacity wait after peer close = %v, want ErrClosed", err)
	}
}

func capacityOutputRemovalWakesWaiter(t *testing.T) {
	registry, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	high := playbackHighWatermark(t, output)
	primeVirtualPlayback(t, output, high)
	wait := startCapacityWait(output, context.Background(), audio.FrameSize)
	if !registry.RemoveDevice("virtual:output") {
		t.Fatal("remove output returned false")
	}
	if err := awaitCapacityWait(t, wait); !errors.Is(err, ErrDeviceLost) {
		t.Fatalf("capacity wait after output removal = %v, want ErrDeviceLost", err)
	}
}

func capacityPeerRemovalWakesWaiter(t *testing.T) {
	registry, output, _ := adversarialVirtualPair(t, audio.SampleRate)
	high := playbackHighWatermark(t, output)
	primeVirtualPlayback(t, output, high)
	wait := startCapacityWait(output, context.Background(), audio.FrameSize)
	if !registry.RemoveDevice("virtual:input") {
		t.Fatal("remove input returned false")
	}
	if err := awaitCapacityWait(t, wait); !errors.Is(err, ErrDeviceLost) {
		t.Fatalf("capacity wait after peer removal = %v, want ErrDeviceLost", err)
	}
}

func capacityExactFinalRemainderRoundTrips(t *testing.T) {
	_, output, input := adversarialVirtualPair(t, audio.SampleRate)
	want := make([]int16, audio.FrameSize-1)
	for index := range want {
		want[index] = int16(index - 200)
	}
	if err := output.WriteSamples(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got := make([]int16, len(want))
	if err := input.ReadSamples(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("final remainder changed across virtual callback")
	}
}

func capacityCancelledExactReadReturnsContextError(t *testing.T) {
	_, _, input := adversarialVirtualPair(t, audio.SampleRate)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := input.ReadSamples(ctx, make([]int16, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ReadSamples = %v", err)
	}
}

func capacitySustainedPacedFIFOHasNoDrops(t *testing.T) {
	_, output, input := adversarialVirtualPair(t, audio.SampleRate)
	const frames = 40
	producerErr := make(chan error, 1)
	go func() {
		for frameIndex := 0; frameIndex < frames; frameIndex++ {
			frame := make([]int16, audio.FrameSize)
			for sampleIndex := range frame {
				frame[sampleIndex] = int16(frameIndex*audio.FrameSize + sampleIndex)
			}
			if err := output.WaitForPlaybackCapacity(context.Background(), len(frame)); err != nil {
				producerErr <- err
				return
			}
			if err := output.WriteFrame(context.Background(), frame); err != nil {
				producerErr <- err
				return
			}
		}
		producerErr <- nil
	}()
	for frameIndex := 0; frameIndex < frames; frameIndex++ {
		got := make([]int16, audio.FrameSize)
		if err := input.ReadFrame(context.Background(), got); err != nil {
			t.Fatal(err)
		}
		if got[0] != int16(frameIndex*audio.FrameSize) || got[len(got)-1] != int16(frameIndex*audio.FrameSize+audio.FrameSize-1) {
			t.Fatalf("frame %d reordered: first=%d last=%d", frameIndex, got[0], got[len(got)-1])
		}
	}
	if err := awaitCapacityWait(t, producerErr); err != nil {
		t.Fatalf("paced producer: %v", err)
	}
	stats := output.PlaybackStats()
	if stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("sustained paced stats = %+v", stats)
	}
}

func capacity48kHzWatermarksScaleByDuration(t *testing.T) {
	low, high, err := audio.PlaybackQueueWatermarks(audio.PCM16DeviceFormat(48000))
	if err != nil {
		t.Fatal(err)
	}
	if low != 5760 || high != 8640 {
		t.Fatalf("48 kHz watermarks = %d/%d, want 5760/8640", low, high)
	}
}

func capacityMalformedFormatRejectsWatermarks(t *testing.T) {
	if _, _, err := audio.PlaybackQueueWatermarks(audio.DeviceFormat{}); !errors.Is(err, audio.ErrInvalidPlaybackQueue) {
		t.Fatalf("malformed format error = %v, want ErrInvalidPlaybackQueue", err)
	}
}

func adversarialVirtualPair(t *testing.T, rate int) (*VirtualRegistry, *VirtualStream, *VirtualStream) {
	t.Helper()
	capability := VirtualCapability{SampleRate: rate, Channels: audio.Channels, BitDepth: audio.DeviceBitDepthPCM16, Format: audio.DeviceEncodingPCM16}
	registry, err := NewVirtualRegistry(VirtualBackendConfig{
		Devices: []VirtualDeviceConfig{
			{ID: "input", Name: "Input", Direction: DirectionInput, Capabilities: []VirtualCapability{capability}, LoopbackID: "output"},
			{ID: "output", Name: "Output", Direction: DirectionOutput, Capabilities: []VirtualCapability{capability}, LoopbackID: "input"},
		},
		Defaults: map[Direction]string{DirectionInput: "input", DirectionOutput: "output"},
	})
	if err != nil {
		t.Fatal(err)
	}
	openedOutput, err := registry.OpenWithFormat("virtual:output", audio.PCM16DeviceFormat(rate))
	if err != nil {
		t.Fatal(err)
	}
	openedInput, err := registry.OpenWithFormat("virtual:input", audio.PCM16DeviceFormat(rate))
	if err != nil {
		_ = openedOutput.Close()
		t.Fatal(err)
	}
	output := openedOutput.(*VirtualStream)
	input := openedInput.(*VirtualStream)
	t.Cleanup(func() {
		_ = output.Close()
		_ = input.Close()
	})
	return registry, output, input
}

func primeVirtualPlayback(t *testing.T, output *VirtualStream, samples int) {
	t.Helper()
	if samples <= 0 {
		return
	}
	if err := output.WriteSamples(context.Background(), make([]int16, samples)); err != nil {
		t.Fatalf("prime virtual playback: %v", err)
	}
}

func startCapacityWait(output *VirtualStream, ctx context.Context, samples int) <-chan error {
	done := make(chan error, 1)
	go func() { done <- output.WaitForPlaybackCapacity(ctx, samples) }()
	return done
}

func assertCapacityWaitBlocked(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("capacity wait returned while it should be blocked: %v", err)
	default:
	}
}

func awaitCapacityWait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("capacity wait did not terminate")
		return nil
	}
}

func playbackHighWatermark(t *testing.T, output *VirtualStream) int {
	t.Helper()
	_, high, err := audio.PlaybackQueueWatermarks(output.DeviceFormat())
	if err != nil {
		t.Fatalf("playback watermarks: %v", err)
	}
	return high
}
