package testing

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestNewSessionReplayerFromBytesReplaysProtectedCaptureBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bytes.session.json")
	writeCapture(t, path, []CapturedSessionEvent{
		makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("hello")),
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replayer, err := NewSessionReplayerFromBytes(t.Context(), data)
	if err != nil {
		t.Fatalf("NewSessionReplayerFromBytes: %v", err)
	}
	t.Cleanup(func() {
		if err := replayer.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if !replayer.Send(t.Context(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")}) {
		t.Fatal("replayer rejected the recorded client event")
	}
	if _, err := NewSessionReplayerFromBytes(t.Context(), []byte("{")); err == nil {
		t.Fatal("NewSessionReplayerFromBytes accepted malformed JSON")
	}
}

func TestReplaySessionInferencerConnectsAReplayerPerSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inferencer.session.json")
	writeCapture(t, path, []CapturedSessionEvent{
		makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("hi")),
	})
	session, err := NewReplaySessionInferencer(path).ConnectSession(t.Context())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := NewReplaySessionInferencer(filepath.Join(t.TempDir(), "missing.json")).ConnectSession(t.Context()); err == nil {
		t.Fatal("ConnectSession succeeded for a missing capture")
	}
}

func TestLoadSessionCaptureUnverifiedReadsWithoutIntegrityChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unverified.session.json")
	writeCapture(t, path, []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("x")),
	})
	capture, err := LoadSessionCaptureUnverified(path)
	if err != nil || len(capture.Records) != 1 {
		t.Fatalf("LoadSessionCaptureUnverified() = %d records, %v", len(capture.Records), err)
	}
	if _, err := LoadSessionCaptureUnverified(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("LoadSessionCaptureUnverified read a missing file")
	}
}

func TestRunSessionReplayProbeFromCaptureMatchesTheFileProbe(t *testing.T) {
	fixture := SharedSessionFixturePath("session_healthy_multiturn_audio.session.json")
	capture, err := LoadSessionCapture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := RunSessionReplayProbe(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	inMemory, err := RunSessionReplayProbeFromCapture(context.Background(), capture)
	if err != nil {
		t.Fatalf("RunSessionReplayProbeFromCapture: %v", err)
	}
	if inMemory.InboundFrames != fromFile.InboundFrames || inMemory.OutboundTicks != fromFile.OutboundTicks {
		t.Fatalf("in-memory probe %d/%d frames/ticks, file probe %d/%d",
			inMemory.InboundFrames, inMemory.OutboundTicks, fromFile.InboundFrames, fromFile.OutboundTicks)
	}
	if _, err := RunSessionReplayProbeFromCapture(context.Background(), SessionCapture{}); err == nil {
		t.Fatal("RunSessionReplayProbeFromCapture accepted an empty capture")
	}
}
