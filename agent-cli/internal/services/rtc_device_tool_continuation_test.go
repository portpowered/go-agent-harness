package services

import (
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// TestRTCDeviceSinkToolContinuationPreservesQueuedResponses reproduces the
// test22 failure at the provider-media -> callback-clocked speaker boundary.
// Realtime providers can deliver response B immediately after a tool result
// while several seconds of response A remain queued for physical playback.
// Merely opening B must not invalidate any unplayed samples from A.
func TestRTCDeviceSinkToolContinuationPreservesQueuedResponses(t *testing.T) {
	assertToolContinuationScenario(t, audio.SampleRate, false, []int{
		40 * audio.FrameSize,
		20 * audio.FrameSize,
	})
}

// TestRTCDeviceSinkToolContinuationFailureModes covers common variations of
// the test22 race. A response may be tiny, empty (tool-only), repeated by the
// provider, end on a partial frame, or arrive at the 24 kHz model rate while
// the physical device remains 16 kHz. None may replace older queued speech.
func TestRTCDeviceSinkToolContinuationFailureModes(t *testing.T) {
	tests := []struct {
		name          string
		providerRate  int
		duplicateOpen bool
		responseSizes []int
	}{
		{name: "rapid chained responses", providerRate: audio.SampleRate, responseSizes: []int{40 * 480, 480, 2 * 480, 3 * 480}},
		{name: "empty tool-only response", providerRate: audio.SampleRate, responseSizes: []int{40 * 480, 0, 20 * 480}},
		{name: "duplicate response start", providerRate: audio.SampleRate, duplicateOpen: true, responseSizes: []int{40 * 480, 20 * 480}},
		{name: "partial response tails", providerRate: audio.SampleRate, responseSizes: []int{40*480 + 137, 5*480 + 91}},
		{name: "24k model to 16k device", providerRate: wavio.Rate24kHz, responseSizes: []int{40 * 720, 20 * 720}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertToolContinuationScenario(t, test.providerRate, test.duplicateOpen, test.responseSizes)
		})
	}
}

func assertToolContinuationScenario(t *testing.T, providerRate int, duplicateOpen bool, responseSizes []int) {
	t.Helper()
	registry := newToolContinuationDevice(t, audio.SampleRate)
	sink, err := NewRTCDeviceSinkAtRate(registry, "", providerRate)
	if err != nil {
		t.Fatalf("open callback-clocked sink: %v", err)
	}
	defer func() { _ = sink.Close() }()
	// This test controls time through the simulated device callbacks. Keep the
	// wall-clock hold cue out of the deterministic PCM assertion.
	sink.holdToneConfig.GapThreshold = time.Hour

	media := rtc.NewSessionMediaAtRate(nil, providerRate)
	defer func() { _ = media.Close() }()
	responses := make([]rtc.PlaybackResponse, len(responseSizes))
	pcm := make([][]int16, len(responseSizes))
	want := make([]int16, 0)
	for index, size := range responseSizes {
		responses[index] = rtc.PlaybackResponse{
			ResponseID: "tool-continuation-response-" + string(rune('a'+index)),
			ItemID:     "tool-continuation-item-" + string(rune('a'+index)),
		}
		pcm[index] = toolContinuationPCM(size, int16(1100-index*5200))
		converted, err := wavio.Resample(pcm[index], providerRate, audio.SampleRate)
		if err != nil {
			t.Fatalf("reference response %d conversion: %v", index, err)
		}
		want = append(want, converted...)
		// Adjacent provider responses are one continuous speaker timeline. A
		// response/tool boundary must not force the native queue to empty or
		// invent a silent callback between the two PCM spans.
	}
	if len(responses) < 2 {
		t.Fatal("tool-continuation scenario requires at least two responses")
	}

	media.StartInboundResponse(responses[0])
	if err := media.PushInbound(pcm[0]); err != nil {
		t.Fatalf("queue pre-tool response: %v", err)
	}
	if err := media.FlushInbound(); err != nil {
		t.Fatalf("finish pre-tool response: %v", err)
	}

	pumpErr := make(chan error, 1)
	go func() { pumpErr <- sink.Pump(context.Background(), media.Endpoints().Inbound) }()
	waitForToolContinuationBacklog(t, sink)

	// This is the tool-result continuation race: the provider opens and fills
	// the next response before the callback-clocked device has drained the first.
	for index := 1; index < len(responses); index++ {
		media.StartInboundResponse(responses[index])
		if duplicateOpen {
			media.StartInboundResponse(responses[index])
		}
		if err := media.PushInbound(pcm[index]); err != nil {
			t.Fatalf("queue continuation response %d: %v", index, err)
		}
		if err := media.FlushInbound(); err != nil {
			t.Fatalf("finish continuation response %d: %v", index, err)
		}
	}
	media.FailInbound(io.EOF)

	drainToolContinuationPlayback(t, registry, sink, pumpErr)
	if got := trimToolContinuationEdgeUnderflow(registry.RenderedSamples()); !reflect.DeepEqual(got, want) {
		t.Fatalf("tool continuation rendered %d/%d samples; response handoff lost %d samples", len(got), len(want), len(want)-len(got))
	}
}

func newToolContinuationDevice(t *testing.T, sampleRate int) *audio.SimulatedDuplexRegistry {
	t.Helper()
	registry, err := audio.NewSimulatedDuplexRegistry(audio.DuplexScenario{
		Seed:    2201,
		Render:  audio.ClockSpec{NominalRate: sampleRate, Quanta: []int{sampleRate * 30 / 1000}},
		Capture: audio.ClockSpec{NominalRate: sampleRate, Quanta: []int{sampleRate * 30 / 1000}},
	})
	if err != nil {
		t.Fatalf("new callback-clocked device: %v", err)
	}
	return registry
}

func waitForToolContinuationBacklog(t *testing.T, sink *RTCDeviceSink) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := sink.PlaybackStats()
		highWaterSamples := sink.DeviceSampleRate() * int(audio.DefaultPlaybackHighWatermark/time.Millisecond) / 1000
		if stats.QueuedSamples >= highWaterSamples && highWaterSamples > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("provider pump did not fill playback backlog: %+v", sink.PlaybackStats())
}

func drainToolContinuationPlayback(t *testing.T, registry *audio.SimulatedDuplexRegistry, sink *RTCDeviceSink, pumpErr <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-pumpErr:
			if err != nil {
				t.Fatalf("pump tool-continuation playback: %v", err)
			}
			return
		default:
		}
		if sink.PlaybackStats().QueuedSamples > 0 {
			if err := registry.Advance(1); err != nil {
				t.Fatalf("advance callback-clocked playback: %v", err)
			}
			// Give the paced producer a scheduling point to refill the queue just
			// as a real 30 ms callback period would. Advancing simulated callbacks
			// back-to-back would manufacture device underflow unrelated to the
			// response-boundary bug under test.
			time.Sleep(100 * time.Microsecond)
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatalf("tool-continuation playback did not drain: %+v", sink.PlaybackStats())
}

func trimToolContinuationEdgeUnderflow(samples []int16) []int16 {
	for len(samples) > 0 && samples[0] == 0 {
		samples = samples[1:]
	}
	for len(samples) > 0 && samples[len(samples)-1] == 0 {
		samples = samples[:len(samples)-1]
	}
	return samples
}

func toolContinuationPCM(samples int, seed int16) []int16 {
	pcm := make([]int16, samples)
	for index := range pcm {
		pcm[index] = seed + int16(index%251)
	}
	return pcm
}
