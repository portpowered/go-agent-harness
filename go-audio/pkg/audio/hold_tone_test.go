package audio

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

func TestHoldTonePulseIsZeroAtBothEdgesAndBelowFullScale(t *testing.T) {
	cfg := DefaultHoldToneConfig()
	pulse := holdTonePulse(cfg, 24000)
	if len(pulse) < 2 {
		t.Fatalf("pulse length = %d, want at least 2 samples", len(pulse))
	}
	if pulse[0] != 0 {
		t.Fatalf("pulse[0] = %d, want 0 (raised-cosine window must start at zero)", pulse[0])
	}
	if pulse[len(pulse)-1] != 0 {
		t.Fatalf("pulse[last] = %d, want 0 (raised-cosine window must end at zero)", pulse[len(pulse)-1])
	}

	peak := 0
	nonZero := 0
	for _, sample := range pulse {
		if sample != 0 {
			nonZero++
		}
		abs := int(sample)
		if abs < 0 {
			abs = -abs
		}
		if abs > peak {
			peak = abs
		}
	}
	if nonZero == 0 {
		t.Fatal("pulse is entirely silent, want audible content")
	}
	if peak == 0 {
		t.Fatal("pulse peak amplitude = 0, want a sane audible level")
	}
	if int16(peak) > cfg.Amplitude {
		t.Fatalf("pulse peak = %d, want at most the configured amplitude %d", peak, cfg.Amplitude)
	}
	// The cue must be well below full scale and below ordinary speech
	// level, so it reads as a background signal and is never mistaken for
	// the assistant talking.
	const fullScale = 32767
	if peak > fullScale/2 {
		t.Fatalf("pulse peak = %d, want comfortably below full scale (%d)", peak, fullScale)
	}
}

func TestHoldTonePulseScalesWithSampleRate(t *testing.T) {
	cfg := DefaultHoldToneConfig()
	pulse16k := holdTonePulse(cfg, 16000)
	pulse24k := holdTonePulse(cfg, 24000)
	wantRatio := 24000.0 / 16000.0
	gotRatio := float64(len(pulse24k)) / float64(len(pulse16k))
	if gotRatio < wantRatio-0.05 || gotRatio > wantRatio+0.05 {
		t.Fatalf("pulse length ratio = %.3f, want ~%.3f (same PulseDuration at both rates)", gotRatio, wantRatio)
	}
}

func TestHoldToneConfigDefaultsFillZeroFields(t *testing.T) {
	cfg := HoldToneConfig{Amplitude: 500}.withDefaults()
	d := DefaultHoldToneConfig()
	if cfg.GapThreshold != d.GapThreshold {
		t.Fatalf("GapThreshold = %v, want default %v", cfg.GapThreshold, d.GapThreshold)
	}
	if cfg.PulseInterval != d.PulseInterval {
		t.Fatalf("PulseInterval = %v, want default %v", cfg.PulseInterval, d.PulseInterval)
	}
	if cfg.PulseDuration != d.PulseDuration {
		t.Fatalf("PulseDuration = %v, want default %v", cfg.PulseDuration, d.PulseDuration)
	}
	if cfg.Amplitude != 500 {
		t.Fatalf("Amplitude = %d, want the explicitly supplied 500", cfg.Amplitude)
	}
}
func TestLocalFeedbackGateSuppressesLoopAndWarnsOnce(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}

	playback := feedbackPlayback(5, 17)
	assertLoopSuppressed(t, gate, playback)
	assertFeedbackWarning(t, warning)
	assertLoopSuppressed(t, gate, playback)
	assertNoFeedbackWarning(t, warning)

	if err := gate.Close(); err != nil {
		t.Fatalf("close feedback gate: %v", err)
	}
	_, err = gate.FilterCapture(context.Background(), playback[0])
	if !errors.Is(err, contract.ErrClosed) {
		t.Fatalf("filter after close = %v, want contract.ErrClosed", err)
	}
}

