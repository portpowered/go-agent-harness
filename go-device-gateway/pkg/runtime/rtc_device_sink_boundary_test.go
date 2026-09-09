package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
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
	testC21ConsumptionBoundaryResponses(t)
	testC21ConsumptionBoundaryHoldTone(t)
}

func testC21ConsumptionBoundaryResponses(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 16000, 5)
	defer closeC21Sink(t, sink)
	sub, err := sink.SubscribePlaybackObservations(64)
	if err != nil {
		t.Fatalf("subscribe playback observations: %v", err)
	}
	firstResponse := audio.PlaybackResponse{ResponseID: "c21-response-1", ItemID: "c21-item-1", ContentIndex: 0}
	secondResponse := audio.PlaybackResponse{ResponseID: "c21-response-2", ItemID: "c21-item-2", ContentIndex: 0}
	first := []int16{101, 102, 103}
	second := []int16{201, 202, 203}
	sink.StartPlayback(firstResponse)
	c21WritePlayback(t, sink, "write first response", first)
	sink.StartPlayback(secondResponse)
	c21WritePlayback(t, sink, "write second response", second)
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
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("observation before callback error = %v, want deadline", err)
	}
	c21Advance(t, registry, 1, "advance first callback")
	firstConsumed := c21NextObservation(t, sub)
	assertC21Consumed(t, firstConsumed, firstResponse, 16000, 0, first)
	secondPrefix := c21NextObservation(t, sub)
	assertC21Consumed(t, secondPrefix, secondResponse, 16000, 3, second[:2])
	c21Advance(t, registry, 1, "advance second callback")
	secondTail := c21NextObservation(t, sub)
	assertC21Consumed(t, secondTail, secondResponse, 16000, 5, second[2:])
	underflow := c21NextObservation(t, sub)
	if underflow.Kind != RTCDevicePlaybackUnderflow || underflow.ResponseID != "" || underflow.SampleRate != 16000 || underflow.StartSample != 6 || underflow.EndSample != 10 || underflow.SampleCount != 4 || !underflow.Consumed || underflow.Precise || !reflect.DeepEqual(underflow.Samples, []int16{0, 0, 0, 0}) {
		t.Fatalf("underflow = %+v, want four zero-filled device samples", underflow)
	}
}

func testC21ConsumptionBoundaryHoldTone(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 16000, 5)
	sub, err := sink.SubscribePlaybackObservations(16)
	if err != nil {
		t.Fatalf("subscribe hold-tone observations: %v", err)
	}
	response := audio.PlaybackResponse{ResponseID: "c21-hold-response", ItemID: "c21-hold-item"}
	samples := []int16{11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	sink.StartPlayback(response)
	c21WritePlayback(t, sink, "write model audio before hold tone", samples)
	_ = c21NextObservation(t, sub)
	c21Advance(t, registry, 2, "advance model audio before hold tone")
	_ = c21NextObservation(t, sub)
	_ = c21NextObservation(t, sub)
	holdTone := []int16{7, 8}
	c21WriteHoldTone(t, sink, holdTone)
	holdAdmission := c21NextObservation(t, sub)
	if holdAdmission.Kind != RTCDevicePlaybackAdmission || holdAdmission.ContentKind != RTCDevicePlaybackHoldTone || holdAdmission.ResponseID != "" || holdAdmission.Precise {
		t.Fatalf("hold-tone admission = %+v", holdAdmission)
	}
	c21Advance(t, registry, 1, "advance hold-tone callback")
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
	if _, err := sub.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("closed observation stream error = %v, want EOF", err)
	}
}

func TestC21ConsumptionInterruptionAndRate(t *testing.T) {
	testC21ConsumptionInterruption(t)
	testC21ConsumptionRate(t)
}

