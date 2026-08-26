package services

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestSessionConversationCollectorRecordsOrderedTurns(t *testing.T) {
	collector := &sessionConversationCollector{}
	outbound := func(msg messages.StreamMessage, inputIndex int) {
		collector.observe(msg, true, inputIndex, -1)
	}
	inbound := func(msg messages.StreamMessage, outputIndex int) {
		collector.observe(msg, false, -1, outputIndex)
	}

	outbound(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 2, 3, 4})}, 0)
	outbound(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{5, 6})}, 1)
	outbound(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, -1)
	inbound(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("ZEPHYR noted.")}, -1)
	inbound(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(" I will remember it.")}, -1)
	inbound(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x10, 0x00, 0x11, 0x00})}, 0)
	inbound(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, -1)

	log, err := sessionConversationLogJSON(collector)
	if err != nil {
		t.Fatalf("render session log: %v", err)
	}
	want := "{\"turn_index\":1,\"input\":{\"text\":\"\",\"audio_bytes\":6,\"committed\":true,\"audio_segments\":[\"audio/in-000.pcm\",\"audio/in-001.pcm\"]}," +
		"\"response\":{\"text\":\"ZEPHYR noted. I will remember it.\",\"complete\":true,\"audio_bytes\":4,\"audio_segments\":[\"audio/out-000.pcm\"]}}\n"
	if string(log) != want {
		t.Fatalf("session log = %q, want %q", log, want)
	}
}

func TestSessionConversationCollectorPrefersFullTranscriptAndFlagsPartialTurn(t *testing.T) {
	collector := &sessionConversationCollector{}
	collector.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("interim")}, false, -1, -1)
	collector.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("Hello there.")}, false, -1, -1)
	collector.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, false, -1, -1)

	// The session ends mid-turn: observed audio but no assistant MESSAGE.END.
	collector.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial reply")}, false, -1, -1)

	log, err := sessionConversationLogJSON(collector)
	if err != nil {
		t.Fatalf("render session log: %v", err)
	}
	want := "{\"turn_index\":1,\"input\":{\"text\":\"\",\"audio_bytes\":0,\"committed\":false},\"response\":{\"text\":\"Hello there.\",\"complete\":true,\"audio_bytes\":0}}\n" +
		"{\"turn_index\":2,\"input\":{\"text\":\"\",\"audio_bytes\":0,\"committed\":false},\"response\":{\"text\":\"partial reply\",\"complete\":false,\"audio_bytes\":0}}\n"
	if string(log) != want {
		t.Fatalf("session log = %q, want %q", log, want)
	}
}

func TestSessionConversationLogJSONEmptyWhenNoTurnObserved(t *testing.T) {
	log, err := sessionConversationLogJSON(&sessionConversationCollector{})
	if err != nil {
		t.Fatalf("render session log: %v", err)
	}
	if log != nil {
		t.Fatalf("session log = %q, want nil for a conversation-free recording", log)
	}
}

func TestSessionDirectoryRecordingWritesConversationSessionLog(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capture")
	plan := sessionRuntimePlan{provider: sessionProviderOpenAI}
	recording := newSessionDirectoryRecording(destination, plan, SessionRunOptions{Model: "gpt-realtime"})
	inner := newSessionRecordingTestSession()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapper := newSessionDirectoryRecordingSession(ctx, inner, recording)

	if !wrapper.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleUser,
		Value: messages.NewAudioDeltaValue([]byte{0x01, 0x00, 0x02, 0x00}),
	}) {
		t.Fatal("recording wrapper rejected input audio")
	}
	if !wrapper.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleUser,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}) {
		t.Fatal("recording wrapper rejected end-of-turn")
	}
	replies := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("ZEPHYR noted.")},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(" I will remember it.")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{0x10, 0x00})},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	for _, msg := range replies {
		if !inner.receive.Write(ctx, msg) {
			t.Fatal("test session rejected output message")
		}
	}
	for range replies {
		if _, ok := wrapper.Receive().ReadBlocking(ctx.Done()); !ok {
			t.Fatal("recording wrapper did not forward output message")
		}
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("close recording wrapper: %v", err)
	}
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}

	sessionLogBytes, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read session-log.jsonl: %v", err)
	}
	wantLine := "{\"turn_index\":1,\"input\":{\"text\":\"\",\"audio_bytes\":4,\"committed\":true,\"audio_segments\":[\"audio/in-000.pcm\"]}," +
		"\"response\":{\"text\":\"ZEPHYR noted. I will remember it.\",\"complete\":true,\"audio_bytes\":2,\"audio_segments\":[\"audio/out-000.pcm\"]}}\n"
	if string(sessionLogBytes) != wantLine {
		t.Fatalf("session-log.jsonl = %q, want %q", sessionLogBytes, wantLine)
	}

	var manifest transcript.RecordingManifest
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	listed := false
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == "session-log.jsonl" {
			listed = true
		}
	}
	if !listed {
		t.Fatal("manifest artifacts do not list session-log.jsonl")
	}
	if !bytes.Contains(manifestBytes, []byte("session-log.jsonl")) {
		t.Fatal("manifest.json does not mention session-log.jsonl")
	}
}
