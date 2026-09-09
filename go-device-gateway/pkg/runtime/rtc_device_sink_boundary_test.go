package runtime

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
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
	defer func() {
		if err := sink.Close(); err != nil {
			t.Errorf("close sink: %v", err)
		}
	}()
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

// TestRTCDeviceSinkPreservesPrefetchedContinuationFrames keeps an earlier
// response audible when the media bridge has already opened a faster tool
// continuation. The latest response identity is not a cancellation boundary;
// only an accepted interruption may invalidate frames already in the device
// queue.
func TestRTCDeviceSinkPreservesPrefetchedContinuationFrames(t *testing.T) {
	registry := newRTCDeviceSinkRateRegistry(t, audio.SampleRate)
	sink, err := NewRTCDeviceSinkAtRate(registry, "virtual:output", wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("open sink: %v", err)
	}
	defer func() {
		if err := sink.Close(); err != nil {
			t.Errorf("close sink: %v", err)
		}
	}()
	sink.holdToneConfig.GapThreshold = time.Hour

	first := boundaryTestPCM(1440, 900)
	second := boundaryTestPCM(1440, 3100)
	firstResponse := audio.PlaybackResponse{ResponseID: "response-1", ItemID: "item-1"}
	secondResponse := audio.PlaybackResponse{ResponseID: "response-2", ItemID: "item-2"}
	inbound := &prefetchedRTCInboundMedia{
		responses: [2]audio.PlaybackResponse{firstResponse, secondResponse},
		frames: []audio.PCMFrame{
			{Samples: first[:720], PlaybackResponse: firstResponse},
			{Samples: first[720:], EndOfResponse: true, PlaybackResponse: firstResponse},
			{Samples: second[:720], PlaybackResponse: secondResponse},
			{Samples: second[720:], EndOfResponse: true, PlaybackResponse: secondResponse},
		},
	}

	var got []int16
	sink.SetPlaybackSamplesObserver(func(_ context.Context, _ int, samples []int16) error {
		got = append(got, samples...)
		return nil
	})
	if err := sink.Pump(context.Background(), inbound); err != nil {
		t.Fatalf("pump prefetched continuation: %v", err)
	}

	want := append(boundaryTestResample(t, first), boundaryTestResample(t, second)...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefetched continuation playback differs: got %d samples, want %d", len(got), len(want))
	}
}

type prefetchedRTCInboundMedia struct {
	responses [2]audio.PlaybackResponse
	frames    []audio.PCMFrame
	index     int
}

func (m *prefetchedRTCInboundMedia) SetPlaybackController(controller audio.PlaybackController) {
	if controller != nil {
		controller.StartPlayback(m.responses[0])
		controller.StartPlayback(m.responses[1])
	}
}

func (m *prefetchedRTCInboundMedia) ReadFrame(context.Context) (audio.PCMFrame, error) {
	if m.index >= len(m.frames) {
		return audio.PCMFrame{}, io.EOF
	}
	frame := m.frames[m.index]
	m.index++
	return frame, nil
}

func (*prefetchedRTCInboundMedia) Close() error { return nil }

var _ audio.PlaybackControlledInbound = (*prefetchedRTCInboundMedia)(nil)

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