func TestC21ConsumptionInterruptionAtIncompleteSpanBoundary(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 16000, audio.FrameSize)
	defer closeC21Sink(t, sink)
	response := audio.PlaybackResponse{ResponseID: "c21-boundary-response", ItemID: "c21-boundary-item"}
	sink.StartPlayback(response)
	c21WritePlayback(t, sink, "write first exact-boundary response chunk", boundaryTestPCM(audio.FrameSize, 1301))
	c21Advance(t, registry, 1, "advance first exact-boundary response chunk")
	c21WritePlayback(t, sink, "write second exact-boundary response chunk", boundaryTestPCM(audio.FrameSize, 1701))
	c21Advance(t, registry, 1, "advance second exact-boundary response chunk")

	interruption, ok := sink.InterruptActivePlayback()
	if !ok || interruption.PlaybackResponse != response || interruption.AudioEndMS != 60 {
		t.Fatalf("exact-boundary interruption = %+v, ok=%v, want 60ms of incomplete response", interruption, ok)
	}
	if stats := sink.PlaybackStats(); stats.DiscardEvents != 1 {
		t.Fatalf("exact-boundary discard stats = %+v, want one accepted discard boundary", stats)
	}
}

func testC21ConsumptionInterruption(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 24000, 2000)
	defer closeC21Sink(t, sink)
	sub, err := sink.SubscribePlaybackObservations(32)
	if err != nil {
		t.Fatalf("subscribe playback observations: %v", err)
	}
	firstResponse := audio.PlaybackResponse{ResponseID: "c21-interrupt-1", ItemID: "c21-interrupt-item-1"}
	secondResponse := audio.PlaybackResponse{ResponseID: "c21-interrupt-2", ItemID: "c21-interrupt-item-2"}
	first := boundaryTestPCM(2000, 301)
	second := boundaryTestPCM(2000, 701)
	sink.StartPlayback(firstResponse)
	c21WritePlayback(t, sink, "write first interrupted chunk", first)
	c21WritePlayback(t, sink, "write second interrupted chunk", second)
	_ = c21NextObservation(t, sub)
	_ = c21NextObservation(t, sub)
	c21Advance(t, registry, 1, "advance interrupted response")
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
	c21WritePlayback(t, sink, "write healthy response", second)
	admission := c21NextObservation(t, sub)
	if admission.Kind != RTCDevicePlaybackAdmission || admission.PlaybackResponse != secondResponse || admission.Generation == heard.Generation {
		t.Fatalf("healthy admission = %+v, want new generation", admission)
	}
	c21Advance(t, registry, 1, "advance healthy response")
	healthy := c21NextObservation(t, sub)
	assertC21Consumed(t, healthy, secondResponse, 24000, 2000, second)
	if got := sink.PlaybackObservationStats().DeviceSamples; got != 4000 {
		t.Fatalf("device sample clock = %d, want 4000 actual callback samples", got)
	}
}

func testC21ConsumptionRate(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 16000, 320)
	defer closeC21Sink(t, sink)
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
	c21WritePlayback(t, sink, "write converted device samples", deviceSamples)
	admission := c21NextObservation(t, sub)
	if admission.SampleRate != 16000 || admission.SampleCount != len(deviceSamples) || !reflect.DeepEqual(admission.Samples, deviceSamples) {
		t.Fatalf("rate admission = %+v, want exact %d-sample 16k PCM", admission, len(deviceSamples))
	}
	c21Advance(t, registry, 1, "advance converted callback")
	consumed := c21NextObservation(t, sub)
	assertC21Consumed(t, consumed, response, 16000, 0, deviceSamples)
}

func TestC21ObservationBoundsStalledConsumer(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, 24000, 1)
	defer closeC21Sink(t, sink)
	sub, err := sink.SubscribePlaybackObservations(1)
	if err != nil {
		t.Fatalf("subscribe bounded observations: %v", err)
	}
	for index := 0; index < 300; index++ {
		response := audio.PlaybackResponse{ResponseID: "c21-many-response", ItemID: "c21-item-" + fmt.Sprint(index)}
		sink.StartPlayback(response)
		c21WritePlayback(t, sink, fmt.Sprintf("write response %d", index), []int16{int16(index + 1)})
	}
	c21Advance(t, registry, 300, "advance stalled-consumer callbacks")
	c21WaitForDeviceSamples(t, sub, 300)
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

