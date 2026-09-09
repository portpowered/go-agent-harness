package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	runtimegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

type observed struct {
	Sequence    uint64                                     `json:"sequence"`
	Kind        runtimegw.RTCDevicePlaybackObservationKind `json:"kind"`
	ContentKind runtimegw.RTCDevicePlaybackObservationKind `json:"content_kind"`
	ResponseID  string                                     `json:"response_id,omitempty"`
	ItemID      string                                     `json:"item_id,omitempty"`
	Generation  uint64                                     `json:"generation"`
	DeviceID    devicegw.DeviceID                          `json:"device_id"`
	SampleRate  int                                        `json:"sample_rate"`
	Start       uint64                                     `json:"start_sample"`
	End         uint64                                     `json:"end_sample"`
	Count       int                                        `json:"sample_count"`
	Consumed    bool                                       `json:"consumed"`
	Precise     bool                                       `json:"precise"`
	Accepted    bool                                       `json:"accepted"`
	PCM         []int16                                    `json:"pcm,omitempty"`
	Reason      string                                     `json:"reason,omitempty"`
}

type report struct {
	Supported        bool                                        `json:"supported"`
	PausedSamples    uint64                                      `json:"paused_device_samples"`
	DeviceRate       int                                         `json:"device_rate"`
	Observations     []observed                                  `json:"observations"`
	RateObservations []observed                                  `json:"rate_observations"`
	StalledStats     runtimegw.RTCDevicePlaybackObservationStats `json:"stalled_stats"`
	PrimaryStats     runtimegw.RTCDevicePlaybackObservationStats `json:"primary_stats"`
	CloseEOF         bool                                        `json:"close_eof"`
	ResampledPCM     []int16                                     `json:"resampled_pcm"`
}