func TestC21ConsumptionBoundary(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 16000, 5)
	sub, err := sink.SubscribePlaybackObservations(64)
	if err != nil {
		t.Fatalf("subscribe playback observations: %v", err)
	}
	firstResponse := audio.PlaybackResponse{ResponseID: "c21-response-1", ItemID: "c21-item-1", ContentIndex: 0}
	secondResponse := audio.PlaybackResponse{ResponseID: "c21-response-2", ItemID: "c21-item-2", ContentIndex: 0}
	first := []int16{101, 102, 103}
	second := []int16{201, 202, 203}
	sink.StartPlayback(firstResponse)
	if err := sink.WritePlayback(context.Background(), first); err != nil {
		t.Fatalf("write first response: %v", err)
	}
	sink.StartPlayback(secondResponse)
	if err := sink.WritePlayback(context.Background(), second); err != nil {
		t.Fatalf("write second response: %v", err)
	}
	if got := sink.PlaybackObservationStats().DeviceSamples; got != 0 {
		t.Fatalf("paused callback device samples = %d, want zero", got)
	}
	for _, want := range []audio.PlaybackResponse{firstResponse, secondResponse} {
		admission := c21NextObservation(t, sub)
		if admission.Kind != RTCDevicePlaybackAdmission || admission.PlaybackResponse != want || !admission.Accepted || admission.Consumed {
			t.Fatalf("admission = %+v, want accepted non-consumed response %q", admission, want.ItemID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err = sub.Next(ctx)
	cancel()
	if err != context.DeadlineExceeded {
		t.Fatalf("observation before callback error = %v, want deadline", err)
	}
	if err := registry.Advance(1); err != nil {
		t.Fatalf("advance first callback: %v", err)
	}
	firstConsumed := c21NextObservation(t, sub)
	assertC21Consumed(t, firstConsumed, firstResponse, 16000, 0, first)
	secondPrefix := c21NextObservation(t, sub)
	assertC21Consumed(t, secondPrefix, secondResponse, 16000, 3, second[:2])
	if err := registry.Advance(1); err != nil {
		t.Fatalf("advance second callback: %v", err)
	}
	secondTail := c21NextObservation(t, sub)
	assertC21Consumed(t, secondTail, secondResponse, 16000, 5, second[2:])
	underflow := c21NextObservation(t, sub)
	if underflow.Kind != RTCDevicePlaybackUnderflow || underflow.ResponseID != "" || underflow.SampleRate != 16000 || underflow.StartSample != 6 || underflow.EndSample != 10 || underflow.SampleCount != 4 || !underflow.Consumed || underflow.Precise || !reflect.DeepEqual(underflow.Samples, []int16{0, 0, 0, 0}) {
		t.Fatalf("underflow = %+v, want four zero-filled device samples", underflow)
	}
	holdTone := []int16{7, 8}
	if err := sink.WritePlaybackHoldTone(context.Background(), holdTone); err != nil {
		t.Fatalf("write hold tone: %v", err)
	}
	holdAdmission := c21NextObservation(t, sub)
	if holdAdmission.Kind != RTCDevicePlaybackAdmission || holdAdmission.ContentKind != RTCDevicePlaybackHoldTone || holdAdmission.ResponseID != "" || holdAdmission.Precise {
		t.Fatalf("hold-tone admission = %+v", holdAdmission)
	}
	if err := registry.Advance(1); err != nil {
		t.Fatalf("advance hold-tone callback: %v", err)
	}
	holdConsumed := c21NextObservation(t, sub)
	if holdConsumed.Kind != RTCDevicePlaybackHoldTone || holdConsumed.ContentKind != RTCDevicePlaybackHoldTone || holdConsumed.StartSample != 10 || holdConsumed.EndSample != 12 || holdConsumed.SampleCount != 2 || !holdConsumed.Consumed || holdConsumed.Precise || !reflect.DeepEqual(holdConsumed.Samples, holdTone) {
		t.Fatalf("hold-tone observation = %+v", holdConsumed)
	}
	trailingUnderflow := c21NextObservation(t, sub)
	if trailingUnderflow.Kind != RTCDevicePlaybackUnderflow || trailingUnderflow.StartSample != 12 || trailingUnderflow.EndSample != 15 || trailingUnderflow.SampleCount != 3 {
		t.Fatalf("trailing underflow = %+v", trailingUnderflow)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close sink: %v", err)
	}
	if _, err := sub.Next(context.Background()); err != io.EOF {
		t.Fatalf("closed observation stream error = %v, want EOF", err)
	}
}

func TestC21ConsumptionInterruptionAndRate(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 24000, 2000)
	defer func() { _ = sink.Close() }()
	sub, err := sink.SubscribePlaybackObservations(32)
	if err != nil {
		t.Fatalf("subscribe playback observations: %v", err)
	}
	firstResponse := audio.PlaybackResponse{ResponseID: "c21-interrupt-1", ItemID: "c21-interrupt-item-1"}
	secondResponse := audio.PlaybackResponse{ResponseID: "c21-interrupt-2", ItemID: "c21-interrupt-item-2"}
	first := boundaryTestPCM(2000, 301)
	second := boundaryTestPCM(2000, 701)
	sink.StartPlayback(firstResponse)
	if err := sink.WritePlayback(context.Background(), first); err != nil {
		t.Fatalf("write first interrupted chunk: %v", err)
	}
	if err := sink.WritePlayback(context.Background(), second); err != nil {
		t.Fatalf("write second interrupted chunk: %v", err)
	}
	_ = c21NextObservation(t, sub)
	_ = c21NextObservation(t, sub)
	if err := registry.Advance(1); err != nil {
		t.Fatalf("advance interrupted response: %v", err)
	}
	heard := c21NextObservation(t, sub)
	assertC21Consumed(t, heard, firstResponse, 24000, 0, first)
	interruption, ok := sink.InterruptActivePlayback()
	if !ok || interruption.PlaybackResponse != firstResponse || interruption.AudioEndMS != 83 {
		t.Fatalf("interruption = %+v, ok=%v, want 83ms of first response", interruption, ok)
	}
	discard := c21NextObservation(t, sub)
	if discard.Kind != RTCDevicePlaybackDiscard || discard.ContentKind != RTCDevicePlaybackConsumed || discard.PlaybackResponse != firstResponse || discard.SampleCount != len(second) || discard.Consumed || discard.StartSample != 2000 || discard.EndSample != 4000 {
		t.Fatalf("discard = %+v, want unconsumed first-response tail", discard)
	}
	sink.StartPlayback(secondResponse)
	if err := sink.WritePlayback(context.Background(), second); err != nil {
		t.Fatalf("write healthy response: %v", err)
	}
	admission := c21NextObservation(t, sub)
	if admission.Kind != RTCDevicePlaybackAdmission || admission.PlaybackResponse != secondResponse || admission.Generation == heard.Generation {
		t.Fatalf("healthy admission = %+v, want new generation", admission)
	}
	if err := registry.Advance(1); err != nil {
		t.Fatalf("advance healthy response: %v", err)
	}
	healthy := c21NextObservation(t, sub)
	assertC21Consumed(t, healthy, secondResponse, 24000, 2000, second)
	if got := sink.PlaybackObservationStats().DeviceSamples; got != 4000 {
		t.Fatalf("device sample clock = %d, want 4000 actual callback samples", got)
	}
}

func TestC21ConsumptionDeviceRate(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 16000, 320)
	defer func() { _ = sink.Close() }()
	sub, err := sink.SubscribePlaybackObservations(16)
	if err != nil {
		t.Fatalf("subscribe playback observations: %v", err)
	}
	provider := boundaryTestPCM(480, 1001)
	converter, err := wavio.NewPCM16Resampler(wavio.Rate24kHz, 16000)
	if err != nil {
		t.Fatalf("new provider/device resampler: %v", err)
	}
	deviceSamples, err := converter.Process(provider, true)
	if err != nil {
		t.Fatalf("convert provider samples: %v", err)
	}
	response := audio.PlaybackResponse{ResponseID: "c21-rate-response", ItemID: "c21-rate-item"}
	sink.StartPlayback(response)
	if err := sink.WritePlayback(context.Background(), deviceSamples); err != nil {
		t.Fatalf("write converted device samples: %v", err)
	}
	admission := c21NextObservation(t, sub)
	if admission.SampleRate != 16000 || admission.SampleCount != len(deviceSamples) || !reflect.DeepEqual(admission.Samples, deviceSamples) {
		t.Fatalf("rate admission = %+v, want exact %d-sample 16k PCM", admission, len(deviceSamples))
	}
	if err := registry.Advance(1); err != nil {
		t.Fatalf("advance converted callback: %v", err)
	}
	consumed := c21NextObservation(t, sub)
	assertC21Consumed(t, consumed, response, 16000, 0, deviceSamples)
}