// TestC21DelayedRenderObservationDoesNotDoubleCountDiscardedPCM models the
// native queue's callback handoff: RenderInto has removed the first samples,
// but its observer is delayed while interruption discards the remaining tail.
// The callback-owned prefix must be consumed once, and only the native tail
// may be reported as discarded.
func TestC21DelayedRenderObservationDoesNotDoubleCountDiscardedPCM(t *testing.T) {
	handle := newC21DelayedPlaybackHandle(t, audio.SampleRate)
	registry := newC21DelayedPlaybackRegistry(t, handle)
	sink, err := NewRTCDeviceSink(registry, handle.deviceID)
	if err != nil {
		t.Fatalf("open delayed callback sink: %v", err)
	}
	defer closeC21Sink(t, sink)
	sub, err := sink.SubscribePlaybackObservations(16)
	if err != nil {
		t.Fatalf("subscribe delayed callback observations: %v", err)
	}
	response := audio.PlaybackResponse{ResponseID: "c21-race-response", ItemID: "c21-race-item"}
	samples := []int16{1, 2, 3, 4, 5, 6, 7, 8}
	sink.StartPlayback(response)
	c21WritePlayback(t, sink, "write delayed callback response", samples)
	_ = c21NextObservation(t, sub)
	c21AssertDelayedRenderLinearization(t, handle, sink, sub, response, samples)
}

func TestC21ObservationIdentityAndLifecycleControls(t *testing.T) {
	registry, sink := newC21SimulatedSink(t, audio.SampleRate, 3)
	defer closeC21Sink(t, sink)
	second := c21ReplaceObservationSubscription(t, sink)
	c21AssertBoundedIdentity(t, registry, sink, second)
	if _, err := second.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("closed replacement subscription error = %v, want EOF", err)
	}
	if _, err := sink.SubscribePlaybackObservations(1); !errors.Is(err, ErrRTCDevicePlaybackObservationClosed) {
		t.Fatalf("subscribe after sink close error = %v", err)
	}
}

func c21AssertDelayedRenderLinearization(t *testing.T, handle *c21DelayedPlaybackHandle, sink *RTCDeviceSink, sub *RTCDevicePlaybackObservationSubscription, response audio.PlaybackResponse, samples []int16) {
	t.Helper()
	renderDone := c21StartDelayedRender(handle)
	c21WaitDelayedCallback(t, handle)
	interruption, ok := sink.InterruptActivePlayback()
	if !ok || interruption.PlaybackResponse != response || interruption.AudioEndMS != 0 {
		t.Fatalf("delayed callback interruption = %+v, ok=%v", interruption, ok)
	}
	handle.releaseObserver()
	c21WaitDelayedRender(t, renderDone)
	consumed := c21NextObservation(t, sub)
	assertC21DelayedConsumed(t, consumed, response, samples)
	discard := c21NextObservation(t, sub)
	assertC21DelayedDiscard(t, discard, response)
	assertC21NoDelayedDuplicate(t, sub)
}

func c21ReplaceObservationSubscription(t *testing.T, sink *RTCDeviceSink) *RTCDevicePlaybackObservationSubscription {
	t.Helper()
	if _, err := sink.SubscribePlaybackObservations(0); !errors.Is(err, ErrInvalidRTCDevicePlaybackObservationCapacity) {
		t.Fatalf("zero observation capacity error = %v", err)
	}
	if _, err := sink.SubscribePlaybackObservations(MaxRTCDevicePlaybackObservationCapacity + 1); !errors.Is(err, ErrInvalidRTCDevicePlaybackObservationCapacity) {
		t.Fatalf("oversized observation capacity error = %v", err)
	}
	if _, err := sink.SubscribePlaybackObservations(1, 2); !errors.Is(err, ErrInvalidRTCDevicePlaybackObservationCapacity) {
		t.Fatalf("multiple observation capacities error = %v", err)
	}
	first, err := sink.SubscribePlaybackObservations(8)
	if err != nil {
		t.Fatalf("first lifecycle subscription: %v", err)
	}
	var nilContext context.Context
	if _, err := first.Next(nilContext); !errors.Is(err, ErrInvalidRTCDevicePlaybackObservationContext) {
		t.Fatalf("nil observation context error = %v", err)
	}
	second, err := sink.SubscribePlaybackObservations(8)
	if err != nil {
		t.Fatalf("replacement lifecycle subscription: %v", err)
	}
	if _, err := first.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("replaced subscription error = %v, want EOF", err)
	}
	return second
}

