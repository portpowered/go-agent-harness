package services

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

// speechLikeStream builds a broadband, aperiodic PCM stream with a
// word/pause amplitude envelope (formant-filtered noise), which is a much
// closer proxy for TTS assistant speech than a pure tone: real speech has
// little sample-to-sample self-similarity, so its cross-correlation with a
// delayed/filtered copy of itself falls off sharply away from the true lag,
// and any single ~30ms slice can land in a low-energy gap between words.
func speechLikeStream(totalFrames int) [][]int16 {
	totalSamples := totalFrames * audio.FrameSize
	state := uint32(55441)
	white := make([]float64, totalSamples)
	for i := range white {
		state = state*1664525 + 1013904223
		white[i] = float64(int32(state>>16)%20000-10000) / 10000.0
	}
	// Formant-ish resonant lowpass (leaky integrator run twice) turns white
	// noise into a broadband "voiced" texture instead of flat hiss.
	shaped := make([]float64, totalSamples)
	prev1, prev2 := 0.0, 0.0
	for i, v := range white {
		prev1 = 0.85*prev1 + 0.15*v
		prev2 = 0.7*prev2 + 0.3*prev1
		shaped[i] = prev2
	}
	full := make([]float64, totalSamples)
	wordSamples := int(0.24 * float64(audio.SampleRate))
	pauseSamples := int(0.09 * float64(audio.SampleRate))
	cycle := wordSamples + pauseSamples
	for i, v := range shaped {
		envelope := 1.0
		if cycle > 0 && (i%cycle) >= wordSamples {
			envelope = 0.02 // near-silence between words
		}
		full[i] = 9000.0 * envelope * v
	}
	frames := make([][]int16, totalFrames)
	for f := 0; f < totalFrames; f++ {
		frame := make([]int16, audio.FrameSize)
		for i := 0; i < audio.FrameSize; i++ {
			frame[i] = int16(full[f*audio.FrameSize+i])
		}
		frames[f] = frame
	}
	return frames
}

// roomCoupledCopy simulates a realistic, adversarial speaker-into-mic
// acoustic path: a delayed, multi-tap ("room reverb") mix of the playback
// signal plus an independent microphone-noise floor. Real laptop
// chassis/room coupling is never a single clean scaled copy at one lag; it
// is a smeared sum of several delayed reflections plus ambient/self noise,
// which is exactly what suppresses short-window correlation even though the
// signal is unambiguously an acoustic echo of the assistant's own voice.
func roomCoupledCopy(playback [][]int16, delaySamples int, gain float64, noiseAmplitude int32) [][]int16 {
	totalFrames := len(playback)
	if totalFrames == 0 {
		return nil
	}
	frameLen := len(playback[0])
	flat := make([]float64, totalFrames*frameLen)
	for f := range playback {
		for i, s := range playback[f] {
			flat[f*frameLen+i] = float64(s)
		}
	}
	taps := []struct {
		lag  int
		gain float64
	}{
		{delaySamples, gain},
		{delaySamples + 80, gain * 0.45},
		{delaySamples + 160, gain * 0.25},
	}
	state := uint32(21771)
	out := make([][]int16, totalFrames)
	for f := 0; f < totalFrames; f++ {
		frame := make([]int16, frameLen)
		for i := 0; i < frameLen; i++ {
			idx := f*frameLen + i
			v := 0.0
			for _, tap := range taps {
				src := idx - tap.lag
				if src >= 0 && src < len(flat) {
					v += flat[src] * tap.gain
				}
			}
			state = state*1664525 + 1013904223
			noise := int32(state>>16)%(2*noiseAmplitude+1) - noiseAmplitude
			v += float64(noise)
			if v > 32767 {
				v = 32767
			}
			if v < -32768 {
				v = -32768
			}
			frame[i] = int16(v)
		}
		out[f] = frame
	}
	return out
}

// TestLocalFeedbackGateNeverForwardsRealisticSustainedFeedback proves the
// gate suppresses a full, realistic assistant utterance -- not just a run of
// identical playback-equals-capture frames. capture is a delayed, multi-tap,
// noisy acoustic coupling of playback (see roomCoupledCopy), sustained across
// word/pause boundaries the way a real TTS response is. Regression coverage
// for the bug where the post-confirmation probe forwarded any frame it could
// not immediately re-confirm as feedback (including ambiguous low-energy
// frames at word boundaries), which let fragments of the assistant's own
// voice reach the provider throughout a response even after the one-time
// warning had fired.
func TestLocalFeedbackGateNeverForwardsRealisticSustainedFeedback(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := newLocalFeedbackGate(audio.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), audio.SampleRate, audio.SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer gate.Close()

	const totalFrames = 60 // 1.8s of continuous assistant speech at 30ms/frame
	playback := speechLikeStream(totalFrames)
	capture := roomCoupledCopy(playback, 240, 0.4, 900)

	confirmedAt := -1
	var leakedAfterConfirm [][]int16
	for i := 0; i < totalFrames; i++ {
		if err := gate.WritePlayback(context.Background(), playback[i], func() error { return nil }); err != nil {
			t.Fatalf("write playback %d: %v", i, err)
		}
		released, err := gate.FilterCapture(context.Background(), capture[i])
		if err != nil {
			t.Fatalf("filter capture %d: %v", i, err)
		}
		if len(released) > 0 && confirmedAt >= 0 {
			leakedAfterConfirm = append(leakedAfterConfirm, released...)
		}
		select {
		case <-warning:
			if confirmedAt < 0 {
				confirmedAt = i
			}
		default:
		}
	}
	if confirmedAt < 0 {
		t.Fatalf("feedback was never confirmed at all")
	}
	if len(leakedAfterConfirm) > 0 {
		t.Fatalf("%d contaminated frame(s) reached the provider-bound path after feedback was confirmed at frame %d (state=%q lag=%s)", len(leakedAfterConfirm), confirmedAt, gate.state, gate.confirmedLag)
	}
}