func main() {
	result, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (report, error) {
	registry, sink, err := openSink(16000, 4)
	if err != nil {
		return report{}, err
	}
	sub, err := sink.SubscribePlaybackObservations(128)
	if err != nil {
		_ = sink.Close()
		return report{}, fmt.Errorf("subscribe primary observation stream: %w", err)
	}
	result := report{Supported: sink.PlaybackConsumptionSupported(), DeviceRate: sink.DeviceSampleRate()}
	if !result.Supported {
		_ = sink.Close()
		return report{}, fmt.Errorf("simulated sink did not advertise callback consumption")
	}
	first := audio.PlaybackResponse{ResponseID: "consumer-response-1", ItemID: "consumer-item-1"}
	second := audio.PlaybackResponse{ResponseID: "consumer-response-2", ItemID: "consumer-item-2"}
	if err := writeResponse(sink, first, []int16{10, 11, 12}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := writeResponse(sink, second, []int16{20, 21, 22}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	result.PausedSamples = sink.PlaybackObservationStats().DeviceSamples
	if result.PausedSamples != 0 {
		_ = sink.Close()
		return report{}, fmt.Errorf("paused callback consumed %d samples", result.PausedSamples)
	}
	if err := appendAdmission(&result, sub, first); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendAdmission(&result, sub, second); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := expectNoObservation(sub); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := registry.Advance(1); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendConsumed(&result, sub, first, 0, []int16{10, 11, 12}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendConsumed(&result, sub, second, 3, []int16{20}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := registry.Advance(1); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendConsumed(&result, sub, second, 4, []int16{21, 22}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendKind(&result, sub, runtimegw.RTCDevicePlaybackUnderflow, 6, []int16{0, 0}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := sink.WritePlaybackHoldTone(context.Background(), []int16{7, 8}); err != nil {
		_ = sink.Close()
		return report{}, fmt.Errorf("write hold tone: %w", err)
	}
	if err := appendAdmissionKind(&result, sub, runtimegw.RTCDevicePlaybackHoldTone); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := registry.Advance(1); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendKind(&result, sub, runtimegw.RTCDevicePlaybackHoldTone, 8, []int16{7, 8}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendKind(&result, sub, runtimegw.RTCDevicePlaybackUnderflow, 10, []int16{0, 0}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	third := audio.PlaybackResponse{ResponseID: "consumer-response-interrupted", ItemID: "consumer-item-interrupted"}
	fourth := audio.PlaybackResponse{ResponseID: "consumer-response-healthy", ItemID: "consumer-item-healthy"}
	if err := writeResponse(sink, third, []int16{30, 31, 32, 33}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendAdmission(&result, sub, third); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	interruption, ok := sink.InterruptActivePlayback()
	if !ok || interruption.PlaybackResponse != third || interruption.AudioEndMS != 0 {
		_ = sink.Close()
		return report{}, fmt.Errorf("unexpected pre-consumption interruption: %+v, ok=%v", interruption, ok)
	}
	if err := appendDiscard(&result, sub, third, 12, 4); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := writeResponse(sink, fourth, []int16{40, 41, 42, 43}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendAdmission(&result, sub, fourth); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := registry.Advance(1); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := appendConsumed(&result, sub, fourth, 12, []int16{40, 41, 42, 43}); err != nil {
		_ = sink.Close()
		return report{}, err
	}
	if err := sink.Close(); err != nil {
		return report{}, fmt.Errorf("close primary sink: %w", err)
	}
	_, err = sub.Next(context.Background())
	result.CloseEOF = err == io.EOF
	if !result.CloseEOF {
		return report{}, fmt.Errorf("closed primary subscription returned %v, want EOF", err)
	}
	result.PrimaryStats = sub.Stats()
	result.ResampledPCM, result.RateObservations, err = runRateScenario()
	if err != nil {
		return report{}, err
	}
	result.StalledStats, err = runStalledConsumer()
	if err != nil {
		return report{}, err
	}
	return result, nil
}

func openSink(rate int, quantum int) (*devicegw.SimulatedDuplexRegistry, *runtimegw.RTCDeviceSink, error) {
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:   devicegw.ClockSpec{NominalRate: rate, Quanta: []int{quantum}},
		Capture:  devicegw.ClockSpec{NominalRate: rate, Quanta: []int{quantum}},
		Acoustic: devicegw.AcousticSpec{GainQ15: 32768},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create simulated device: %w", err)
	}
	sink, err := runtimegw.NewRTCDeviceSinkAtRate(registry, "simulated-duplex:output", rate)
	if err != nil {
		return nil, nil, fmt.Errorf("open simulated device: %w", err)
	}
	return registry, sink, nil
}

func writeResponse(sink *runtimegw.RTCDeviceSink, response audio.PlaybackResponse, samples []int16) error {
	sink.StartPlayback(response)
	if err := sink.WritePlayback(context.Background(), samples); err != nil {
		return fmt.Errorf("write %q: %w", response.ItemID, err)
	}
	return nil
}

func next(sub *runtimegw.RTCDevicePlaybackObservationSubscription) (runtimegw.RTCDevicePlaybackObservation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return sub.Next(ctx)
}

func expectNoObservation(sub *runtimegw.RTCDevicePlaybackObservationSubscription) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := sub.Next(ctx)
	if err != context.DeadlineExceeded {
		return fmt.Errorf("callback-paused observation = %v, want deadline", err)
	}
	return nil
}

func appendAdmission(result *report, sub *runtimegw.RTCDevicePlaybackObservationSubscription, response audio.PlaybackResponse) error {
	event, err := next(sub)
	if err != nil {
		return err
	}
	if event.Kind != runtimegw.RTCDevicePlaybackAdmission || event.PlaybackResponse != response || !event.Accepted || event.Consumed || event.DeviceID != "simulated-duplex:output" || event.SampleRate != 16000 {
		return fmt.Errorf("admission mismatch: %+v", event)
	}
	result.Observations = append(result.Observations, project(event))
	return nil
}

func appendAdmissionKind(result *report, sub *runtimegw.RTCDevicePlaybackObservationSubscription, kind runtimegw.RTCDevicePlaybackObservationKind) error {
	event, err := next(sub)
	if err != nil {
		return err
	}
	if event.Kind != runtimegw.RTCDevicePlaybackAdmission || event.ContentKind != kind || event.ResponseID != "" || event.SampleRate != 16000 {
		return fmt.Errorf("%s admission mismatch: %+v", kind, event)
	}
	result.Observations = append(result.Observations, project(event))
	return nil
}

func appendConsumed(result *report, sub *runtimegw.RTCDevicePlaybackObservationSubscription, response audio.PlaybackResponse, start uint64, samples []int16) error {
	event, err := next(sub)
	if err != nil {
		return err
	}
	if event.Kind != runtimegw.RTCDevicePlaybackConsumed || event.PlaybackResponse != response || event.StartSample != start || event.EndSample != start+uint64(len(samples)) || event.SampleCount != len(samples) || !event.Consumed || !event.Precise || !sameSamples(event.Samples, samples) {
		return fmt.Errorf("consumed mismatch: %+v", event)
	}
	result.Observations = append(result.Observations, project(event))
	return nil
}

func appendKind(result *report, sub *runtimegw.RTCDevicePlaybackObservationSubscription, kind runtimegw.RTCDevicePlaybackObservationKind, start uint64, samples []int16) error {
	event, err := next(sub)
	if err != nil {
		return err
	}
	if event.Kind != kind || event.StartSample != start || event.EndSample != start+uint64(len(samples)) || event.SampleCount != len(samples) || !event.Consumed || event.Precise || !sameSamples(event.Samples, samples) {
		return fmt.Errorf("%s mismatch: %+v", kind, event)
	}
	result.Observations = append(result.Observations, project(event))
	return nil
}

func appendDiscard(result *report, sub *runtimegw.RTCDevicePlaybackObservationSubscription, response audio.PlaybackResponse, start uint64, count int) error {
	event, err := next(sub)
	if err != nil {
		return err
	}
	if event.Kind != runtimegw.RTCDevicePlaybackDiscard || event.PlaybackResponse != response || event.StartSample != start || event.SampleCount != count || event.Consumed || !event.Precise {
		return fmt.Errorf("discard mismatch: %+v", event)
	}
	result.Observations = append(result.Observations, project(event))
	return nil
}

func project(event runtimegw.RTCDevicePlaybackObservation) observed {
	return observed{Sequence: event.Sequence, Kind: event.Kind, ContentKind: event.ContentKind, ResponseID: event.ResponseID, ItemID: event.ItemID, Generation: event.Generation, DeviceID: event.DeviceID, SampleRate: event.SampleRate, Start: event.StartSample, End: event.EndSample, Count: event.SampleCount, Consumed: event.Consumed, Precise: event.Precise, Accepted: event.Accepted, PCM: append([]int16(nil), event.Samples...), Reason: event.Reason}
}

func sameSamples(left, right []int16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func runRateScenario() ([]int16, []observed, error) {
	registry, sink, err := openSink(16000, 32)
	if err != nil {
		return nil, nil, err
	}
	sub, err := sink.SubscribePlaybackObservations(8)
	if err != nil {
		_ = sink.Close()
		return nil, nil, err
	}
	provider := make([]int16, 48)
	for index := range provider {
		provider[index] = 1000 + int16(index%7)
	}
	converter, err := wavio.NewPCM16Resampler(wavio.Rate24kHz, 16000)
	if err != nil {
		_ = sink.Close()
		return nil, nil, fmt.Errorf("create rate converter: %w", err)
	}
	converted, err := converter.Process(provider, true)
	if err != nil {
		_ = sink.Close()
		return nil, nil, fmt.Errorf("convert provider fixture: %w", err)
	}
	if len(converted) != 32 || !hasNonZero(converted) {
		_ = sink.Close()
		return nil, nil, fmt.Errorf("converted fixture = %d samples, nonzero=%v; want 32 nonzero samples", len(converted), hasNonZero(converted))
	}
	response := audio.PlaybackResponse{ResponseID: "consumer-response-rate", ItemID: "consumer-item-rate"}
	if err := writeResponse(sink, response, converted); err != nil {
		_ = sink.Close()
		return nil, nil, err
	}
	admission, err := next(sub)
	if err != nil {
		_ = sink.Close()
		return nil, nil, err
	}
	if admission.Kind != runtimegw.RTCDevicePlaybackAdmission || admission.SampleRate != 16000 || admission.SampleCount != len(converted) || !sameSamples(admission.Samples, converted) {
		_ = sink.Close()
		return nil, nil, fmt.Errorf("rate admission mismatch: %+v", admission)
	}
	if err := registry.Advance(1); err != nil {
		_ = sink.Close()
		return nil, nil, err
	}
	consumed, err := next(sub)
	if err != nil {
		_ = sink.Close()
		return nil, nil, err
	}
	if consumed.Kind != runtimegw.RTCDevicePlaybackConsumed || consumed.PlaybackResponse != response || consumed.StartSample != 0 || consumed.EndSample != 32 || !sameSamples(consumed.Samples, converted) {
		_ = sink.Close()
		return nil, nil, fmt.Errorf("rate consumption mismatch: %+v", consumed)
	}
	if err := sink.Close(); err != nil {
		return nil, nil, fmt.Errorf("close rate sink: %w", err)
	}
	return converted, []observed{project(admission), project(consumed)}, nil
}

func hasNonZero(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}

func runStalledConsumer() (runtimegw.RTCDevicePlaybackObservationStats, error) {
	registry, sink, err := openSink(24000, 1)
	if err != nil {
		return runtimegw.RTCDevicePlaybackObservationStats{}, err
	}
	sub, err := sink.SubscribePlaybackObservations(1)
	if err != nil {
		_ = sink.Close()
		return runtimegw.RTCDevicePlaybackObservationStats{}, err
	}
	for index := 0; index < 300; index++ {
		response := audio.PlaybackResponse{ResponseID: "stalled-response", ItemID: fmt.Sprintf("stalled-item-%03d", index)}
		if err := writeResponse(sink, response, []int16{int16(index + 1)}); err != nil {
			_ = sink.Close()
			return runtimegw.RTCDevicePlaybackObservationStats{}, err
		}
	}
	if err := registry.Advance(300); err != nil {
		_ = sink.Close()
		return runtimegw.RTCDevicePlaybackObservationStats{}, err
	}
	stats := sub.Stats()
	if stats.DroppedObservations == 0 || stats.DroppedSamples == 0 || stats.MetadataLostSamples != 45 || stats.DeviceSamples != 300 || stats.LastSequence <= stats.PublishedObservations {
		_ = sink.Close()
		return runtimegw.RTCDevicePlaybackObservationStats{}, fmt.Errorf("stalled-consumer bounds mismatch: %+v", stats)
	}
	if err := sink.Close(); err != nil {
		return runtimegw.RTCDevicePlaybackObservationStats{}, fmt.Errorf("close stalled sink: %w", err)
	}
	stats = sub.Stats()
	_ = sub.Close()
	return stats, nil
}
