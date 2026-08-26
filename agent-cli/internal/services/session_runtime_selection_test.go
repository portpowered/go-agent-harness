package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestResolveSessionRuntimeSelection_DefaultsToWebSocket(t *testing.T) {
	selection, err := resolveSessionRuntimeSelection(SessionRunOptions{})
	if err != nil {
		t.Fatalf("resolveSessionRuntimeSelection: %v", err)
	}
	if selection != (SessionRuntimeSelection{Transport: SessionTransportWebSocket}) {
		t.Fatalf("selection = %#v, want the WebSocket default", selection)
	}
}

func TestPlanSessionRuntime_RetainsExactWebRTCSelection(t *testing.T) {
	const signaling = " loopback://sentinel/signaling?token=exact "
	const media = "rtsp://fixture:secret@sentinel.example/camera/main"

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		ReplayPath:        "synthetic.session.json",
		SessionInferencer: &selectionTestInferencer{},
		Transport:         " WebRTC ",
		SignalingEndpoint: signaling,
		MediaSource:       media,
	}, sessionRuntimeFactory{})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}
	if plan.transport != SessionTransportWebRTC {
		t.Fatalf("plan.transport = %q, want %q", plan.transport, SessionTransportWebRTC)
	}
	if plan.signalingEndpoint != signaling {
		t.Fatalf("plan.signalingEndpoint = %q, want exact %q", plan.signalingEndpoint, signaling)
	}
	if plan.mediaSource != media {
		t.Fatalf("plan.mediaSource = %q, want exact input", plan.mediaSource)
	}
	if plan.selection != (SessionRuntimeSelection{
		Transport:         SessionTransportWebRTC,
		SignalingEndpoint: signaling,
		MediaSource:       media,
	}) {
		t.Fatalf("plan.selection = %#v, want exact selection", plan.selection)
	}
}

func TestPlanSessionRuntime_InvalidSelectionFailsBeforeFactorySideEffects(t *testing.T) {
	cases := []struct {
		name   string
		opts   SessionRunOptions
		fields []string
		cause  error
	}{
		{
			name:   "unknown transport",
			opts:   SessionRunOptions{Transport: "quic", RecordPath: filepath.Join(t.TempDir(), "capture.json")},
			fields: []string{"transport"},
			cause:  ErrInvalidSessionTransport,
		},
		{
			name:   "signaling on websocket",
			opts:   SessionRunOptions{Signaling: "loopback", RecordPath: filepath.Join(t.TempDir(), "capture.json")},
			fields: []string{"transport", "signaling"},
			cause:  ErrSessionSignalingRequiresWebRTC,
		},
		{
			name:   "media on websocket",
			opts:   SessionRunOptions{MediaSource: "fixture", RecordPath: filepath.Join(t.TempDir(), "capture.json")},
			fields: []string{"transport", "media-source"},
			cause:  ErrSessionMediaSourceRequiresWebRTC,
		},
		{
			name:   "webrtc without signaling or media",
			opts:   SessionRunOptions{Transport: SessionTransportWebRTC, RecordPath: filepath.Join(t.TempDir(), "capture.json")},
			fields: []string{"transport", "signaling", "media-source"},
			cause:  ErrSessionWebRTCRequiresSignaling,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			factoryCalls := 0
			factory := sessionRuntimeFactory{
				newDefaultLiveDialer: func() transport.Dialer {
					factoryCalls++
					return nil
				},
				newRecordingDialer: func(transport.Dialer, string, string) sessionRecordingDialer {
					factoryCalls++
					return nil
				},
				newReplayDialer: func(string) (sessionReplayDialer, error) {
					factoryCalls++
					return nil, nil
				},
				newReplayInferencer: func(string) messages.SessionInferencer {
					factoryCalls++
					return nil
				},
			}

			_, err := planSessionRuntimeWithFactory(testCase.opts, factory)
			if err == nil {
				t.Fatal("invalid runtime selection returned nil")
			}
			var selectionErr *SessionRuntimeSelectionError
			if !errors.As(err, &selectionErr) {
				t.Fatalf("error type = %T, want *SessionRuntimeSelectionError: %v", err, err)
			}
			for _, field := range testCase.fields {
				found := false
				for _, got := range selectionErr.Fields {
					if got == field {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("selection fields = %v, missing %q", selectionErr.Fields, field)
				}
			}
			if !errors.Is(err, testCase.cause) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, testCase.cause)
			}
			if factoryCalls != 0 {
				t.Fatalf("factory calls = %d, want 0 before selection rejection", factoryCalls)
			}
		})
	}
}

func TestRunSession_InvalidRTCSelectionDoesNotMutateCapturePath(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "rejected.session.json")
	err := RunSession(context.Background(), os.Stdout, SessionRunOptions{
		RecordPath: recordPath,
		Transport:  SessionTransportWebRTC,
		Signaling:  "loopback",
	})
	if err == nil {
		t.Fatal("invalid WebRTC selection returned nil")
	}
	if _, statErr := os.Stat(recordPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected selection touched capture path: stat error = %v", statErr)
	}
}

type selectionTestInferencer struct{}

func (*selectionTestInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("selection test inferencer must not connect")
}
