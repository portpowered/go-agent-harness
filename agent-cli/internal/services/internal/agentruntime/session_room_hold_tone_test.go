package agentruntime

import (
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func testRoomHoldToneConfig() audio.HoldToneConfig {
	return audio.HoldToneConfig{
		GapThreshold:  400 * time.Millisecond,
		PulseInterval: 300 * time.Millisecond,
		PulseDuration: 100 * time.Millisecond,
		Amplitude:     5000,
		ToneHz1:       440,
		ToneHz2:       660,
	}
}

func allZeroRoomFrame(sampleCount int) []byte {
	return make([]byte, sampleCount*2)
}

// TestApplyRoomHoldToneNilFillerIsNoOp confirms a nil filler (hold tone
// disabled) leaves every frame -- silent or not -- byte-for-byte unchanged.
func TestApplyRoomHoldToneNilFillerIsNoOp(t *testing.T) {
	frame := allZeroRoomFrame(160)
	got := applyRoomHoldTone(nil, time.Now(), frame)
	if len(got) != len(frame) {
		t.Fatalf("applyRoomHoldTone(nil, ...) changed frame length: got %d, want %d", len(got), len(frame))
	}
	for i := range frame {
		if got[i] != frame[i] {
			t.Fatalf("applyRoomHoldTone(nil, ...) modified frame at byte %d", i)
		}
	}
}

// TestApplyRoomHoldToneStaysSilentBeforeThreshold pins the "do not fill
// every gap" requirement for the room path: a silent mixer frame within an
// ordinary conversational pause must reach the speaker unchanged (still
// literally silent), not overridden with the cue.
func TestApplyRoomHoldToneStaysSilentBeforeThreshold(t *testing.T) {
	cfg := testRoomHoldToneConfig()
	start := time.Now()
	filler := audio.NewHoldToneFiller(cfg, 24000, start)

	const sampleCount = 480 // 20ms at 24kHz
	frameDuration := time.Duration(sampleCount) * time.Second / 24000

	now := start
	for elapsed := time.Duration(0); elapsed < cfg.GapThreshold-frameDuration; elapsed += frameDuration {
		now = now.Add(frameDuration)
		silent := allZeroRoomFrame(sampleCount)
		got := applyRoomHoldTone(filler, now, silent)
		if pcm16HasSignal(got) {
			t.Fatalf("applyRoomHoldTone at elapsed=%v (< GapThreshold=%v) produced signal, want the silent frame to pass through unchanged", elapsed, cfg.GapThreshold)
		}
	}
}

// TestApplyRoomHoldToneFillsAfterThreshold pins the complementary
// requirement: once the mixer has reported an all-zero (silent) frame for
// longer than GapThreshold, applyRoomHoldTone must substitute audible,
// non-silent, sane-level PCM for what would otherwise be true digital
// silence delivered to the customer's speaker.
func TestApplyRoomHoldToneFillsAfterThreshold(t *testing.T) {
	cfg := testRoomHoldToneConfig()
	start := time.Now()
	filler := audio.NewHoldToneFiller(cfg, 24000, start)

	silent := allZeroRoomFrame(480)
	now := start.Add(cfg.GapThreshold + time.Millisecond)
	got := applyRoomHoldTone(filler, now, silent)
	if len(got) != len(silent) {
		t.Fatalf("applyRoomHoldTone changed frame length: got %d, want %d", len(got), len(silent))
	}
	if !pcm16HasSignal(got) {
		t.Fatal("applyRoomHoldTone after GapThreshold elapsed left the frame silent, want audible filler content")
	}
	samples := decodeRoomPCM16ForTest(got)
	peak := 0
	for _, sample := range samples {
		abs := int(sample)
		if abs < 0 {
			abs = -abs
		}
		if abs > peak {
			peak = abs
		}
	}
	if peak == 0 || int16(peak) > cfg.Amplitude {
		t.Fatalf("filler frame peak = %d, want a sane level in (0, %d]", peak, cfg.Amplitude)
	}
}

func TestRoomHumanOutputClockUsesInjectedElapsedTime(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	scheduler := platformclock.NewDeterministic(base, time.Millisecond)
	runtime := &roomParticipantRuntime{plan: &roomParticipantPlan{options: SessionRunOptions{Clock: scheduler}}}
	roomClock := roomHumanOutputClock(runtime)
	filler := audio.NewHoldToneFiller(testRoomHoldToneConfig(), 24000, roomClock.Now())
	silent := allZeroRoomFrame(480)
	if got := applyRoomHoldTone(filler, roomClock.Now(), silent); pcm16HasSignal(got) {
		t.Fatal("hold tone fired before injected elapsed deadline")
	}
	scheduler.AdvanceBy(testRoomHoldToneConfig().GapThreshold + time.Millisecond)
	if got := applyRoomHoldTone(filler, roomClock.Now(), silent); !pcm16HasSignal(got) {
		t.Fatal("hold tone did not fire after injected clock advanced past deadline")
	}
}

// TestApplyRoomHoldTonePassesRealSignalThroughUnchanged confirms a mixed
// frame that genuinely carries signal (any active participant) is never
// altered by the filler, and that it resets the gap clock.
func TestApplyRoomHoldTonePassesRealSignalThroughUnchanged(t *testing.T) {
	cfg := testRoomHoldToneConfig()
	start := time.Now()
	filler := audio.NewHoldToneFiller(cfg, 24000, start)

	real := make([]int16, 480)
	for i := range real {
		real[i] = int16(i%100 + 1)
	}
	realBytes := encodeRoomPCM16(real)

	now := start.Add(cfg.GapThreshold + time.Millisecond)
	got := applyRoomHoldTone(filler, now, realBytes)
	if len(got) != len(realBytes) {
		t.Fatalf("real signal frame length changed: got %d, want %d", len(got), len(realBytes))
	}
	for i := range realBytes {
		if got[i] != realBytes[i] {
			t.Fatalf("real signal frame was altered at byte %d: got %d, want %d", i, got[i], realBytes[i])
		}
	}
}

// TestApplyRoomHoldToneStopsImmediatelyOnRealAudioNoOverlap pins the "stop
// cleanly the instant real audio arrives, no overlap" requirement end to
// end through the room integration point: once a pulse is in progress and a
// real (non-silent) mixed frame arrives, the very next silent frame must
// not resume the cue until a fresh GapThreshold elapses, and a mid-pulse
// transition must be prefixed with a short fade-out tail rather than
// truncated abruptly.
func TestApplyRoomHoldToneStopsImmediatelyOnRealAudioNoOverlap(t *testing.T) {
	cfg := testRoomHoldToneConfig()
	start := time.Now()
	filler := audio.NewHoldToneFiller(cfg, 24000, start)

	silent := allZeroRoomFrame(480)
	mid := start.Add(cfg.GapThreshold + cfg.PulseDuration/2)
	inPulse := applyRoomHoldTone(filler, mid, silent)
	if !pcm16HasSignal(inPulse) {
		t.Fatal("setup: expected an in-progress pulse before real audio arrives")
	}

	real := make([]int16, 480)
	for i := range real {
		real[i] = int16(-(i%77 + 1))
	}
	realBytes := encodeRoomPCM16(real)
	got := applyRoomHoldTone(filler, mid, realBytes)

	// The real frame's own bytes must appear intact and last (nothing
	// overlaid on top of them); a fade-out tail may precede them.
	if len(got) < len(realBytes) {
		t.Fatalf("returned frame shorter than the real frame: got %d bytes, want at least %d", len(got), len(realBytes))
	}
	tailStart := len(got) - len(realBytes)
	for i, want := range realBytes {
		if got[tailStart+i] != want {
			t.Fatalf("real frame bytes were altered at offset %d: got %d, want %d", i, got[tailStart+i], want)
		}
	}
	if extra := got[:tailStart]; len(extra) > 0 {
		samples := decodeRoomPCM16ForTest(extra)
		if samples[len(samples)-1] != 0 {
			t.Fatalf("fade-out tail before real audio ends at %d, want exactly 0 (no click at the boundary)", samples[len(samples)-1])
		}
	}

	// Immediately after, a silent frame must stay silent -- no overlap with
	// the real audio that was just observed.
	after := applyRoomHoldTone(filler, mid.Add(time.Millisecond), allZeroRoomFrame(480))
	if pcm16HasSignal(after) {
		t.Fatal("cue resumed immediately after real audio, want silence until a fresh GapThreshold elapses")
	}
}

func decodeRoomPCM16ForTest(pcm []byte) []int16 {
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(uint16(pcm[i*2]) | uint16(pcm[i*2+1])<<8)
	}
	return samples
}
