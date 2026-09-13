package livehost

import (
	"context"
	"errors"
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
)

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