func feedbackPlayback(frames, seed int) [][]int16 {
	playback := make([][]int16, frames)
	for frameIndex := range playback {
		playback[frameIndex] = feedbackSignal(frameIndex, seed)
	}
	return playback
}

func assertLoopSuppressed(t *testing.T, gate *PCM16FeedbackGate, playback [][]int16) {
	t.Helper()
	for frameIndex, frame := range playback {
		if err := gate.WritePlayback(context.Background(), frame, func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
		released, err := gate.FilterCapture(context.Background(), frame)
		if err != nil {
			t.Fatalf("filter looped capture frame %d: %v", frameIndex, err)
		}
		if len(released) != 0 {
			t.Fatalf("looped capture frame %d released %d frames, want none (state=%q)", frameIndex, len(released), gate.State())
		}
	}
}

func assertFeedbackWarning(t *testing.T, warning <-chan string) {
	t.Helper()
	select {
	case got := <-warning:
		if !strings.Contains(got, "Acoustic feedback detected") || !strings.Contains(got, "headphones") || !strings.Contains(got, "file") {
			t.Fatalf("warning = %q, want diagnosis and headphones/file remedies", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for feedback warning")
	}
}

func assertNoFeedbackWarning(t *testing.T, warning <-chan string) {
	t.Helper()
	select {
	case extra := <-warning:
		t.Fatalf("repeated feedback emitted another warning %q", extra)
	default:
	}
}

func TestLocalFeedbackGateNonIntegralDeviceQuantumStaysMonotonic(t *testing.T) {
	for _, test := range []struct {
		name    string
		rate    int
		samples int
	}{
		{name: "coreaudio_44k1", rate: 44100, samples: 480},
		{name: "coreaudio_48k_variable_quantum", rate: 48000, samples: 683},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertNonIntegralQuantumPosition(t, test.rate, test.samples)
		})
	}
}

func assertNonIntegralQuantumPosition(t *testing.T, rate, samples int) {
	t.Helper()
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), io.Discard, rate, rate)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()
	frame := make([]int16, samples)
	seed := feedbackSignal(0, 113)
	for index := range frame {
		frame[index] = seed[index%len(seed)]
	}
	for index := 0; index < 64; index++ {
		if err := gate.WritePlayback(context.Background(), frame, func() error { return nil }); err != nil {
			t.Fatalf("playback callback %d: %v", index, err)
		}
	}
	want := time.Duration((int64(samples)*int64(time.Second)+int64(rate)/2)/int64(rate)) * 64
	if gate.PlaybackPosition() != want {
		t.Fatalf("playback position=%s, want rounded sample cursor %s", gate.PlaybackPosition(), want)
	}
}

// TestLocalFeedbackGateFeedbackConfirmedTracksWarningState pins the exact
// contract rtc_device_hold_tone.go relies on to stop re-arming itself once
// local hardware has demonstrated real speaker->mic coupling: false before
// any loop has been confirmed, true from the moment the one-time warning
// fires onward (monotonic, never resets), and false for a nil gate (a sink
// with no paired local feedback gate at all).
func TestLocalFeedbackGateFeedbackConfirmedTracksWarningState(t *testing.T) {
	var nilGate *PCM16FeedbackGate
	if nilGate.FeedbackConfirmed() {
		t.Fatal("nil gate FeedbackConfirmed() = true, want false")
	}

	warning := make(chan string, 1)
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()

	if gate.FeedbackConfirmed() {
		t.Fatal("FeedbackConfirmed() = true before any loop was ever observed")
	}

	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		loop := feedbackSignal(frameIndex, 31)
		if err := gate.WritePlayback(context.Background(), loop, func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
		if _, err := gate.FilterCapture(context.Background(), loop); err != nil {
			t.Fatalf("filter looped capture frame %d: %v", frameIndex, err)
		}
	}
	select {
	case <-warning:
	case <-time.After(time.Second):
		t.Fatal("feedback was never confirmed")
	}
	if !gate.FeedbackConfirmed() {
		t.Fatal("FeedbackConfirmed() = false immediately after the loop was confirmed")
	}

	// The independent, uncorrelated capture below eventually drains the
	// gate back to idle, but FeedbackConfirmed must stay true: it records
	// that this hardware pairing has coupling, not the gate's current
	// suppressing/idle state.
	for frameIndex := 5; frameIndex < 15; frameIndex++ {
		if _, err := gate.FilterCapture(context.Background(), feedbackSignal(frameIndex, 71)); err != nil {
			t.Fatalf("filter independent capture frame %d: %v", frameIndex, err)
		}
	}
	if !gate.FeedbackConfirmed() {
		t.Fatal("FeedbackConfirmed() = false after the gate drained back to idle, want it to stay true for the gate's lifetime")
	}
}

