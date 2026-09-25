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

// holdToneTestTick is the hold-tone worker cadence used by these tests. The
// worker runs on an injected deterministic clock, so the tick only sets the
// virtual time step; no test waits for it in real time.
const holdToneTestTick = 5 * time.Millisecond

// holdToneHarness drives an RTCDeviceSink's hold-tone worker on a
// deterministic clock and observes every PCM write on the virtual output.
type holdToneHarness struct {
	registry *devicegw.VirtualRegistry
	source   *devicegw.DeviceSource
	sink     *RTCDeviceSink
	clock    *platformclock.Deterministic
	inbound  *stepRTCInboundMedia
	ctx      context.Context
	cancel   context.CancelFunc
	pumpDone chan error
}

// startHoldToneHarness starts Pump with one real provider frame queued and
// consumes that frame from the device, so the gap clock starts from it.
// config nil keeps the sink's production hold-tone configuration.
func startHoldToneHarness(t *testing.T, config *audio.HoldToneConfig) *holdToneHarness {
	t.Helper()
	backend := devicegw.DefaultVirtualBackendConfig()
	backend.RecordPCM = true
	registry, err := devicegw.NewVirtualRegistry(backend)
	if err != nil {
		t.Fatal(err)
	}
	source, err := devicegw.NewDeviceSource(registry, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeForTest(t, "source", source) })
	sink, err := NewDefaultRTCDeviceSink(registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeForTest(t, "sink", sink) })
	if config != nil {
		sink.SetHoldToneConfig(*config)
	}
	sink.SetHoldToneTick(holdToneTestTick)

	h := &holdToneHarness{
		registry: registry, source: source, sink: sink,
		clock:    platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), holdToneTestTick),
		inbound:  newStepRTCInboundMedia(),
		pumpDone: make(chan error, 1),
	}
	first := make([]int16, audio.FrameSize)
	for i := range first {
		first[i] = int16(i%50 + 1)
	}
	h.inbound.frames <- audio.PCMFrame{Samples: first}
	h.ctx, h.cancel = context.WithCancel(WithTimingClock(context.Background(), h.clock))
	t.Cleanup(h.cancel)
	go func() { h.pumpDone <- sink.Pump(h.ctx, h.inbound) }()

	got := make([]int16, audio.FrameSize)
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if err := source.ReadFrame(readCtx, got); err != nil {
		t.Fatalf("read initial real frame: %v", err)
	}
	return h
}

// advance moves virtual time forward one worker tick at a time. Before each
// step it waits for the worker to arm its next timer, which it does only
// after the previous tick's write has completed, so every write caused by a
// step is recorded when advance returns or calls until.
func (h *holdToneHarness) advance(t *testing.T, total time.Duration, until func() bool) bool {
	t.Helper()
	for elapsed := time.Duration(0); elapsed < total; elapsed += holdToneTestTick {
		h.awaitWorkerIdle(t)
		h.clock.AdvanceBy(holdToneTestTick)
		h.awaitWorkerIdle(t)
		if until != nil && until() {
			return true
		}
	}
	return false
}

func (h *holdToneHarness) awaitWorkerIdle(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.clock.WaitForTimers(ctx, 1); err != nil {
		t.Fatalf("hold-tone worker did not arm its next tick: %v", err)
	}
}

// writes returns the number of recorded PCM operations, for use as a
// signalWrittenSince cursor.
func (h *holdToneHarness) writes() int {
	return len(h.registry.PCMObservations())
}

// signalWrittenSince reports whether any non-silent PCM was written to the
// virtual output after cursor.
func (h *holdToneHarness) signalWrittenSince(cursor int) bool {
	observations := h.registry.PCMObservations()
	for _, observation := range observations[min(cursor, len(observations)):] {
		if observation.Operation == "write" && hasNonZeroSamples(observation.Samples) {
			return true
		}
	}
	return false
}

func (h *holdToneHarness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case <-h.pumpDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Pump did not stop after cancellation")
	}
}

