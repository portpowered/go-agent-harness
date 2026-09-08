package audio

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
)

func speechLikeStream(totalFrames int) [][]int16 {
	totalSamples := totalFrames * FrameSize
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
	wordSamples := int(0.24 * float64(SampleRate))
	pauseSamples := int(0.09 * float64(SampleRate))
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
		frame := make([]int16, FrameSize)
		for i := 0; i < FrameSize; i++ {
			frame[i] = int16(full[f*FrameSize+i])
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
type roomEchoTap struct {
	lag  int
	gain float64
}

func roomCoupledCopy(playback [][]int16, delaySamples int, gain float64, noiseAmplitude int32) [][]int16 {
	if len(playback) == 0 {
		return nil
	}
	frameLen := len(playback[0])
	flat := flattenPlayback(playback, frameLen)
	taps := []roomEchoTap{
		{lag: delaySamples, gain: gain},
		{lag: delaySamples + 80, gain: gain * 0.45},
		{lag: delaySamples + 160, gain: gain * 0.25},
	}
	state := uint32(21771)
	out := make([][]int16, len(playback))
	for frameIndex := range out {
		frame := make([]int16, frameLen)
		for sampleIndex := range frame {
			index := frameIndex*frameLen + sampleIndex
			frame[sampleIndex] = roomCoupledSample(flat, index, taps, &state, noiseAmplitude)
		}
		out[frameIndex] = frame
	}
	return out
}

func flattenPlayback(playback [][]int16, frameLen int) []float64 {
	flat := make([]float64, len(playback)*frameLen)
	for frameIndex, frame := range playback {
		for sampleIndex, sample := range frame {
			flat[frameIndex*frameLen+sampleIndex] = float64(sample)
		}
	}
	return flat
}

func roomCoupledSample(flat []float64, index int, taps []roomEchoTap, state *uint32, noiseAmplitude int32) int16 {
	value := 0.0
	for _, tap := range taps {
		source := index - tap.lag
		if source >= 0 && source < len(flat) {
			value += flat[source] * tap.gain
		}
	}
	*state = *state*1664525 + 1013904223
	noise := int32(*state>>16)%(2*noiseAmplitude+1) - noiseAmplitude
	value += float64(noise)
	if value > 32767 {
		value = 32767
	}
	if value < -32768 {
		value = -32768
	}
	return int16(value)
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
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()

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
		t.Fatalf("%d contaminated frame(s) reached the provider-bound path after feedback was confirmed at frame %d (state=%q lag=%s)", len(leakedAfterConfirm), confirmedAt, gate.State(), gate.ConfirmedLag())
	}
}

// TestLocalFeedbackGateDropsTest42ShortEchoTransient reproduces the false
// server-VAD barge-in from test42.json. After this paired device has already
// established real speaker-to-microphone coupling, a later response leaks a
// short, inverted onset through the acoustic path and then returns to silence.
// The onset is shorter than MinimumEvidence, so it can never be called user
// speech merely because the bounded startup hold expires. Forwarding it is
// enough for provider VAD to cancel several seconds of queued assistant audio.
func TestLocalFeedbackGateDropsTest42ShortEchoTransient(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	gate, err := NewPCM16FeedbackGate(config, nil, SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()

	// Establish the physical loop once, as the earlier assistant turns did in
	// test42. The next response is separated by enough quiet capture to force
	// the normal response re-anchor path.
	first := speechLikeStream(8)
	writeAssistantResponse(t, gate, first)
	filterAssistantEcho(t, gate, first, nil)
	if !gate.FeedbackConfirmed() {
		t.Fatal("initial response did not establish speaker-to-microphone coupling")
	}
	frameDuration := feedbackDeviceDurationAtRate(FrameSize, SampleRate)
	quietFrames := int((config.PostPlaybackAcousticTail+config.CorrelationLagWindow.Max)/frameDuration) + 3
	for frame := 0; frame < quietFrames; frame++ {
		if _, filterErr := gate.FilterCapture(context.Background(), make([]int16, FrameSize)); filterErr != nil {
			t.Fatalf("advance quiet capture frame %d: %v", frame, filterErr)
		}
	}

	second := speechLikeStream(10)
	writeAssistantResponse(t, gate, second)
	shortFrames := int(config.MinimumEvidence/frameDuration) - 1
	if shortFrames < 1 {
		t.Fatal("test requires a sub-MinimumEvidence device-frame burst")
	}
	submitted := collectInvertedEcho(t, gate, second, shortFrames)
	// Continue the microphone clock beyond the normal release bound. This is
	// where the old gate forwarded the ambiguous onset and triggered server VAD.
	submitted = append(submitted, collectSilence(t, gate, 12)...)
	assertSilentSubmission(t, submitted)
}

func collectInvertedEcho(t *testing.T, gate *PCM16FeedbackGate, frames [][]int16, count int) [][]int16 {
	t.Helper()
	var submitted [][]int16
	for frame := 0; frame < count; frame++ {
		inverted := make([]int16, len(frames[frame]))
		for sample, value := range frames[frame] {
			inverted[sample] = -value / 2
		}
		released, err := gate.FilterCapture(context.Background(), inverted)
		if err != nil {
			t.Fatalf("filter short echo frame %d: %v", frame, err)
		}
		submitted = append(submitted, released...)
	}
	return submitted
}

func collectSilence(t *testing.T, gate *PCM16FeedbackGate, count int) [][]int16 {
	t.Helper()
	var submitted [][]int16
	for frame := 0; frame < count; frame++ {
		released, err := gate.FilterCapture(context.Background(), make([]int16, FrameSize))
		if err != nil {
			t.Fatalf("filter post-transient silence %d: %v", frame, err)
		}
		submitted = append(submitted, released...)
	}
	return submitted
}

func assertSilentSubmission(t *testing.T, submitted [][]int16) {
	t.Helper()
	for frameIndex, frame := range submitted {
		for _, sample := range frame {
			if sample != 0 {
				t.Fatalf("test42 echo transient reached provider in submitted frame %d", frameIndex)
			}
		}
	}
}

// TestLocalFeedbackGateReleasesSustainedIndependentSpeechAfterPlaybackEnds
// proves the gate does not over-suppress: once the assistant has stopped
// talking (no further WritePlayback calls) but the acoustic tail is still
// open, genuinely independent captured audio must still reach the provider,
// not be trapped in suppression forever.
func TestLocalFeedbackGateReleasesSustainedIndependentSpeechAfterPlaybackEnds(t *testing.T) {
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
		t.Fatalf("no independent speech was released after playback ended; gate over-suppressed (state=%q lag=%s)", gate.State(), gate.ConfirmedLag())
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
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()

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
		t.Fatalf("no barge-in audio reached the provider-bound path while playback was still active (state=%q)", gate.State())
	}
}

// TestLocalFeedbackGateRetargetsTest14AcrossAssistantResponses reproduces the
// multi-response device timeline that terminated test14.json. The first assistant response
// is heard back at essentially zero lag and narrows the post-confirmation
// probe. After that response and its acoustic tail expire, a later assistant
// response is heard back at a different physical lag. That second response is
// new assistant output, not customer barge-in audio: it must be suppressed and
// must not terminate the microphone pump. A subsequent independent customer
// signal is the barge-in and must be released once, with a bounded amount of
// provider-bound audio.
func TestLocalFeedbackGateRetargetsTest14AcrossAssistantResponses(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	config.PostPlaybackAcousticTail = 120 * time.Millisecond
	gate, err := NewPCM16FeedbackGate(config, nil, SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()

	const responseFrames = 12
	firstAssistant := speechLikeStream(responseFrames)
	writeAssistantResponse(t, gate, firstAssistant)
	filterAssistantEcho(t, gate, firstAssistant, nil)
	if gate.State() != "suppressing" {
		t.Fatalf("first assistant response state = %q, want suppression after self-hearing confirmation", gate.State())
	}
	firstLag := gate.ConfirmedLag()

	// Advance capture beyond the prior response, configured acoustic tail, and
	// maximum correlation horizon. This is the ordinary quiet gap between two
	// assistant responses in test14, not a customer turn.
	frameDuration := feedbackDeviceDurationAtRate(FrameSize, SampleRate)
	quietFrames := int((config.PostPlaybackAcousticTail+config.CorrelationLagWindow.Max)/frameDuration) + 3
	advanceQuietCapture(t, gate, quietFrames)

	secondAssistant := speechLikeStream(responseFrames)
	writeAssistantResponse(t, gate, secondAssistant)
	const secondLagFrames = 4
	advanceQuietCapture(t, gate, secondLagFrames)
	filterAssistantEcho(t, gate, secondAssistant, nil)
	if gate.ConfirmedLag() == firstLag {
		t.Fatalf("second assistant response retained first lag %s; want a newly learned lag", firstLag)
	}

	// Independent microphone PCM is the customer barge-in. It may be held for
	// the analysis window, but it must eventually be forwarded in order and may
	// never expand into more provider-bound audio than was captured.
	const customerFrames = 20
	customer := feedbackPlayback(customerFrames, 997)
	submitted := collectBargeIn(t, gate, customer)
	assertBargeInBounds(t, customer, submitted)
	assertBargeInOrder(t, customer, submitted)
	t.Logf("provider-bound customer audio: frames=%d/%d samples=%d/%d; assistant echo frames=0", len(submitted), customerFrames, len(submitted)*FrameSize, customerFrames*FrameSize)
}

func advanceQuietCapture(t *testing.T, gate *PCM16FeedbackGate, frames int) {
	t.Helper()
	for frame := 0; frame < frames; frame++ {
		if _, err := gate.FilterCapture(context.Background(), make([]int16, FrameSize)); err != nil {
			t.Fatalf("advance quiet capture frame %d: %v", frame, err)
		}
	}
}

func collectBargeIn(t *testing.T, gate *PCM16FeedbackGate, customer [][]int16) [][]int16 {
	t.Helper()
	var submitted [][]int16
	for frame, samples := range customer {
		released, err := gate.FilterCapture(context.Background(), samples)
		if err != nil {
			t.Fatalf("filter customer barge-in frame %d: %v", frame, err)
		}
		submitted = append(submitted, released...)
	}
	return submitted
}

func assertBargeInBounds(t *testing.T, customer, submitted [][]int16) {
	t.Helper()
	if len(submitted) == 0 {
		t.Fatal("customer barge-in was lost: no independent microphone audio reached the provider")
	}
	if len(submitted) > len(customer) {
		t.Fatalf("customer barge-in submitted %d frames from %d captured frames", len(submitted), len(customer))
	}
	if submittedSamples := len(submitted) * FrameSize; submittedSamples > len(customer)*FrameSize {
		t.Fatalf("customer barge-in submitted %d samples, want at most %d", submittedSamples, len(customer)*FrameSize)
	}
}

func assertBargeInOrder(t *testing.T, customer, submitted [][]int16) {
	t.Helper()
	customerIndex := 0
	for submittedIndex := range submitted {
		for customerIndex < len(customer) && !equalSamples(submitted[submittedIndex], customer[customerIndex]) {
			customerIndex++
		}
		if customerIndex == len(customer) {
			t.Fatalf("provider-bound frame %d is duplicated, reordered, or assistant audio", submittedIndex)
		}
		customerIndex++
	}
}

func writeAssistantResponse(t *testing.T, gate *PCM16FeedbackGate, frames [][]int16) {
	t.Helper()
	for frame := range frames {
		if err := gate.WritePlayback(context.Background(), frames[frame], func() error { return nil }); err != nil {
			t.Fatalf("write assistant response frame %d: %v", frame, err)
		}
	}
}

func filterAssistantEcho(t *testing.T, gate *PCM16FeedbackGate, frames [][]int16, submitted *[][]int16) {
	t.Helper()
	for frame := range frames {
		released, err := gate.FilterCapture(context.Background(), frames[frame])
		if err != nil {
			t.Fatalf("filter assistant echo frame %d: %v", frame, err)
		}
		if submitted != nil {
			*submitted = append(*submitted, released...)
		} else if len(released) != 0 {
			t.Fatalf("assistant echo frame %d submitted %d frame(s) as customer audio", frame, len(released))
		}
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

func feedbackDeviceDurationAtRate(samples, rate int) time.Duration {
	if samples <= 0 || rate <= 0 {
		return 0
	}
	return time.Duration((int64(samples)*int64(time.Second) + int64(rate)/2) / int64(rate))
}

func feedbackSignal(frameIndex, seed int) []int16 {
	samples := make([]int16, FrameSize)
	state := uint32(seed*7919 + frameIndex*104729 + 1)
	for index := range samples {
		state = state*1664525 + 1013904223
		samples[index] = int16(int32(state>>16)%24000 - 12000) //nolint:gosec // bounded deterministic PCM fixture
	}
	return samples
}

type feedbackChannelWriter struct {
	warning chan<- string
}

func feedbackWarningChannel(warning chan<- string) *feedbackChannelWriter {
	return &feedbackChannelWriter{warning: warning}
}

func (w *feedbackChannelWriter) Write(data []byte) (int, error) {
	w.warning <- string(data)
	return len(data), nil
}

type blockingFeedbackWarningWriter struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (w blockingFeedbackWarningWriter) Write(data []byte) (int, error) {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.release
	return len(data), nil
}
