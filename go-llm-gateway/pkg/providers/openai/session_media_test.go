package openai

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

func TestRealtimeSession_RTCMediaBridgesProviderAudioPath(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, logging.DummyLogger())
	session.mediaSampleRate = 24000
	owner, ok := any(session).(sharedaudio.MediaSession)
	if !ok {
		t.Fatal("OpenAI Realtime session does not expose rtc.MediaSession")
	}
	endpoints := owner.RTCMedia()
	ctx := newRealtimeTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	want := make([]int16, 720)
	for index := range want {
		want[index] = int16((index*97)%24000 - 12000) //nolint:gosec // bounded test tone
	}
	if err := endpoints.Outbound.WriteFrame(ctx, sharedaudio.PCMFrame{Samples: want}); err != nil {
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
	if got, wantBytes := encoded, codec.EncodePCM16Base64(want); got != wantBytes {
		t.Fatalf("RTC outbound PCM payload = %q, want %q", got, wantBytes)
	}

	conn.addServerEvent("response.output_audio.delta", map[string]any{
		"delta":  codec.EncodePCM16Base64(want),
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

func TestRealtimeSession_ServerVADTruncatesAtDevicePlaybackCursor(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, logging.DummyLogger())
	session.mediaSampleRate = 24000
	endpoints := session.RTCMedia()
	controlled, ok := endpoints.Inbound.(sharedaudio.PlaybackControlledInbound)
	if !ok {
		t.Fatal("OpenAI SessionMedia inbound does not expose playback control")
	}
	controller := &openAIPlaybackController{audioEndMS: 1500}
	controlled.SetPlaybackController(controller)
	ctx := newRealtimeTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	want := make([]int16, 24000*2)
	conn.addServerEvent("response.output_audio.delta", map[string]any{
		"response_id": "resp-vad", "item_id": "item-vad", "content_index": 3,
		"delta": codec.EncodePCM16Base64(want), "format": "pcm16",
	})
	frame, err := endpoints.Inbound.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("read response frame before VAD: %v", err)
	}
	response := sharedaudio.PlaybackResponse{ResponseID: "resp-vad", ItemID: "item-vad", ContentIndex: 3}
	started, _ := controller.snapshot()
	if frame.PlaybackResponse != response || started != response {
		t.Fatalf("playback response frame/controller = %+v/%+v, want %+v", frame.PlaybackResponse, started, response)
	}

	conn.addServerEvent("input_audio_buffer.speech_started", map[string]any{
		"audio_start_ms": 4200, "item_id": "item-user-vad",
	})
	clientMessage := waitForClientMessages(t, conn, 1, "device-clocked conversation truncation")[0]
	var event struct {
		Type         string `json:"type"`
		ItemID       string `json:"item_id"`
		ContentIndex int    `json:"content_index"`
		AudioEndMS   int    `json:"audio_end_ms"`
	}
	if err := json.Unmarshal(clientMessage, &event); err != nil {
		t.Fatalf("unmarshal conversation truncation: %v", err)
	}
	if event.Type != "conversation.item.truncate" || event.ItemID != "item-vad" || event.ContentIndex != 3 || event.AudioEndMS != 1500 {
		t.Fatalf("conversation truncation = %+v", event)
	}
	_, interrupted := controller.snapshot()
	if interrupted != response {
		t.Fatalf("interrupted response = %+v, want %+v", interrupted, response)
	}
}

// TestRealtimeSession_ServerVADNeverTruncatesBeyondReceivedAudio replays the
// eac13 failure at the provider boundary: OpenAI produced 2.2 seconds of
// PCM, while the local device clock claimed 3.3 seconds. The wire event must
// be capped to the audio that exists or OpenAI terminates the whole session.
func TestRealtimeSession_ServerVADNeverTruncatesBeyondReceivedAudio(t *testing.T) {
	conn := newMockWebSocketConn()
	session := newRealtimeSession(conn, logging.DummyLogger())
	session.mediaSampleRate = 24000
	endpoints := session.RTCMedia()
	controlled := endpoints.Inbound.(sharedaudio.PlaybackControlledInbound)
	controlled.SetPlaybackController(&openAIPlaybackController{audioEndMS: 3300})
	ctx := newRealtimeTestContext(t)
	session.start(ctx)
	defer func() { _ = session.Close() }()

	providerPCM := make([]int16, 24000*2200/1000)
	conn.addServerEvent("response.output_audio.delta", map[string]any{
		"response_id": "resp-eac13", "item_id": "item-eac13", "content_index": 0,
		"delta": codec.EncodePCM16Base64(providerPCM), "format": "pcm16",
	})
	if _, err := controlled.ReadFrame(ctx); err != nil {
		t.Fatalf("read eac13 response frame: %v", err)
	}

	conn.addServerEvent("input_audio_buffer.speech_started", map[string]any{
		"audio_start_ms": 25876, "item_id": "item-user-eac13",
	})
	clientMessage := waitForClientMessages(t, conn, 1, "bounded eac13 conversation truncation")[0]
	var event struct {
		Type       string `json:"type"`
		ItemID     string `json:"item_id"`
		AudioEndMS int    `json:"audio_end_ms"`
	}
	if err := json.Unmarshal(clientMessage, &event); err != nil {
		t.Fatalf("unmarshal bounded conversation truncation: %v", err)
	}
	if event.Type != "conversation.item.truncate" || event.ItemID != "item-eac13" || event.AudioEndMS != 2200 {
		t.Fatalf("bounded conversation truncation = %+v, want item-eac13 at 2200 ms", event)
	}
}

type openAIPlaybackController struct {
	mu          sync.Mutex
	started     sharedaudio.PlaybackResponse
	interrupted sharedaudio.PlaybackResponse
	audioEndMS  int
}

func (c *openAIPlaybackController) StartPlayback(response sharedaudio.PlaybackResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = response
}

func (c *openAIPlaybackController) InterruptPlayback(response sharedaudio.PlaybackResponse) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interrupted = response
	return c.audioEndMS, true
}

func (c *openAIPlaybackController) snapshot() (sharedaudio.PlaybackResponse, sharedaudio.PlaybackResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started, c.interrupted
}