// TestRTCDeviceSinkHoldToneFillsGapLongerThanThreshold pins the primary
// customer-facing requirement: once no real assistant audio has reached the
// local device for longer than GapThreshold, the sink must produce audible,
// non-silent PCM on that device, not true digital silence.
func TestRTCDeviceSinkHoldToneFillsGapLongerThanThreshold(t *testing.T) {
	config := testHoldToneSinkConfig()
	h := startHoldToneHarness(t, &config)
	cursor := h.writes()
	if !h.advance(t, config.GapThreshold+config.PulseInterval+config.PulseDuration, func() bool { return h.signalWrittenSince(cursor) }) {
		t.Fatal("no hold-tone content written to the device after the gap exceeded GapThreshold")
	}
	h.stop(t)
}

// TestRTCDeviceSinkHoldToneStaysSilentForShortGap pins the complementary
// requirement: an ordinary gap shorter than the production GapThreshold must
// never produce filler content. It uses the sink's default (production)
// configuration and walks virtual time up to just below that threshold.
func TestRTCDeviceSinkHoldToneStaysSilentForShortGap(t *testing.T) {
	h := startHoldToneHarness(t, nil)
	cursor := h.writes()
	gap := h.sink.holdToneConfig.GapThreshold - holdToneTestTick
	if gap < time.Second {
		t.Fatalf("production GapThreshold = %v, want a multi-second natural-pause allowance", h.sink.holdToneConfig.GapThreshold)
	}
	if h.advance(t, gap, func() bool { return h.signalWrittenSince(cursor) }) {
		t.Fatal("hold-tone content appeared before GapThreshold elapsed, want silence for an ordinary short gap")
	}
	h.stop(t)
}

// TestRTCDeviceSinkHoldToneRealAudioReachesDeviceUnmodifiedAfterGap proves
// the cue stops and hands the device back cleanly: once real assistant
// audio resumes after a long gap, it must reach the device byte-for-byte,
// not mixed, delayed, or overlaid with filler content.
func TestRTCDeviceSinkHoldToneRealAudioReachesDeviceUnmodifiedAfterGap(t *testing.T) {
	config := testHoldToneSinkConfig()
	h := startHoldToneHarness(t, &config)
	cursor := h.writes()
	if !h.advance(t, config.GapThreshold+config.PulseInterval+config.PulseDuration, func() bool { return h.signalWrittenSince(cursor) }) {
		t.Fatal("no hold-tone content written before resuming real audio")
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
	h.inbound.frames <- audio.PCMFrame{Samples: second}
	h.inbound.frames <- audio.PCMFrame{Samples: padding}
	close(h.inbound.frames)

	// The device exposes a fixed-size read window over a running playback
	// queue; the short fade-out tail and any hold-tone chunk ahead of
	// "second" can shift it off a clean FrameSize boundary even though every
	// sample still reaches the device in order. Accumulate reads and look for
	// "second" as a contiguous subsequence instead of requiring alignment.
	var accumulated []int16
	for !containsInt16Subsequence(accumulated, second) {
		frame := make([]int16, audio.FrameSize)
		readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := h.source.ReadFrame(readCtx, frame)
		readCancel()
		if err != nil {
			t.Fatalf("the real frame written after the gap never reached the device unmodified: %v", err)
		}
		accumulated = append(accumulated, frame...)
	}

	select {
	case err := <-h.pumpDone:
		if err != nil {
			t.Fatalf("Pump returned an error after EOF: %v", err)
		}
	case <-time.After(5 * time.Second):
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
	config := testHoldToneSinkConfig()
	h := startHoldToneHarness(t, &config)
	cursor := h.writes()
	if !h.advance(t, config.GapThreshold+config.PulseInterval+config.PulseDuration, func() bool { return h.signalWrittenSince(cursor) }) {
		t.Fatal("no hold-tone content written before simulated barge-in")
	}

	// Simulate the exact barge-in path: a RESPONSE.CANCEL discards queued
	// local playback and blocks the current playback generation. The worker
	// is idle between ticks here, so no write can straddle the discard.
	if discarded := h.sink.DiscardPlayback(); discarded < 0 {
		t.Fatalf("DiscardPlayback returned %d, want a non-negative discarded-sample count", discarded)
	}
	cursor = h.writes()
	// Several pulse intervals of virtual time would each emit a pulse if the
	// cue ignored the discard.
	if h.advance(t, 4*(config.PulseInterval+config.PulseDuration), func() bool { return h.signalWrittenSince(cursor) }) {
		t.Fatal("hold-tone content kept arriving after DiscardPlayback, want the cue to stop immediately like real playback")
	}
	h.stop(t)
}
