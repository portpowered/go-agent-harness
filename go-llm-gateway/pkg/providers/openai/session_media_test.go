package openai

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestRealtimeSession_RTCMediaBridgesProviderAudioPath(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, logging.DummyLogger())
	owner, ok := any(session).(rtc.MediaSession)
	if !ok {
		t.Fatal("OpenAI Realtime session does not expose rtc.MediaSession")
	}
	endpoints := owner.RTCMedia()
	ctx := newRealtimeTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	want := make([]int16, rtc.DefaultSessionMediaFrameSamples)
	for index := range want {
		want[index] = int16((index*97)%24000 - 12000) //nolint:gosec // bounded test tone
	}
	if err := endpoints.Outbound.WriteFrame(ctx, rtc.PCMFrame{Samples: want}); err != nil {
		t.Fatalf("write RTC outbound frame: %v", err)
	}

	clientMessage := waitForClientMessages(t, conn, 1, "RTC outbound audio event")[0]
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(clientMessage, &wire); err != nil {
		t.Fatalf("unmarshal RTC outbound event: %v", err)
	}
	var eventType, encoded string
	if err := json.Unmarshal(wire["type"], &eventType); err != nil {
		t.Fatalf("unmarshal RTC outbound event type: %v", err)
	}
	if err := json.Unmarshal(wire["audio"], &encoded); err != nil {
		t.Fatalf("unmarshal RTC outbound audio: %v", err)
	}
	if eventType != "input_audio_buffer.append" {
		t.Fatalf("RTC outbound event type = %q, want input_audio_buffer.append", eventType)
	}
	if got, wantBytes := encoded, base64.StdEncoding.EncodeToString(encodePCM16(want)); got != wantBytes {
		t.Fatalf("RTC outbound PCM payload = %q, want %q", got, wantBytes)
	}

	conn.addServerEvent("response.output_audio.delta", map[string]any{
		"delta":  base64.StdEncoding.EncodeToString(encodePCM16(want)),
		"format": "pcm16",
	})
	conn.addServerEvent("response.output_audio.done", nil)
	got, err := endpoints.Inbound.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("read RTC inbound frame: %v", err)
	}
	if !reflect.DeepEqual(got.Samples, want) {
		t.Fatalf("RTC inbound PCM frame differs from provider audio: got %d samples", len(got.Samples))
	}
}