// TestLocalFeedbackGateReleasesSustainedIndependentSpeechAfterPlaybackEnds
// proves the gate does not over-suppress: once the assistant has stopped
// talking (no further WritePlayback calls) but the acoustic tail is still
// open, genuinely independent captured audio must still reach the provider,
// not be trapped in suppression forever.
func TestLocalFeedbackGateReleasesSustainedIndependentSpeechAfterPlaybackEnds(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := newLocalFeedbackGate(audio.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), audio.SampleRate, audio.SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer gate.Close()

	for i := 0; i < 5; i++ {
		loop := feedbackSignal(i, 47)
		if err := gate.WritePlayback(context.Background(), loop, func() error { return nil }); err != nil {
			t.Fatalf("write playback %d: %v", i, err)
		}
		if _, err := gate.FilterCapture(context.Background(), loop); err != nil {
			t.Fatalf("filter loop %d: %v", i, err)
		}
	}
	select {
	case <-warning:
	case <-time.After(time.Second):
		t.Fatal("feedback was never confirmed")
	}

	const independentFrames = 10
	want := make([][]int16, independentFrames)
	var got [][]int16
	for i := 0; i < independentFrames; i++ {
		want[i] = feedbackSignal(i, 97)
		released, err := gate.FilterCapture(context.Background(), want[i])
		if err != nil {
			t.Fatalf("filter independent frame %d: %v", i, err)
		}
		got = append(got, released...)
	}
	if len(got) == 0 {
		t.Fatalf("no independent speech was released after playback ended; gate over-suppressed (state=%q lag=%s)", gate.state, gate.confirmedLag)
	}
	// Order must be preserved even though release is delayed by the
	// confirmation streak. A wider far-field lag search may classify a weak
	// coincidental peak in an individual synthetic frame and discard it, so the
	// released frames need only be an in-order subsequence (never reordered or
	// duplicated), not necessarily one suffix.
	if len(got) > len(want) {
		t.Fatalf("released %d frames, more than the %d fed", len(got), len(want))
	}
	wantIndex := 0
	for releasedIndex := range got {
		for wantIndex < len(want) && !equalSamples(got[releasedIndex], want[wantIndex]) {
			wantIndex++
		}
		if wantIndex == len(want) {
			t.Fatalf("released frame %d is reordered, duplicated, or not an independent input frame", releasedIndex)
		}
		wantIndex++
	}
}

// TestLocalFeedbackGateForwardsBargeInDuringActivePlayback proves genuine
// barge-in still gets through while the assistant is still actively
// speaking (not just after playback has ended): the hard case this fix must
// not regress. Playback keeps advancing every frame (as it would for a live
// TTS response) while captured audio is simultaneously an unrelated,
// independent signal the whole time.
func TestLocalFeedbackGateForwardsBargeInDuringActivePlayback(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := newLocalFeedbackGate(audio.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), audio.SampleRate, audio.SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer gate.Close()

	// Establish confirmed feedback first, exactly like a real session: the
	// gate only starts suppressing once it has seen its own echo.
	for i := 0; i < 5; i++ {
		loop := feedbackSignal(i, 47)
		if err := gate.WritePlayback(context.Background(), loop, func() error { return nil }); err != nil {
			t.Fatalf("write playback %d: %v", i, err)
		}
		if _, err := gate.FilterCapture(context.Background(), loop); err != nil {
			t.Fatalf("filter loop %d: %v", i, err)
		}
	}
	select {
	case <-warning:
	case <-time.After(time.Second):
		t.Fatal("feedback was never confirmed")
	}

	// The assistant keeps talking (playback continues) while the user barges
	// in with independent speech at the same time.
	const bargeInFrames = 20
	var released [][]int16
	for i := 5; i < 5+bargeInFrames; i++ {
		loop := feedbackSignal(i, 47)
		if err := gate.WritePlayback(context.Background(), loop, func() error { return nil }); err != nil {
			t.Fatalf("write playback %d: %v", i, err)
		}
		frames, err := gate.FilterCapture(context.Background(), feedbackSignal(i, 97))
		if err != nil {
			t.Fatalf("filter barge-in frame %d: %v", i, err)
		}
		released = append(released, frames...)
	}
	if len(released) == 0 {
		t.Fatalf("no barge-in audio reached the provider-bound path while playback was still active (state=%q)", gate.state)
	}
}

func equalSamples(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
