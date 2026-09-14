package consumer_test

import (
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog"
	conversationlogwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog/wire"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

func TestPublicConversationLogConsumer(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	service := conversationlogwire.NewService(clock)

	// Invalid/nil payloads are ignored without requiring private constructors,
	// CLI state, a provider, a device, or any process-global initialization.
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta}, false, -1, -1)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser}, false, -1, -1)

	service.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue("typed")}, true, -1, -1)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleUser, Value: messages.NewAudioDeltaValue([]byte{1, 2, 3})}, true, 0, -1)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, true, -1, -1)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeInputItemAdded, Role: messages.RoleUser, Value: messages.NewInputItemAddedValue("consumer-item")}, false, -1, -1)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValueForItem("spoken consumer input", "consumer-item")}, false, -1, -1)

	call := messages.ToolCall{ID: "consumer-call", Name: "screen", Arguments: `{"target":"tab-1"}`}
	service.ObserveToolCall(call)
	service.ObserveToolResult(call, messages.ToolCallResponse{Content: `{"ok":true}`}, false, &conversationlog.ImageEvidence{
		Path:            "images/consumer.png",
		Source:          "browser",
		BrowserID:       "browser-1",
		TargetID:        "tab-1",
		MIMEType:        "image/png",
		ByteLength:      4,
		Width:           2,
		Height:          2,
		SHA256:          "consumer-hash",
		TypedProjection: "screen",
	})
	failedCall := messages.ToolCall{ID: "consumer-failed", Name: "screen"}
	service.ObserveToolCall(failedCall)
	service.ObserveToolResult(failedCall, messages.ToolCallResponse{}, true)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("delta")}, false, -1, -1)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("full response")}, false, -1, -1)
	clock.now = clock.now.Add(1200 * time.Millisecond)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{9, 8})}, false, -1, 0)
	service.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, false, -1, -1)

	log, err := service.JSONL()
	if err != nil {
		t.Fatalf("JSONL: %v", err)
	}
	want := "{\"turn_index\":1,\"input\":{\"text\":\"spoken consumer input\",\"audio_bytes\":3,\"committed\":true,\"audio_segments\":[\"audio/in-000.pcm\"]}," +
		"\"response\":{\"text\":\"full response\",\"complete\":true,\"audio_bytes\":2,\"audio_segments\":[\"audio/out-000.pcm\"]},\"tool_events\":[" +
		"{\"sequence\":1,\"type\":\"tool_call\",\"tool_call_id\":\"consumer-call\",\"tool_name\":\"screen\",\"arguments\":\"{\\\"target\\\":\\\"tab-1\\\"}\"}," +
		"{\"sequence\":2,\"type\":\"tool_result\",\"tool_call_id\":\"consumer-call\",\"tool_name\":\"screen\",\"status\":\"completed\",\"content\":\"{\\\"ok\\\":true}\",\"image\":{\"path\":\"images/consumer.png\",\"source\":\"browser\",\"browser_id\":\"browser-1\",\"target_id\":\"tab-1\",\"mime_type\":\"image/png\",\"byte_length\":4,\"width\":2,\"height\":2,\"sha256\":\"consumer-hash\",\"typed_projection\":\"screen\"}}," +
		"{\"sequence\":3,\"type\":\"tool_call\",\"tool_call_id\":\"consumer-failed\",\"tool_name\":\"screen\",\"arguments\":\"\"}," +
		"{\"sequence\":4,\"type\":\"tool_result\",\"tool_call_id\":\"consumer-failed\",\"tool_name\":\"screen\",\"status\":\"failed\",\"content\":\"\"}]}\n"
	if string(log) != want {
		t.Fatalf("JSONL = %q, want %q", log, want)
	}
	if strings.Contains(string(log), "time") || strings.Contains(string(log), "consumer-hash-bytes") {
		t.Fatalf("run-specific or pixel data leaked into JSONL: %s", log)
	}
	timing := service.TimingEntries()
	if len(timing) != 1 || timing[0].CommittedAt != "2026-09-01T00:00:00Z" || timing[0].FirstResponseAudioMS != 1200 {
		t.Fatalf("timing = %+v", timing)
	}

	// A completed public call has no background work to drain. Calling all
	// snapshot methods once more is the consumer's explicit shutdown boundary.
	if _, err := service.JSONL(); err != nil {
		t.Fatalf("shutdown snapshot: %v", err)
	}
}
