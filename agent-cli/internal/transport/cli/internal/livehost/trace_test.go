package livehost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
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

func TestTraceRequiredFailsClosedWithoutPublicService(t *testing.T) {
	for _, test := range []struct {
		name       string
		traceAudio bool
	}{
		{name: "trace audio", traceAudio: true},
		{name: "record directory", traceAudio: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			probe := &traceHostProbe{}
			recording := &traceRecordingProbe{}
			request := serviceSession.Request{TraceAudio: test.traceAudio, RecordDirectory: filepath.Join(root, "bundle")}

			err := Run(context.Background(), nil, request, Dependencies{
				LiveService: probe,
				BuildRequest: func(context.Context, serviceSession.Request, *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error) {
					probe.built.Add(1)
					return runtimeSession.LiveRequest{SessionID: "trace-required", OutputAudioSampleRate: 16_000}, nil
				},
				RecordingService: recording,
				CredentialValues: func(serviceSession.Request) ([]string, error) {
					recording.credentials.Add(1)
					return nil, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), "live session trace service is unavailable") {
				t.Fatalf("Run error = %v, want fail-closed public trace-service diagnostic", err)
			}
			if got := probe.built.Load(); got != 0 {
				t.Fatalf("request builder calls = %d, want 0 before trace admission", got)
			}
			if got := probe.runs.Load(); got != 0 {
				t.Fatalf("live runner calls = %d, want 0 before trace admission", got)
			}
			if got := recording.opened.Load(); got != 0 {
				t.Fatalf("recording opens = %d, want 0 before trace admission", got)
			}
			if got := recording.credentials.Load(); got != 0 {
				t.Fatalf("credential resolutions = %d, want 0 before trace admission", got)
			}
			if _, statErr := os.Stat(request.RecordDirectory); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("record directory side effect: stat error = %v", statErr)
			}
		})
	}
}

func TestTraceFailClosedKeepsTraceDisabledBehavior(t *testing.T) {
	wantErr := errors.New("trace-disabled runner result")
	probe := &traceHostProbe{runErr: wantErr}
	err := Run(context.Background(), nil, serviceSession.Request{}, Dependencies{
		LiveService: probe,
		BuildRequest: func(context.Context, serviceSession.Request, *runtimeReplay.CaptureInspection) (runtimeSession.LiveRequest, error) {
			probe.built.Add(1)
			return runtimeSession.LiveRequest{SessionID: "trace-disabled", OutputAudioSampleRate: 16_000}, nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if got := probe.built.Load(); got != 1 {
		t.Fatalf("request builder calls = %d, want 1", got)
	}
	if got := probe.runs.Load(); got != 1 {
		t.Fatalf("live runner calls = %d, want 1", got)
	}
}

type traceHostProbe struct {
	built  atomic.Int32
	runs   atomic.Int32
	runErr error
}

func (p *traceHostProbe) OpenLive(context.Context, runtimeSession.LiveRequest) (runtimeSession.LiveHandle, error) {
	return nil, errors.New("trace host probe does not open handles")
}

func (p *traceHostProbe) RunLive(context.Context, runtimeSession.LiveRunOptions) error {
	p.runs.Add(1)
	return p.runErr
}

type traceRecordingProbe struct {
	opened      atomic.Int32
	credentials atomic.Int32
}

func (p *traceRecordingProbe) TrackSession(messages.SessionInferencer, runtimeRecording.Writer, string) (runtimeRecording.SessionCapture, error) {
	return nil, nil
}

func (p *traceRecordingProbe) OpenLiveEvidence(runtimeRecording.LiveEvidenceOptions) (runtimeSession.LiveRecorder, error) {
	p.opened.Add(1)
	return &traceRecorderProbe{}, nil
}

func (p *traceRecordingProbe) OpenLiveSemanticEvidence(string) (runtimeSession.LiveRecorder, error) {
	p.opened.Add(1)
	return &traceRecorderProbe{}, nil
}

type traceRecorderProbe struct{}

func (*traceRecorderProbe) RecordMessage(context.Context, runtimeSession.LiveRecord) error {
	return nil
}

func (*traceRecorderProbe) RecordAudio(context.Context, runtimeSession.LiveAudioRecord) error {
	return nil
}

func (*traceRecorderProbe) RecordEvent(context.Context, runtimeSession.LiveEvent) error {
	return nil
}

func (*traceRecorderProbe) Finalize(context.Context, error) error { return nil }

var _ runtimeSession.LiveService = (*traceHostProbe)(nil)
var _ runtimeSession.LiveRunner = (*traceHostProbe)(nil)
var _ runtimeRecording.Service = (*traceRecordingProbe)(nil)
var _ runtimeSession.LiveRecorder = (*traceRecorderProbe)(nil)