func TestC21ObservationBoundsStalledConsumer(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 24000, 1)
	defer func() { _ = sink.Close() }()
	sub, err := sink.SubscribePlaybackObservations(1)
	if err != nil {
		t.Fatalf("subscribe bounded observations: %v", err)
	}
	for index := 0; index < 300; index++ {
		response := audio.PlaybackResponse{ResponseID: "c21-many-response", ItemID: "c21-item-" + fmt.Sprint(index)}
		sink.StartPlayback(response)
		if err := sink.WritePlayback(context.Background(), []int16{int16(index + 1)}); err != nil {
			t.Fatalf("write response %d: %v", index, err)
		}
	}
	if err := registry.Advance(300); err != nil {
		t.Fatalf("advance stalled-consumer callbacks: %v", err)
	}
	stats := sub.Stats()
	if stats.DroppedObservations == 0 || stats.DroppedSamples == 0 || stats.MetadataLostSamples == 0 || stats.DeviceSamples != 300 || stats.LastSequence <= stats.PublishedObservations {
		t.Fatalf("bounded observation stats = %+v, want explicit finite delivery and metadata loss", stats)
	}
	if stats.MetadataLostSamples != 45 {
		t.Fatalf("metadata loss = %d, want 45 samples beyond the 256-span bound", stats.MetadataLostSamples)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("close stalled subscription: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("repeat close stalled subscription: %v", err)
	}
}

func c21NextObservation(t *testing.T, sub *RTCDevicePlaybackObservationSubscription) RTCDevicePlaybackObservation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("next playback observation: %v", err)
	}
	return event
}

func assertC21Consumed(t *testing.T, got RTCDevicePlaybackObservation, response audio.PlaybackResponse, rate int, start uint64, samples []int16) {
	t.Helper()
	if got.Kind != RTCDevicePlaybackConsumed || got.ContentKind != RTCDevicePlaybackConsumed || got.DeviceID != "simulated-duplex:output" || got.PlaybackResponse != response || got.ResponseID != response.ResponseID || got.ItemID != response.ItemID || got.SampleRate != rate || got.StartSample != start || got.EndSample != start+uint64(len(samples)) || got.SampleCount != len(samples) || !got.Consumed || !got.Precise || !reflect.DeepEqual(got.Samples, samples) || !reflect.DeepEqual(got.PCM, samples) {
		t.Fatalf("consumed observation = %+v, want response %q range [%d,%d)", got, response.ItemID, start, start+uint64(len(samples)))
	}
}

func newC21SimulatedSink(t *testing.T, rate int, quanta ...int) (*devicegw.SimulatedDuplexRegistry, *RTCDeviceSink) {
	t.Helper()
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:   devicegw.ClockSpec{NominalRate: rate, Quanta: quanta},
		Capture:  devicegw.ClockSpec{NominalRate: rate, Quanta: quanta},
		Acoustic: devicegw.AcousticSpec{GainQ15: 32768},
	})
	if err != nil {
		t.Fatalf("new simulated C21 registry: %v", err)
	}
	sink, err := NewRTCDeviceSinkAtRate(registry, "simulated-duplex:output", rate)
	if err != nil {
		t.Fatalf("open simulated C21 sink: %v", err)
	}
	return registry, sink
}
