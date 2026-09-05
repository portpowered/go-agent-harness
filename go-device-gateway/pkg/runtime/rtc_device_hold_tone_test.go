package runtime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type timestampOnlyTimingSource struct{}

func (timestampOnlyTimingSource) Now() time.Time { return time.Unix(42, 0).UTC() }

func TestSessionTimingClockRejectsTimestampOnlyInjection(t *testing.T) {
	ctx := WithTimingClock(context.Background(), timestampOnlyTimingSource{})
	if source, ok := TimingClock(ctx); ok || source != nil {
		t.Fatalf("timestamp-only timing source resolved as scheduler: source=%T ok=%v", source, ok)
	}

	deterministic := platformclock.NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	ctx = WithTimingClock(context.Background(), deterministic)
	source, ok := TimingClock(ctx)
	if !ok || source != deterministic {
		t.Fatalf("deterministic timing source was not preserved: source=%T ok=%v", source, ok)
	}
}

func TestHoldToneCheckedStartReportsInvalidTimingClock(t *testing.T) {
	sink := &RTCDeviceSink{}
	ctx := WithTimingClock(context.Background(), timestampOnlyTimingSource{})
	stop, err := sink.StartHoldToneChecked(ctx)
	if !errors.Is(err, ErrInvalidSessionTimingClock) {
		t.Fatalf("checked hold-tone start error: got %v, want ErrInvalidSessionTimingClock", err)
	}
	stop()
}

// stepRTCInboundMedia lets a test control exactly when each provider frame
// "arrives", so it can hold a gap open long enough to observe the hold-tone
// cue and then resume real audio on demand.
type stepRTCInboundMedia struct {
	frames chan audio.PCMFrame
}

func newStepRTCInboundMedia() *stepRTCInboundMedia {
	return &stepRTCInboundMedia{frames: make(chan audio.PCMFrame, 4)}
}

func (m *stepRTCInboundMedia) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case frame, ok := <-m.frames:
		if !ok {
			return audio.PCMFrame{}, io.EOF
		}
		return frame, nil
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}

func (m *stepRTCInboundMedia) Close() error { return nil }