func TestLocalFeedbackGateReanchorsCaptureAfterPrePlaybackLead(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()

	// A live microphone is already pumping while the provider is preparing its
	// first response. Those frames are ordinary input and must not make the
	// first later speaker frame look unrelated on the gate's logical timeline.
	for frameIndex := 0; frameIndex < 20; frameIndex++ {
		released, filterErr := gate.FilterCapture(context.Background(), feedbackSignal(frameIndex, 73))
		if filterErr != nil {
			t.Fatalf("filter pre-playback capture frame %d: %v", frameIndex, filterErr)
		}
		if len(released) != 1 {
			t.Fatalf("pre-playback capture frame %d released %d frames, want one", frameIndex, len(released))
		}
	}

	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		playback := feedbackSignal(frameIndex, 17)
		if err := gate.WritePlayback(context.Background(), playback, func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
		released, filterErr := gate.FilterCapture(context.Background(), playback)
		if filterErr != nil {
			t.Fatalf("filter looped capture frame %d: %v", frameIndex, filterErr)
		}
		if len(released) != 0 {
			t.Fatalf("looped capture frame %d released %d frames, want none", frameIndex, len(released))
		}
	}

	select {
	case got := <-warning:
		if !strings.Contains(got, "Acoustic feedback detected") {
			t.Fatalf("warning = %q, want acoustic-feedback diagnosis", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-playback capture lead prevented feedback confirmation")
	}
}

func TestLocalFeedbackGateReleasesIndependentCaptureOnceInOrder(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()

	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		if err := gate.WritePlayback(context.Background(), feedbackSignal(frameIndex, 23), func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
	}

	want := make([][]int16, 5)
	var got [][]int16
	for frameIndex := range want {
		want[frameIndex] = feedbackSignal(frameIndex, 71)
		released, err := gate.FilterCapture(context.Background(), want[frameIndex])
		if err != nil {
			t.Fatalf("filter independent capture frame %d: %v", frameIndex, err)
		}
		got = append(got, released...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("released independent frames = %d/%d or reordered, got %#v", len(got), len(want), got)
	}
	select {
	case extra := <-warning:
		t.Fatalf("independent capture emitted feedback warning %q", extra)
	default:
	}
}

func TestLocalFeedbackGateBlockedWarningWriterCannotBlockMedia(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	writer := blockingFeedbackWarningWriter{started: started, release: release}
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), writer, SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		close(release)
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()

	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		if err := gate.WritePlayback(context.Background(), feedbackSignal(frameIndex, 17), func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
	}
	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		if _, err := gate.FilterCapture(context.Background(), feedbackSignal(frameIndex, 17)); err != nil {
			t.Fatalf("filter looped capture frame %d: %v", frameIndex, err)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked warning writer")
	}

	mediaDone := make(chan struct{})
	mediaErr := make(chan error, 1)
	go func() {
		_, err := gate.FilterCapture(context.Background(), feedbackSignal(5, 17))
		mediaErr <- err
		close(mediaDone)
	}()
	select {
	case <-mediaDone:
		if err := <-mediaErr; err != nil {
			t.Fatalf("capture classification: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("capture classification blocked behind warning writer")
	}
}
