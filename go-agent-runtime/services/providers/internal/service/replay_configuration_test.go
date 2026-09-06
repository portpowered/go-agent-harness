package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestReplayConfigurationPreservesPayloadAndDeclaredRates(t *testing.T) {
	for _, tc := range []struct {
		name, audio   string
		input, output int
	}{
		{name: "legacy codec strings", audio: `"input_audio_format":"pcm16","output_audio_format":"pcm16"`},
		{name: "legacy rate objects", audio: `"input_audio_format":{"rate":16000},"output_audio_format":{"rate":24000}`, input: 16000, output: 24000},
		{name: "nested rates", audio: `"audio":{"input":{"format":{"rate":24000}},"output":{"format":{"rate":48000}}}`, input: 24000, output: 48000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"type":"session.update","session":{"model":" fixture ",` + tc.audio + `}}`)
			configuration, err := decodeReplaySessionConfiguration("fixture.json", gatewaytesting.CapturedSessionEvent{Sequence: 1, Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(configuration.payload, payload) || configuration.model != "fixture" {
				t.Fatalf("configuration changed captured handshake: %+v", configuration)
			}
			if configuration.inputAudioSampleRate != tc.input || configuration.outputAudioSampleRate != tc.output {
				t.Fatalf("rates = %d/%d, want %d/%d", configuration.inputAudioSampleRate, configuration.outputAudioSampleRate, tc.input, tc.output)
			}
		})
	}
}

func TestReplayConfigurationRejectsMalformedAudioRatherThanDefaulting(t *testing.T) {
	for _, audio := range []string{
		`"audio":42`,
		`"audio":{"input":{"format":{"rate":"invalid"}}}`,
		`"input_audio_format":42`,
		`"output_audio_format":{"rate":"invalid"}`,
	} {
		payload := []byte(`{"type":"session.update","session":{` + audio + `}}`)
		_, err := decodeReplaySessionConfiguration("broken.json", gatewaytesting.CapturedSessionEvent{Sequence: 1, Payload: payload})
		if err == nil || !strings.Contains(err.Error(), "broken.json") || !strings.Contains(err.Error(), "sequence 1") {
			t.Fatalf("malformed audio %s lost diagnostic location: %v", audio, err)
		}
	}
}

func TestReplayShimPacesInterleavedProviderEventsAndKeepsStrictWrites(t *testing.T) {
	const handshake = `{"type":"session.update","session":{"model":"captured"}}`
	capture := gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureLegacyVersion}
	for i, payload := range []string{handshake, `{"type":"session.updated"}`, `{"type":"input_audio_buffer.commit"}`} {
		direction := gatewaytesting.DirectionClientToServer
		if i == 1 {
			direction = gatewaytesting.DirectionServerToClient
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			t.Fatal(err)
		}
		capture.Records = append(capture.Records, gatewaytesting.CapturedSessionEvent{Sequence: i + 1, Type: envelope.Type, Direction: direction, PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: []byte(payload)})
	}
	inner, err := gatewaytesting.NewReplayWebSocketDialerFromCapture(capture)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &replayInitialSessionUpdateDialer{inner: inner, payload: []byte(handshake)}
	conn, err := dialer.Dial("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update","session":{"model":"local"}}`)); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- conn.WriteMessage(1, []byte(`{"type":"response.create"}`)) }()
	select {
	case err := <-result:
		t.Fatalf("outbound write raced ahead of unread provider event: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	_, payload, err := conn.ReadMessage()
	if err != nil || string(payload) != `{"type":"session.updated"}` {
		t.Fatalf("provider event = %s, %v", payload, err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("pacing bypassed strict payload validation for response.create versus captured commit")
		}
	case <-time.After(time.Second):
		t.Fatal("write remained blocked after provider event consumed")
	}
}
