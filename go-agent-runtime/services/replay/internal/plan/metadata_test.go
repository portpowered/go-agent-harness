package plan

import (
	"encoding/json"
	"strings"
	"testing"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestReplayHasSessionUpdatedOnlyBeforeFirstClientAction(t *testing.T) {
	tests := []struct {
		name    string
		records []gatewaytesting.CapturedSessionEvent
		want    bool
	}{
		{
			name: "handshake update follows session configuration",
			records: []gatewaytesting.CapturedSessionEvent{
				{Direction: gatewaytesting.DirectionClientToServer, Type: "session.update"},
				{Direction: gatewaytesting.DirectionServerToClient, Type: "session.updated"},
				{Direction: gatewaytesting.DirectionClientToServer, Type: "input_audio_buffer.append"},
			},
			want: true,
		},
		{
			name: "late update is not an admission barrier",
			records: []gatewaytesting.CapturedSessionEvent{
				{Direction: gatewaytesting.DirectionClientToServer, Type: "session.update"},
				{Direction: gatewaytesting.DirectionClientToServer, Type: "input_audio_buffer.append"},
				{Direction: gatewaytesting.DirectionServerToClient, Type: "session.updated"},
			},
			want: false,
		},
		{
			name: "no client action still waits for handshake",
			records: []gatewaytesting.CapturedSessionEvent{
				{Direction: gatewaytesting.DirectionClientToServer, Type: "session.update"},
				{Direction: gatewaytesting.DirectionServerToClient, Type: "session.updated"},
			},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := replayHasSessionUpdated(test.records); got != test.want {
				t.Fatalf("replayHasSessionUpdated() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestReplayAudioSampleRates(t *testing.T) {
	records := []gatewaytesting.CapturedSessionEvent{{
		Direction: gatewaytesting.DirectionClientToServer,
		Type:      "session.update",
		Payload:   []byte(`{"type":"session.update","session":{"audio":{"input":{"format":{"rate":24000}},"output":{"format":{"rate":16000}}}}}`),
	}}
	input, output, err := replayAudioSampleRates(records)
	if err != nil {
		t.Fatal(err)
	}
	if input != 24000 || output != 16000 {
		t.Fatalf("replayAudioSampleRates() = %d/%d, want 24000/16000", input, output)
	}
}

func TestReplaySessionAudioSampleRatesSupportsLegacyFormats(t *testing.T) {
	input, output, err := replaySessionAudioSampleRates(map[string]json.RawMessage{
		"input_audio_format":  json.RawMessage(`{"rate":16000}`),
		"output_audio_format": json.RawMessage(`"pcm16"`),
	})
	if err != nil {
		t.Fatalf("legacy sample rates: %v", err)
	}
	if input != 16000 || output != 0 {
		t.Fatalf("legacy sample rates = %d/%d, want 16000/0", input, output)
	}
	if input, output, err := replaySessionAudioSampleRates(map[string]json.RawMessage{
		"input_audio_format":  json.RawMessage("null"),
		"output_audio_format": json.RawMessage("null"),
	}); err != nil || input != 0 || output != 0 {
		t.Fatalf("empty legacy sample rates = %d/%d, %v", input, output, err)
	}
	if _, _, err := replaySessionAudioSampleRates(map[string]json.RawMessage{
		"input_audio_format": json.RawMessage(`{"rate":`),
	}); err == nil || !strings.Contains(err.Error(), "decode input audio format") {
		t.Fatalf("malformed legacy sample rate error = %v", err)
	}
}

func TestReplayHasInterruptionReplacement(t *testing.T) {
	tests := []struct {
		name    string
		records []gatewaytesting.CapturedSessionEvent
		want    bool
	}{
		{
			name: "distinct response follows cancellation",
			records: []gatewaytesting.CapturedSessionEvent{
				{Direction: gatewaytesting.DirectionServerToClient, Type: "response.done", Payload: []byte(`{"response":{"id":"response-old","status":"cancelled"}}`)},
				{Direction: gatewaytesting.DirectionServerToClient, Type: "response.created", Payload: []byte(`{"response":{"id":"response-new"}}`)},
			},
			want: true,
		},
		{
			name: "cancelled response has no replacement",
			records: []gatewaytesting.CapturedSessionEvent{
				{Direction: gatewaytesting.DirectionServerToClient, Type: "response.created", Payload: []byte(`{"response":{"id":"response-old"}}`)},
				{Direction: gatewaytesting.DirectionServerToClient, Type: "response.done", Payload: []byte(`{"response":{"id":"response-old","status":"cancelled"}}`)},
			},
			want: false,
		},
		{
			name: "same response is not a replacement",
			records: []gatewaytesting.CapturedSessionEvent{
				{Direction: gatewaytesting.DirectionServerToClient, Type: "response.done", Payload: []byte(`{"response":{"id":"response-old","status":"canceled"}}`)},
				{Direction: gatewaytesting.DirectionServerToClient, Type: "response.created", Payload: []byte(`{"response":{"id":"response-old"}}`)},
			},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := replayHasInterruptionReplacement(test.records); got != test.want {
				t.Fatalf("replayHasInterruptionReplacement() = %t, want %t", got, test.want)
			}
		})
	}
}
