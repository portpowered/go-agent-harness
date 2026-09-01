package services

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// TestRTCDeviceSinkHoldToneConfirmedFeedbackStillForwardsGenuineBargeInPromptly
// is the interaction test the #357 acoustic-feedback fix specifically calls
// for: a hold-tone pulse genuinely played on the local speaker, genuinely
// looped back into the local mic (the same direct-paired virtual topology
// TestPairedDeviceBindingDropsLoopedSpeakerFramesBeforeProviderMedia uses,
// which avoids the tap-and-re-inject race hazard a separate loopback tap
// topology has -- see the loopback harness comments), and a genuine,
// uncorrelated customer utterance injected concurrently while the cue keeps
// repeating in the background.
//
// The hold-tone pulse being classified as self-heard feedback here is
// CORRECT and expected: it is real audio genuinely played on the speaker,
// so a real acoustic loop back into the mic is indistinguishable from any
// other played content, and localFeedbackGate's job is exactly to keep that
// out of what reaches the provider. What this test actually pins down is
// the coordinator's specific concern: because recovery out of `suppressing`
// requires AnalysisWindow of sustained NonFeedback evidence, a *periodic*
// tone that keeps re-arming suppression every PulseInterval could in
// principle keep re-classifying a genuine, ongoing barge-in as ambiguous
// and delay it far beyond one AnalysisWindow. This test drives real audio
// through the real RTCDeviceSink -> localFeedbackGate -> RTCDeviceSource
// pipeline (not a synthetic gate-only harness) across several pulse/gap
// cycles and asserts every barge-in frame still reaches provider-bound
// media within a small, bounded latency.
func TestRTCDeviceSinkHoldToneConfirmedFeedbackStillForwardsGenuineBargeInPromptly(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "mic", Name: "Virtual Mic", Direction: audio.DirectionInput, LoopbackID: "speaker"},
			{ID: "speaker", Name: "Virtual Speaker", Direction: audio.DirectionOutput, LoopbackID: "mic"},
		},
		Defaults: map[audio.Direction]string{
			audio.DirectionInput:  "mic",
			audio.DirectionOutput: "speaker",
		},
	})
	if err != nil {
		t.Fatalf("new virtual feedback registry: %v", err)
	}

	warning := make(chan string, 1)
	feedbackConfig := audio.DefaultSelfHearingConfig()
	feedbackConfig.CorrelationLagWindow = audio.PCM16LagWindow{Min: -5 * time.Millisecond, Max: 5 * time.Millisecond}
	feedbackConfig.MinimumEvidence = 30 * time.Millisecond
	feedbackConfig.MaximumReleaseLatency = 500 * time.Millisecond
	// AnalysisWindow (120ms) and PostPlaybackAcousticTail (200ms) are left at
	// their production defaults deliberately: this test needs the real
	// "how long does recovery take" bar the coordinator is asking about, not
	// a shortened one that would hide a real regression.

	binding, err := PrepareRTCDeviceBindings(RTCDeviceBindingRequest{
		Registry:              registry,
		InputPresent:          true,
		OutputPresent:         true,
		SelfHearingConfig:     feedbackConfig,
		FeedbackWarningWriter: feedbackWarningChannel(warning),
	})
	if err != nil {
		t.Fatalf("prepare paired binding: %v", err)
	}
	if binding == nil || binding.Source == nil || binding.Sink == nil {
		t.Fatalf("binding = %#v, want paired source and sink", binding)
	}
	defer func() { _ = binding.Close() }()

	// A short gap threshold and a pulse duration comfortably above
	// MinimumEvidence so a single pulse alone gives the gate enough
	// sustained correlated evidence to confirm feedback -- this is the
	// scenario under test, not an edge case being avoided.
	binding.Sink.holdToneConfig = audio.HoldToneConfig{
		GapThreshold:  20 * time.Millisecond,
		PulseInterval: 150 * time.Millisecond,
		PulseDuration: 100 * time.Millisecond,
		Amplitude:     8000,
		ToneHz1:       440,
		ToneHz2:       660,
	}
	binding.Sink.holdToneTick = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbound := newFeedbackInbound(2)
	outbound := &feedbackOutbound{frames: make(chan rtc.PCMFrame, 256)}
	sinkDone := make(chan error, 1)
	sourceDone := make(chan error, 1)
	go func() { sinkDone <- binding.Sink.Pump(ctx, inbound) }()
	go func() { sourceDone <- binding.Source.Pump(ctx, outbound) }()

	// Anchor the gap clock with one real frame, then go silent (never close
	// inbound) so the hold tone takes over -- the exact customer-facing gap
	// this cue exists to fill.
	inbound.push(make([]int16, audio.FrameSize))

	select {
	case got := <-warning:
		if !strings.Contains(got, "Acoustic feedback detected") {
			t.Fatalf("warning = %q, want acoustic-feedback diagnosis", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a hold-tone pulse coupled back through the mic was never confirmed as self-heard feedback; this is a test-setup problem, not the behavior under test")
	}

	// The customer now genuinely interrupts, continuously, for a window
	// that spans several pulse/gap cycles (150ms * ~6 = 900ms), so the
	// injected speech is guaranteed to overlap both an actively-playing
	// pulse and a silent gap at various points -- exactly the condition
	// that could let a periodic tone repeatedly re-arm suppression.
	//
	// Each frame is written directly to the shared "speaker" device (a
	// second, independent Sink handle, exactly like
	// TestPairedDeviceBindingDropsLoopedSpeakerFramesBeforeProviderMedia
	// uses) rather than through binding.Sink.Pump, so it never passes
	// through localFeedbackGate.WritePlayback: from the gate's perspective
	// this is indistinguishable from a real, independent customer
	// utterance arriving at the mic while the assistant's own cue is still
	// playing.
	userFeed, err := audio.NewDeviceSink(registry, binding.Sink.DeviceID())
	if err != nil {
		t.Fatalf("open independent virtual user feeder: %v", err)
	}
	defer func() { _ = userFeed.Close() }()

	const bargeInFrames = 45
	const bargeInSpacing = 20 * time.Millisecond
	wantFrames := make([][]int16, bargeInFrames)
	sentAt := make([]time.Time, bargeInFrames)
	for i := 0; i < bargeInFrames; i++ {
		wantFrames[i] = feedbackSignal(i, 97)
		sentAt[i] = time.Now()
		if err := userFeed.WriteFrame(ctx, wantFrames[i]); err != nil {
			t.Fatalf("feed independent virtual user frame %d: %v", i, err)
		}
		time.Sleep(bargeInSpacing)
	}

	// Every one of those frames must still reach provider-bound media,
	// promptly, while the hold tone is still actively cycling in the
	// background (it is not stopped or discarded before this assertion).
	// The gate releases a batch of held-but-independent frames together
	// once AnalysisWindow of sustained NonFeedback evidence accumulates
	// (see classifySuppressedCaptureLocked), so the bound below is a real
	// "promptly" bound, not a per-frame one: comfortably above the
	// observed ~1s batch-release latency for this test's config, but far
	// below "delayed indefinitely by a periodic re-arming pulse" -- which
	// is what this test failed with before holdToneFeedbackConfirmed
	// existed (only 11/45 frames ever arrived, even after 20s).
	const bargeInBound = 3 * time.Second
	seen := make(map[int]bool, bargeInFrames)
	var maxLatency time.Duration
	deadline := time.Now().Add(bargeInBound)
	for len(seen) < bargeInFrames && time.Now().Before(deadline) {
		select {
		case frame := <-outbound.frames:
			for i, want := range wantFrames {
				if !seen[i] && reflect.DeepEqual(frame.Samples, want) {
					if latency := time.Since(sentAt[i]); latency > maxLatency {
						maxLatency = latency
					}
					seen[i] = true
					break
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if len(seen) != bargeInFrames {
		t.Fatalf("only %d/%d genuine barge-in frames reached provider media within %v; a periodic hold-tone pulse must not delay a real interruption this long", len(seen), bargeInFrames, bargeInBound)
	}
	t.Logf("max per-frame barge-in latency while the hold tone kept cycling: %v", maxLatency)

	cancel()
	select {
	case <-sinkDone:
	case <-time.After(time.Second):
		t.Fatal("speaker pump did not stop after cancellation")
	}
	select {
	case <-sourceDone:
	case <-time.After(time.Second):
		t.Fatal("microphone pump did not stop after cancellation")
	}
}
