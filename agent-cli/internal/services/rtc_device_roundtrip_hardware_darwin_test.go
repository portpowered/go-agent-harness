//go:build darwin && cgo && !nomicrophone

package services_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

// TestRTCDeviceBindingHardwareRoundTrip is an opt-in Darwin smoke variant for
// operators with real audio endpoints. It is intentionally separate from the
// hermetic virtual proof because it sends captured microphone audio to the
// selected speaker. Missing endpoints, permissions, and silent capture all
// produce an actionable skip using the existing s2s hardware-test convention.
func TestRTCDeviceBindingHardwareRoundTrip(t *testing.T) {
	if os.Getenv("AGENT_HARNESS_RTC_HARDWARE") != "1" {
		t.Skip("Darwin RTC hardware roundtrip is opt-in; set AGENT_HARNESS_RTC_HARDWARE=1 to enable microphone-to-speaker playback")
	}

	registry := audio.NewCoreAudioDeviceRegistry()
	if _, err := registry.List(); err != nil {
		t.Skipf("darwin: CoreAudio enumeration unavailable: %v", err)
	}
	input, err := registry.Default(audio.DirectionInput)
	if err != nil {
		t.Skipf("darwin: no usable default input device: %v", err)
	}
	output, err := registry.Default(audio.DirectionOutput)
	if err != nil {
		t.Skipf("darwin: no usable default output device: %v", err)
	}

	binding, err := services.PrepareRTCDeviceBindings(services.RTCDeviceBindingRequest{
		Registry:      registry,
		InputDevice:   input.ID,
		OutputDevice:  output.ID,
		InputPresent:  true,
		OutputPresent: true,
	})
	if err != nil {
		t.Skipf("darwin: physical RTC endpoints cannot be opened together: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peer := newLoopbackRTCTrackPeer(rtcRoundtripFrameCount)
	sourceDone := make(chan error, 1)
	sinkDone := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		_ = binding.Close()
		_ = peer.Close()
	})
	go func() { sourceDone <- binding.Source.Pump(ctx, peer) }()
	go func() { sinkDone <- binding.Sink.Pump(ctx, peer) }()

	sourceErr := <-sourceDone
	sinkErr := <-sinkDone
	for name, runErr := range map[string]error{"source": sourceErr, "sink": sinkErr} {
		if runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) && !errors.Is(runErr, context.Canceled) {
			t.Fatalf("darwin: %s pump failed: %v", name, runErr)
		}
	}
	stats := peer.Stats()
	if stats.Writes == 0 || stats.Reads == 0 {
		t.Skipf("darwin: physical RTC path emitted no complete frames (writes=%d reads=%d)", stats.Writes, stats.Reads)
	}
	if stats.Energy == 0 {
		t.Skipf("darwin: physical capture produced no positive PCM energy across %d frames", stats.Writes)
	}
	t.Logf("darwin RTC hardware roundtrip: input=%q output=%q frames=%d energy=%d", input.ID, output.ID, stats.Reads, stats.Energy)
}
