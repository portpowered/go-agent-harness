package runtime

import (
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// TestRTCDeviceSinkResetsResamplerAtResponseBoundary keeps response tails
// independent while a continuation is already queued in SessionMedia. A
// response.done marker must reset the converter before the next response;
// otherwise its first samples inherit the previous response's filter history.
func TestRTCDeviceSinkResetsResamplerAtResponseBoundary(t *testing.T) {
	registry := newRTCDeviceSinkRateRegistry(t, audio.SampleRate)
	sink, err := NewRTCDeviceSinkAtRate(registry, "virtual:output", wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("open sink: %v", err)
	}
	defer func() { _ = sink.Close() }()
	sink.holdToneConfig.GapThreshold = time.Hour

	first := boundaryTestPCM(1800, 900)
	second := boundaryTestPCM(1800, 3100)
	firstResponse := audio.PlaybackResponse{ResponseID: "response-1", ItemID: "item-1"}
	secondResponse := audio.PlaybackResponse{ResponseID: "response-2", ItemID: "item-2"}
	media := audio.NewSessionMediaAtRate(nil, wavio.Rate24kHz)
	defer func() { _ = media.Close() }()
	media.StartInboundResponse(firstResponse)
	if err := media.PushInbound(first); err != nil {
		t.Fatalf("push first response: %v", err)
	}
	if err := media.FlushInbound(); err != nil {
		t.Fatalf("flush first response: %v", err)
	}
	media.StartInboundResponse(secondResponse)
	if err := media.PushInbound(second); err != nil {
		t.Fatalf("push second response: %v", err)
	}
	if err := media.FlushInbound(); err != nil {
		t.Fatalf("flush second response: %v", err)
	}
	media.FailInbound(io.EOF)

	var got []int16
	sink.SetPlaybackSamplesObserver(func(_ context.Context, _ int, samples []int16) error {
		got = append(got, samples...)
		return nil
	})
	if err := sink.Pump(context.Background(), media.Endpoints().Inbound); err != nil {
		t.Fatalf("pump: %v", err)
	}

	want := append(boundaryTestResample(t, first), boundaryTestResample(t, second)...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response-boundary playback differs at filter reset: got %d samples, want %d", len(got), len(want))
	}
}

func boundaryTestPCM(count int, seed int16) []int16 {
	samples := make([]int16, count)
	state := uint32(uint16(seed)) ^ 0x9e3779b9
	for index := range samples {
		state = state*1664525 + 1013904223
		samples[index] = seed + int16(state%1021)
	}
	return samples
}

func boundaryTestResample(t *testing.T, samples []int16) []int16 {
	t.Helper()
	converter, err := wavio.NewPCM16Resampler(wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := converter.Process(samples, true)
	if err != nil {
		t.Fatal(err)
	}
	return converted
}
