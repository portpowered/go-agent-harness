package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// TestSessionAudioTraceObservesRealDeviceAndProviderEdges exercises the same
// RTC source/sink pumps as a live session. It deliberately filters capture
// before upload and cancels queued playback after one device read, proving the
// four artifacts describe distinct edges rather than copies of one internal
// buffer.
func TestSessionAudioTraceObservesRealDeviceAndProviderEdges(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "trace")
	trace, err := NewSessionAudioTrace(directory)
	if err != nil {
		t.Fatal(err)
	}

	preGate := make([]int16, audio.FrameSize)
	for index := range preGate {
		preGate[index] = int16(index + 1) //nolint:gosec // bounded fixture
	}
	uploadSource := pumpOneFilteredCaptureFrame(t, trace, preGate)
	wantUpload, err := wavio.Resample(preGate[len(preGate)/2:], audio.SampleRate, wavio.Rate24kHz)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(uploadSource, wantUpload) {
		t.Fatalf("provider upload = %v, want filtered capture suffix", uploadSource[:4])
	}

	providerPlayback := make([]int16, 2*720)
	for index := range providerPlayback {
		providerPlayback[index] = int16(2000 - index) //nolint:gosec // bounded fixture
	}
	wantEnqueued, err := wavio.Resample(providerPlayback, wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	pumpPlaybackThenCancelTail(t, trace, providerPlayback)

	trace.ObserveSessionRuntime(SessionRuntimeObservation{Kind: SessionRuntimeObservationResponseCreate, Tick: 7, ResponseID: "response-1"})
	if err := trace.Close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}

	assertTraceWAV(t, directory, "microphone-pre-gate.wav", audio.SampleRate, preGate)
	assertTraceWAV(t, directory, "microphone-uploaded.wav", wavio.Rate24kHz, uploadSource)
	assertTraceWAV(t, directory, "speaker-enqueued.wav", audio.SampleRate, wantEnqueued)
	assertTraceWAV(t, directory, "speaker-rendered.wav", audio.SampleRate, wantEnqueued[:audio.FrameSize])

	events := readTraceEvents(t, filepath.Join(directory, "timeline.jsonl"))
	wantTaps := map[string]bool{
		"microphone_pre_gate": false, "microphone_uploaded": false,
		"speaker_enqueued": false, "speaker_rendered": false,
	}
	runtimeSeen := false
	for _, event := range events {
		if _, ok := wantTaps[event.Tap]; ok {
			wantTaps[event.Tap] = true
			if event.SampleRate <= 0 || event.SampleCount <= 0 || event.DurationNS <= 0 {
				t.Fatalf("incomplete timing event: %+v", event)
			}
		}
		if event.Kind == "runtime" && event.RuntimeKind == string(SessionRuntimeObservationResponseCreate) && event.ResponseID == "response-1" {
			runtimeSeen = true
		}
	}
	for tap, seen := range wantTaps {
		if !seen {
			t.Fatalf("timeline missing %s event", tap)
		}
	}
	if !runtimeSeen {
		t.Fatal("timeline missing response_create runtime boundary")
	}
}

type traceSuffixFilter struct{}

func (traceSuffixFilter) FilterCapture(_ context.Context, samples []int16) ([][]int16, error) {
	return [][]int16{append([]int16(nil), samples[len(samples)/2:]...)}, nil
}
func (traceSuffixFilter) DiscardHeld() {}

func pumpOneFilteredCaptureFrame(t *testing.T, trace *SessionAudioTrace, samples []int16) []int16 {
	t.Helper()
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	feed, err := audio.NewDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Close()
	source, err := NewRTCDeviceSourceAtRate(registry, "virtual:input", wavio.Rate24kHz)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	source.filter = traceSuffixFilter{}
	source.preGateSamplesObserver = trace.CaptureMicrophonePreGate
	source.uploadedSamplesObserver = trace.CaptureMicrophoneUploaded
	if err := feed.WriteFrame(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	outbound := &recordingRTCOutboundMedia{cancelAfterFirst: cancel}
	if err := source.Pump(ctx, outbound); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture pump: %v", err)
	}
	if len(outbound.frames) != 1 {
		t.Fatalf("uploaded frames = %d", len(outbound.frames))
	}
	return outbound.frames[0].Samples
}

func pumpPlaybackThenCancelTail(t *testing.T, trace *SessionAudioTrace, providerSamples []int16) {
	t.Helper()
	registry := newRTCDeviceSinkRateRegistry(t, audio.SampleRate)
	opened, err := registry.OpenWithFormat("virtual:input", audio.PCM16DeviceFormat(audio.SampleRate))
	if err != nil {
		t.Fatal(err)
	}
	observer := opened.(*audio.VirtualStream)
	defer observer.Close()
	sink, err := NewRTCDeviceSinkAtRate(registry, "virtual:output", wavio.Rate24kHz)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	sink.playbackSamplesObserver = trace.CaptureSpeakerEnqueued
	if !sink.setRenderedSamplesObserver(trace.CaptureSpeakerRendered) {
		t.Fatal("virtual output did not expose render observer")
	}
	frames := []rtc.PCMFrame{
		{Samples: append([]int16(nil), providerSamples[:720]...)},
		{Samples: append([]int16(nil), providerSamples[720:]...)},
	}
	pumpErr := make(chan error, 1)
	go func() { pumpErr <- sink.Pump(context.Background(), &recordingRTCInboundMedia{frames: frames}) }()
	deadline := time.Now().Add(time.Second)
	for sink.PlaybackStats().QueuedSamples < 2*audio.FrameSize && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	rendered := make([]int16, audio.FrameSize)
	if err := observer.ReadFrame(context.Background(), rendered); err != nil {
		t.Fatal(err)
	}
	if discarded := sink.DiscardPlayback(); discarded != audio.FrameSize {
		t.Fatalf("discarded tail = %d, want %d", discarded, audio.FrameSize)
	}
	if err := <-pumpErr; err != nil {
		t.Fatal(err)
	}
}

func assertTraceWAV(t *testing.T, directory, name string, wantRate int, want []int16) {
	t.Helper()
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rate, got, err := wavio.Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if rate != wantRate || !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = rate %d/%d samples %d/%d", name, rate, wantRate, len(got), len(want))
	}
}

func readTraceEvents(t *testing.T, path string) []sessionAudioTraceEvent {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []sessionAudioTraceEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event sessionAudioTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}
