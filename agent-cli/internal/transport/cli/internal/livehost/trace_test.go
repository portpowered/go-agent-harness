package livehost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestLiveTraceRecorderRoundTripsProviderAndPCM(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	provider := filepath.Join(root, "provider.json")
	writeTraceProviderCapture(t, provider)
	inner := &traceTestRecorder{destination: bundle}
	traced, err := newLiveTraceRecorder(inner, bundle, provider, 16_000, clock.Real{})
	if err != nil {
		t.Fatalf("newLiveTraceRecorder: %v", err)
	}
	wantSamples := []int16{1, -2, 3, -5}
	if err := traced.RecordAudio(context.Background(), runtimeSession.LiveAudioRecord{
		Direction: runtimeSession.LiveRecordAgent,
		Admission: runtimeSession.LiveAudioMediaBridged,
		Frame:     audio.PCMFrame{Format: audio.PCM16DeviceFormat(16_000), Samples: wantSamples},
	}); err != nil {
		t.Fatalf("RecordAudio: %v", err)
	}
	if err := traced.Finalize(context.Background(), nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	assertTraceReplay(t, filepath.Join(bundle, "audio-trace"), wantSamples)
	if inner.finalized != 1 {
		t.Fatalf("inner finalization count = %d, want 1", inner.finalized)
	}
}

func assertTraceReplay(t *testing.T, path string, wantSamples []int16) {
	t.Helper()
	replay, err := recording.OpenReplay(path)
	if err != nil {
		t.Fatalf("OpenReplay: %v", err)
	}
	var audioFrames int
	var providerWires int
	for {
		event, frame, nextErr := replay.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("Replay.Next: %v", nextErr)
		}
		if event.Kind == "audio" {
			audioFrames++
			assertTraceAudioFrame(t, frame, wantSamples)
		}
		if event.Kind == "runtime" && strings.HasPrefix(event.RuntimeKind, "provider_wire_") {
			providerWires++
		}
	}
	if audioFrames != 1 {
		t.Fatalf("audio frame count = %d, want 1", audioFrames)
	}
	if providerWires != 3 {
		t.Fatalf("provider wire count = %d, want 3", providerWires)
	}
}

func assertTraceAudioFrame(t *testing.T, frame *audio.PCMFrame, wantSamples []int16) {
	t.Helper()
	if frame == nil || len(frame.Samples) != len(wantSamples) {
		t.Fatalf("audio frame = %+v, want %d samples", frame, len(wantSamples))
	}
	for i, sample := range frame.Samples {
		if sample != wantSamples[i] {
			t.Fatalf("audio sample[%d] = %d, want %d", i, sample, wantSamples[i])
		}
	}
}

func TestLiveTraceRecorderRetainsTraceWhenProviderEvidenceIsMissing(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	stagedProvider := filepath.Join(root, "missing-provider.json")
	inner := &traceTestRecorder{destination: bundle}
	traced, err := newLiveTraceRecorder(inner, bundle, stagedProvider, 16_000, clock.Real{})
	if err != nil {
		t.Fatalf("newLiveTraceRecorder: %v", err)
	}
	err = traced.Finalize(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "load provider capture for audio trace") {
		t.Fatalf("Finalize error = %v, want provider capture diagnostic", err)
	}
	if _, statErr := os.Stat(filepath.Join(bundle, "audio-trace")); !os.IsNotExist(statErr) {
		t.Fatalf("missing provider evidence attached trace: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(traced.tracePath, "timeline.jsonl")); statErr != nil {
		t.Fatalf("retained trace timeline: %v", statErr)
	}
}

func TestLiveTraceRecorderRetainsTraceWhenProviderEvidenceIsCorrupt(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	provider := filepath.Join(root, "provider.json")
	if err := os.WriteFile(provider, []byte("not-json\n"), 0o600); err != nil {
		t.Fatalf("write corrupt provider capture: %v", err)
	}
	inner := &traceTestRecorder{destination: bundle}
	traced, err := newLiveTraceRecorder(inner, bundle, provider, 16_000, clock.Real{})
	if err != nil {
		t.Fatalf("newLiveTraceRecorder: %v", err)
	}
	err = traced.Finalize(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "load provider capture for audio trace") {
		t.Fatalf("Finalize error = %v, want provider capture diagnostic", err)
	}
	if _, statErr := os.Stat(filepath.Join(bundle, "audio-trace")); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt provider evidence attached trace: stat error = %v", statErr)
	}
}

type traceTestRecorder struct {
	destination string
	finalized   int
}

func (r *traceTestRecorder) RecordMessage(context.Context, runtimeSession.LiveRecord) error {
	return nil
}

func (r *traceTestRecorder) RecordAudio(context.Context, runtimeSession.LiveAudioRecord) error {
	return nil
}

func (r *traceTestRecorder) RecordEvent(context.Context, runtimeSession.LiveEvent) error {
	return nil
}

func (r *traceTestRecorder) Finalize(context.Context, error) error {
	r.finalized++
	return os.MkdirAll(r.destination, 0o755)
}

func writeTraceProviderCapture(t *testing.T, path string) {
	t.Helper()
	websocket := func(sequence int, direction gatewaytesting.SessionEventDirection, event string) gatewaytesting.CapturedSessionEvent {
		return gatewaytesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: int64(sequence),
			Type: eventType(event), PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload: json.RawMessage(event),
		}
	}
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-test"},
		Records: []gatewaytesting.CapturedSessionEvent{
			websocket(1, gatewaytesting.DirectionClientToServer, `{"type":"session.update","session":{"model":"gpt-test"}}`),
			websocket(2, gatewaytesting.DirectionServerToClient, `{"type":"session.created","session":{"model":"gpt-test"}}`),
			websocket(3, gatewaytesting.DirectionServerToClient, `{"type":"response.done"}`),
		},
	})
	if err != nil {
		t.Fatalf("SealSessionCapture: %v", err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal provider capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write provider capture: %v", err)
	}
}

func eventType(payload string) string {
	var value struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return ""
	}
	return value.Type
}

var _ runtimeSession.LiveRecorder = (*traceTestRecorder)(nil)
