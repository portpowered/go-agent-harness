package plan

import (
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
