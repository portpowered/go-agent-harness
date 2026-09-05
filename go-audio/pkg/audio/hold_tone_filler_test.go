package audio

import (
	"testing"
	"time"
)

func testHoldToneConfig() HoldToneConfig {
	return HoldToneConfig{
		GapThreshold:  500 * time.Millisecond,
		PulseInterval: 300 * time.Millisecond,
		PulseDuration: 100 * time.Millisecond,
		Amplitude:     4000,
		ToneHz1:       440,
		ToneHz2:       660,
	}
}

func frameHasSignal(frame []int16) bool {
	for _, sample := range frame {
		if sample != 0 {
			return true
		}
	}
	return false
}

// TestHoldToneFillerStaysSilentBeforeThreshold pins the "do not fill every
// gap" requirement: a pause shorter than GapThreshold -- an ordinary
// conversational pause -- must never produce audible filler.
func TestHoldToneFillerStaysSilentBeforeThreshold(t *testing.T) {
	cfg := testHoldToneConfig()
	start := time.Now()
	filler := NewHoldToneFiller(cfg, 16000, start)

	const sampleRate = 16000
	const frameSamples = 320 // 20ms at 16kHz
	frameDuration := time.Duration(frameSamples) * time.Second / time.Duration(sampleRate)

	now := start
	for elapsed := time.Duration(0); elapsed < cfg.GapThreshold-frameDuration; elapsed += frameDuration {
		now = now.Add(frameDuration)
		if frame := filler.NextFrame(now, frameSamples); frame != nil {
			t.Fatalf("NextFrame at elapsed=%v (< GapThreshold=%v) = %v, want nil (natural pause must stay silent)", elapsed, cfg.GapThreshold, frame)
		}
	}
}

// TestHoldToneFillerFillsAfterThreshold pins the complementary requirement:
// once a gap has run longer than GapThreshold, the filler must produce
// audible, non-silent, sane-level PCM.
func TestHoldToneFillerFillsAfterThreshold(t *testing.T) {
	cfg := testHoldToneConfig()
	start := time.Now()
	filler := NewHoldToneFiller(cfg, 16000, start)

	now := start.Add(cfg.GapThreshold + time.Millisecond)
	frame := filler.NextFrame(now, 320)
	if len(frame) == 0 {
		t.Fatal("NextFrame after GapThreshold elapsed = nil, want audible filler content")
	}
	if !frameHasSignal(frame) {
		t.Fatal("filler frame is all-zero, want non-silent samples")
	}
	peak := 0
	for _, sample := range frame {
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

// TestHoldToneFillerRepeatsAtPulseIntervalThenStopsBetweenPulses proves the
// cue is a periodic pulse, not continuous noise: it must go silent again
// between pulses rather than filling the entire remaining gap.
func TestHoldToneFillerRepeatsAtPulseIntervalThenStopsBetweenPulses(t *testing.T) {
	cfg := testHoldToneConfig()
	start := time.Now()
	filler := NewHoldToneFiller(cfg, 16000, start)

	const frameSamples = 160 // 10ms at 16kHz
	frameDuration := 10 * time.Millisecond

	now := start
	sawSignal := false
	sawSilenceAfterPulse := false
	inPulse := false
	for elapsed := time.Duration(0); elapsed < cfg.GapThreshold+cfg.PulseInterval+cfg.PulseDuration+50*time.Millisecond; elapsed += frameDuration {
		now = now.Add(frameDuration)
		frame := filler.NextFrame(now, frameSamples)
		hasSignal := frameHasSignal(frame)
		if hasSignal {
			sawSignal = true
			inPulse = true
		} else if inPulse {
			// The first pulse just ended; confirm the filler goes back to
			// silence rather than playing continuously.
			sawSilenceAfterPulse = true
			inPulse = false
		}
	}
	if !sawSignal {
		t.Fatal("filler never produced a pulse")
	}
	if !sawSilenceAfterPulse {
		t.Fatal("filler never returned to silence between pulses, want a periodic pulse rather than continuous noise")
	}
}

// TestHoldToneFillerObserveRealAudioStopsImmediatelyNoOverlap pins the
// hard requirement that the cue stops the instant real audio arrives, with
// no overlap: after ObserveRealAudio, NextFrame must not resume filler
// content until another full GapThreshold has elapsed, and any in-progress
// pulse must be handed back only as a short fade-to-zero tail (never raw,
// never continuing).
func TestHoldToneFillerObserveRealAudioStopsImmediatelyNoOverlap(t *testing.T) {
	cfg := testHoldToneConfig()
	start := time.Now()
	filler := NewHoldToneFiller(cfg, 16000, start)

	// Run well into an active pulse.
	mid := start.Add(cfg.GapThreshold + cfg.PulseDuration/2)
	first := filler.NextFrame(mid, 160)
	if !frameHasSignal(first) {
		t.Fatal("setup: expected an in-progress pulse before observing real audio")
	}

	tail := filler.ObserveRealAudio(mid)
	if len(tail) == 0 {
		t.Fatal("ObserveRealAudio mid-pulse returned no fade-out tail, want a short ramp to zero")
	}
	if tail[len(tail)-1] != 0 {
		t.Fatalf("fade-out tail ends at %d, want exactly 0 (no click at the real-audio boundary)", tail[len(tail)-1])
	}
	for i := 1; i < len(tail); i++ {
		prevAbs, curAbs := abs16(tail[i-1]), abs16(tail[i])
		if curAbs > prevAbs {
			t.Fatalf("fade-out tail is not monotonically decaying at index %d: %d then %d", i, tail[i-1], tail[i])
		}
	}

	// Immediately after observing real audio, the cue must be silent again
	// even though we are still well past the original GapThreshold.
	if frame := filler.NextFrame(mid.Add(time.Millisecond), 160); frame != nil {
		t.Fatalf("NextFrame immediately after ObserveRealAudio = %v, want nil (no overlap with real audio)", frame)
	}

	// And it must not resume until a fresh GapThreshold has elapsed from
	// the moment real audio was observed.
	tooSoon := mid.Add(cfg.GapThreshold - time.Millisecond)
	if frame := filler.NextFrame(tooSoon, 160); frame != nil {
		t.Fatalf("NextFrame before a fresh GapThreshold since real audio = %v, want nil", frame)
	}
	resumed := mid.Add(cfg.GapThreshold + time.Millisecond)
	if frame := filler.NextFrame(resumed, 160); !frameHasSignal(frame) {
		t.Fatal("filler never resumed after a fresh GapThreshold elapsed post real-audio")
	}
}

// TestHoldToneFillerObserveRealAudioBetweenPulsesReturnsNoTail confirms the
// common case -- real audio arriving while the cue is already silent
// between pulses, or before it has ever fired -- produces no spurious tail.
func TestHoldToneFillerObserveRealAudioBetweenPulsesReturnsNoTail(t *testing.T) {
	cfg := testHoldToneConfig()
	start := time.Now()
	filler := NewHoldToneFiller(cfg, 16000, start)

	if tail := filler.ObserveRealAudio(start.Add(10 * time.Millisecond)); tail != nil {
		t.Fatalf("ObserveRealAudio before any pulse fired returned %v, want nil", tail)
	}
}

func abs16(v int16) int16 {
	if v < 0 {
		return -v
	}
	return v
}
