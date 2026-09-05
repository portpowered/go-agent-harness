package agentruntime

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

func TestProviderWireTraceRedactsPayloadWithoutChangingAudio(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "trace")
	trace, err := recording.NewTrace(directory, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("wire-secret")
	wirePayload := append([]byte(`{"type":"response.create","token":"`), secret...)
	wirePayload = append(wirePayload, '"', '}')
	audioPayload := append([]byte("pcm:"), secret...)
	observer := TraceRuntimeObserver{Trace: trace, Redactions: []string{string(secret)}}
	observer.ObserveSessionRuntime(SessionRuntimeObservation{Kind: "provider_wire_send", Payload: wirePayload})
	observer.ObserveSessionRuntime(SessionRuntimeObservation{Kind: SessionRuntimeObservationAudioOutput, Payload: audioPayload})
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wirePayload, secret) || !bytes.Contains(audioPayload, secret) {
		t.Fatal("observer mutated caller payload")
	}

	events := readTraceEvents(t, filepath.Join(directory, "timeline.jsonl"))
	var wire, audio *recording.Event
	for index := range events {
		event := &events[index]
		switch event.RuntimeKind {
		case "provider_wire_send":
			wire = event
		case "audio_output":
			audio = event
		}
	}
	if wire == nil || bytes.Contains(wire.Payload, secret) || !bytes.Contains(wire.Payload, []byte("[REDACTED]")) {
		if wire == nil {
			t.Fatal("wire event missing")
		}
		t.Fatalf("wire payload was not redacted: %q", wire.Payload)
	}
	if audio == nil || !bytes.Equal(audio.Payload, audioPayload) {
		if audio == nil {
			t.Fatal("audio event missing")
		}
		t.Fatalf("audio payload changed: %q", audio.Payload)
	}
}