func c21AssertBoundedIdentity(t *testing.T, registry *devicegw.SimulatedDuplexRegistry, sink *RTCDeviceSink, sub *RTCDevicePlaybackObservationSubscription) {
	t.Helper()
	longResponse := audio.PlaybackResponse{
		ResponseID: strings.Repeat("r", maxRTCDevicePlaybackIdentityBytes+17),
		ItemID:     strings.Repeat("i", maxRTCDevicePlaybackIdentityBytes+31),
	}
	sink.StartPlayback(longResponse)
	c21WritePlayback(t, sink, "write bounded identity response", []int16{21, 22, 23})
	admission := c21NextObservation(t, sub)
	if admission.Precise || len(admission.ResponseID) > maxRTCDevicePlaybackIdentityBytes || len(admission.ItemID) > maxRTCDevicePlaybackIdentityBytes || !strings.HasPrefix(admission.ResponseID, "sha256:") || !strings.HasPrefix(admission.ItemID, "sha256:") || !strings.Contains(admission.Reason, rtcDevicePlaybackIdentityOverflowReason) {
		t.Fatalf("bounded identity admission = %+v", admission)
	}
	c21Advance(t, registry, 1, "advance bounded identity callback")
	consumed := c21NextObservation(t, sub)
	if consumed.Precise || len(consumed.PlaybackResponse.ResponseID) > maxRTCDevicePlaybackIdentityBytes || len(consumed.PlaybackResponse.ItemID) > maxRTCDevicePlaybackIdentityBytes || !strings.Contains(consumed.Reason, rtcDevicePlaybackIdentityOverflowReason) {
		t.Fatalf("bounded identity consumed = %+v", consumed)
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("close lifecycle sink: %v", err)
	}
}

func TestC21ObservationUnsupportedBackendControl(t *testing.T) {
	handle := &adversarialCapacityHandle{release: make(chan struct{})}
	registry := newAdversarialCapacityRegistry(t, handle)
	sink, err := NewRTCDeviceSink(registry, "adversarial:output")
	if err != nil {
		t.Fatalf("open unsupported observation sink: %v", err)
	}
	defer closeC21Sink(t, sink)
	if sink.PlaybackConsumptionSupported() {
		t.Fatal("backend without render observer advertised consumption support")
	}
	if _, err := sink.SubscribePlaybackObservations(1); !errors.Is(err, ErrRTCDevicePlaybackObservationUnsupported) {
		t.Fatalf("unsupported observation error = %v", err)
	}
}

func closeC21Sink(t *testing.T, sink *RTCDeviceSink) {
	t.Helper()
	if err := sink.Close(); err != nil {
		t.Errorf("close C21 sink: %v", err)
	}
}

func c21WritePlayback(t *testing.T, sink *RTCDeviceSink, label string, samples []int16) {
	t.Helper()
	if err := sink.WritePlayback(context.Background(), samples); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func c21WriteHoldTone(t *testing.T, sink *RTCDeviceSink, samples []int16) {
	t.Helper()
	if err := sink.WritePlaybackHoldTone(context.Background(), samples); err != nil {
		t.Fatalf("write hold tone: %v", err)
	}
}

func c21Advance(t *testing.T, registry *devicegw.SimulatedDuplexRegistry, callbacks int, label string) {
	t.Helper()
	if err := registry.Advance(callbacks); err != nil {
		t.Fatalf("%s: %v", label, err)
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