func containsInt16Subsequence(haystack, needle []int16) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		match := true
		for i, want := range needle {
			if haystack[start+i] != want {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func testHoldToneSinkConfig() audio.HoldToneConfig {
	return audio.HoldToneConfig{
		GapThreshold:  40 * time.Millisecond,
		PulseInterval: 30 * time.Millisecond,
		PulseDuration: 25 * time.Millisecond,
		Amplitude:     6000,
		ToneHz1:       440,
		ToneHz2:       660,
	}
}

func readUntilSignalOrDeadline(t *testing.T, source *devicegw.DeviceSource, deadline time.Time) bool {
	t.Helper()
	for time.Now().Before(deadline) {
		frame := make([]int16, audio.FrameSize)
		readCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := source.ReadFrame(readCtx, frame)
		cancel()
		if err != nil {
			continue
		}
		if hasNonZeroSamples(frame) {
			return true
		}
	}
	return false
}

// TestRTCDeviceSinkHoldToneFillsGapLongerThanThreshold pins the primary
// customer-facing requirement: once no real assistant audio has reached the
// local device for longer than GapThreshold, the sink must produce audible,
// non-silent PCM on that device, not true digital silence.
func TestRTCDeviceSinkHoldToneFillsGapLongerThanThreshold(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	source, err := devicegw.NewDeviceSource(registry, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	sink, err := NewDefaultRTCDeviceSink(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	sink.SetHoldToneConfig(testHoldToneSinkConfig())
	sink.SetHoldToneTick(5 * time.Millisecond)

	inbound := newStepRTCInboundMedia()
	first := make([]int16, audio.FrameSize)
	for i := range first {
		first[i] = int16(i%50 + 1)
	}
	inbound.frames <- audio.PCMFrame{Samples: first}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- sink.Pump(ctx, inbound) }()

	// Drain the initial real frame so the gap clock starts from it.
	got := make([]int16, audio.FrameSize)
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	err = source.ReadFrame(readCtx, got)
	readCancel()
	if err != nil {
		t.Fatalf("read initial real frame: %v", err)
	}

	if !readUntilSignalOrDeadline(t, source, time.Now().Add(700*time.Millisecond)) {
		t.Fatal("no hold-tone content observed on the device after the gap exceeded GapThreshold")
	}

	cancel()
	select {
	case <-pumpDone:
	case <-time.After(time.Second):
		t.Fatal("Pump did not stop after cancellation")
	}
}

// TestRTCDeviceSinkHoldToneStaysSilentForShortGap pins the complementary
// requirement: an ordinary short gap -- well under the production
// GapThreshold -- must never produce filler content. This uses the sink's
// default (production) configuration so the assertion reflects real
// behavior, not a test-only threshold.
func TestRTCDeviceSinkHoldToneStaysSilentForShortGap(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	source, err := devicegw.NewDeviceSource(registry, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	sink, err := NewDefaultRTCDeviceSink(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	// Tick quickly so a bug that ignores GapThreshold would show up fast;
	// GapThreshold itself is left at its production default (2.5s).
	sink.SetHoldToneTick(5 * time.Millisecond)

	inbound := newStepRTCInboundMedia()
	first := make([]int16, audio.FrameSize)
	for i := range first {
		first[i] = int16(i%50 + 1)
	}
	inbound.frames <- audio.PCMFrame{Samples: first}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- sink.Pump(ctx, inbound) }()

	got := make([]int16, audio.FrameSize)
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	err = source.ReadFrame(readCtx, got)
	readCancel()
	if err != nil {
		t.Fatalf("read initial real frame: %v", err)
	}

	if readUntilSignalOrDeadline(t, source, time.Now().Add(200*time.Millisecond)) {
		t.Fatal("hold-tone content appeared before GapThreshold elapsed, want silence for an ordinary short gap")
	}

	cancel()
	select {
	case <-pumpDone:
	case <-time.After(time.Second):
		t.Fatal("Pump did not stop after cancellation")
	}
}

// TestRTCDeviceSinkHoldToneRealAudioReachesDeviceUnmodifiedAfterGap proves
// the cue stops and hands the device back cleanly: once real assistant
// audio resumes after a long gap, it must reach the device byte-for-byte,
// not mixed, delayed, or overlaid with filler content.
func TestRTCDeviceSinkHoldToneRealAudioReachesDeviceUnmodifiedAfterGap(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	source, err := devicegw.NewDeviceSource(registry, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	sink, err := NewDefaultRTCDeviceSink(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	sink.SetHoldToneConfig(testHoldToneSinkConfig())
	sink.SetHoldToneTick(5 * time.Millisecond)

	inbound := newStepRTCInboundMedia()
	first := make([]int16, audio.FrameSize)
	for i := range first {
		first[i] = int16(i%50 + 1)
	}
	inbound.frames <- audio.PCMFrame{Samples: first}

	ctx := context.Background()
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- sink.Pump(ctx, inbound) }()

	got := make([]int16, audio.FrameSize)
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	err = source.ReadFrame(readCtx, got)
	readCancel()
	if err != nil {
		t.Fatalf("read initial real frame: %v", err)
	}

	if !readUntilSignalOrDeadline(t, source, time.Now().Add(700*time.Millisecond)) {
		t.Fatal("no hold-tone content observed before resuming real audio")
	}

	second := make([]int16, audio.FrameSize)
	for i := range second {
		second[i] = int16(-(i%40 + 1))
	}
	// A trailing padding frame ensures enough total content remains queued
	// after the read that already consumed the fade-out tail plus the
	// leading part of "second" -- otherwise the last few samples of
	// "second" would sit below DeviceSource.ReadFrame's fixed FrameSize
	// read granularity forever once Pump has nothing left to send.
	padding := make([]int16, audio.FrameSize)
	for i := range padding {
		padding[i] = 777
	}
	inbound.frames <- audio.PCMFrame{Samples: second}
	inbound.frames <- audio.PCMFrame{Samples: padding}
	close(inbound.frames)

	// The device exposes a fixed-size read window over a running playback
	// queue; the short fade-out tail ahead of "second" can shift it off a
	// clean FrameSize boundary even though every sample still reaches the
	// device in order. Accumulate reads and look for "second" as a
	// contiguous subsequence instead of requiring frame alignment.
	var accumulated []int16
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !containsInt16Subsequence(accumulated, second) {
		frame := make([]int16, audio.FrameSize)
		readCtx, readCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := source.ReadFrame(readCtx, frame)
		readCancel()
		if err != nil {
			continue
		}
		accumulated = append(accumulated, frame...)
	}
	if !containsInt16Subsequence(accumulated, second) {
		t.Fatal("the real frame written after the gap never reached the device unmodified")
	}

	select {
	case err := <-pumpDone:
		if err != nil {
			t.Fatalf("Pump returned an error after EOF: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Pump did not stop after EOF")
	}
}

// TestRTCDeviceSinkHoldToneStopsImmediatelyOnDiscardPlayback pins the hard
// barge-in constraint: DiscardPlayback is the exact mechanism a genuine
// RESPONSE.CANCEL invokes on this sink (see rtc_device_runtime.go
// SendWithOutcome). A hold-tone cue that was actively writing must stop
// contributing anything the instant DiscardPlayback runs, the same way real
// playback already does -- proving the cue can never delay or mask a
// genuine interruption.
func TestRTCDeviceSinkHoldToneStopsImmediatelyOnDiscardPlayback(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	source, err := devicegw.NewDeviceSource(registry, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	sink, err := NewDefaultRTCDeviceSink(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	sink.SetHoldToneConfig(testHoldToneSinkConfig())
	sink.SetHoldToneTick(5 * time.Millisecond)

	inbound := newStepRTCInboundMedia()
	first := make([]int16, audio.FrameSize)
	for i := range first {
		first[i] = int16(i%50 + 1)
	}
	inbound.frames <- audio.PCMFrame{Samples: first}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pumpDone := make(chan error, 1)
	go func() { pumpDone <- sink.Pump(ctx, inbound) }()

	got := make([]int16, audio.FrameSize)
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	err = source.ReadFrame(readCtx, got)
	readCancel()
	if err != nil {
		t.Fatalf("read initial real frame: %v", err)
	}

	if !readUntilSignalOrDeadline(t, source, time.Now().Add(700*time.Millisecond)) {
		t.Fatal("no hold-tone content observed before simulated barge-in")
	}

	// Simulate the exact barge-in path: a RESPONSE.CANCEL discards queued
	// local playback and blocks the current playback generation.
	discarded := sink.DiscardPlayback()
	if discarded < 0 {
		t.Fatalf("DiscardPlayback returned %d, want a non-negative discarded-sample count", discarded)
	}

	// Drain whatever was already physically queued before the discard took
	// effect, then confirm nothing new (filler or otherwise) arrives.
	drainDeadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(drainDeadline) {
		frame := make([]int16, audio.FrameSize)
		readCtx, readCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_ = source.ReadFrame(readCtx, frame)
		readCancel()
	}
	settledDeadline := time.Now().Add(150 * time.Millisecond)
	if readUntilSignalOrDeadline(t, source, settledDeadline) {
		t.Fatal("hold-tone content kept arriving after DiscardPlayback, want the cue to stop immediately like real playback")
	}

	cancel()
	select {
	case <-pumpDone:
	case <-time.After(time.Second):
		t.Fatal("Pump did not stop after cancellation")
	}
}
