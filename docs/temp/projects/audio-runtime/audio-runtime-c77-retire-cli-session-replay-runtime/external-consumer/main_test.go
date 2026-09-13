package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
)

const (
	clientToServer = "client_to_server"
	serverToClient = "server_to_client"
	websocket      = "websocket_message"

	sessionUpdate   = "session.update"
	conversationAdd = "conversation.item.create"
	responseCreate  = "response.create"
	audioAppend     = "input_audio_buffer.append"
	audioCommit     = "input_audio_buffer.commit"
)

type capturedEvent struct {
	Sequence    int             `json:"sequence"`
	Direction   string          `json:"direction"`
	TimestampMs int64           `json:"timestamp_ms"`
	Type        string          `json:"type"`
	PayloadType string          `json:"payload_type"`
	Payload     json.RawMessage `json:"payload"`
}

func TestPublicReplayConsumerConstructsWireAndLoadsTextAndAudio(t *testing.T) {
	service := wire.NewService()
	if service == nil {
		t.Fatal("replay Wire returned a nil service")
	}

	textPath := writeCapture(t, []capturedEvent{
		event(1, clientToServer, sessionUpdate, 0, `{"type":"session.update","session":{"model":"fixture-model","tools":[{"name":"first"},{"name":"second"}],"input_audio_format":{"rate":24000},"output_audio_format":{"rate":24000}}}`),
		event(2, serverToClient, "session.updated", 1, `{"type":"session.updated"}`),
		event(3, clientToServer, conversationAdd, 2, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"external text"}]}}`),
		event(4, clientToServer, responseCreate, 3, `{"type":"response.create"}`),
		event(5, serverToClient, "session.closed", 7, `{"type":"session.closed"}`),
	})
	configuration, err := service.LoadSessionConfiguration(t.Context(), textPath)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Model() != "fixture-model" || configuration.InputAudioSampleRate() != 24000 || configuration.OutputAudioSampleRate() != 24000 {
		t.Fatalf("configuration = model %q rates %d/%d", configuration.Model(), configuration.InputAudioSampleRate(), configuration.OutputAudioSampleRate())
	}
	if !configuration.InitialToolsKnown() || len(configuration.InitialToolNames()) != 2 {
		t.Fatalf("configuration tools = %v", configuration.InitialToolNames())
	}
	prompt, err := service.LoadCapturedTextPrompt(t.Context(), textPath)
	if err != nil || prompt == nil || prompt.Text != "external text" {
		t.Fatalf("prompt = %#v, error = %v", prompt, err)
	}
	textPlan, err := service.LoadLivePlan(t.Context(), textPath)
	if err != nil || !textPlan.WaitForSessionUpdated {
		t.Fatalf("text plan = %#v, error = %v", textPlan, err)
	}

	audioPath := writeCapture(t, []capturedEvent{
		event(1, clientToServer, sessionUpdate, 0, `{"type":"session.update","session":{"model":"fixture-model","audio":{"input":{"format":{"rate":16000}},"output":{"format":{"rate":16000}}}}}`),
		event(2, serverToClient, "session.updated", 1, `{"type":"session.updated"}`),
		event(3, clientToServer, audioAppend, 2, `{"type":"input_audio_buffer.append","audio":"AQACAA=="}`),
		event(4, clientToServer, audioCommit, 3, `{"type":"input_audio_buffer.commit"}`),
		event(5, clientToServer, responseCreate, 4, `{"type":"response.create"}`),
		event(6, serverToClient, "session.closed", 8, `{"type":"session.closed"}`),
	})
	turns, err := service.LoadCapturedAudioTurns(t.Context(), audioPath)
	if err != nil || len(turns) != 1 || string(turns[0].PCM) != string([]byte{1, 0, 2, 0}) {
		t.Fatalf("audio turns = %#v, error = %v", turns, err)
	}
	audioPlan, err := service.LoadLivePlan(t.Context(), audioPath)
	if err != nil || audioPlan.InputAudioSampleRate != 16000 || audioPlan.OutputAudioSampleRate != 16000 {
		t.Fatalf("audio plan = %#v, error = %v", audioPlan, err)
	}
}

func TestPublicReplayConsumerUsesDeterministicTimingAndTypedFailures(t *testing.T) {
	service := wire.NewService()
	path := writeCapture(t, []capturedEvent{
		event(1, clientToServer, sessionUpdate, 10, `{"type":"session.update","session":{"model":"fixture"}}`),
		event(2, serverToClient, "session.closed", 17, `{"type":"session.closed"}`),
	})
	first, err := service.ReplayDuration(t.Context(), path, "recorded")
	if err != nil || first != 3*time.Second+7*time.Millisecond {
		t.Fatalf("first duration = %v, error = %v", first, err)
	}
	second, err := service.ReplayDuration(t.Context(), path, "recorded")
	if err != nil || second != first {
		t.Fatalf("second duration = %v, error = %v, first = %v", second, err, first)
	}
	hasClose, err := service.HasEvent(t.Context(), path, "session.closed")
	if err != nil || !hasClose {
		t.Fatalf("HasEvent = %t, error = %v", hasClose, err)
	}

	conflictPath := writeCapture(t, []capturedEvent{
		event(1, clientToServer, sessionUpdate, 0, `{"type":"session.update","session":{"audio":{"input":{"format":{"rate":16000}},"output":{"format":{"rate":24000}}}}}`),
	})
	if _, err := service.LoadSessionConfiguration(t.Context(), conflictPath); !errors.Is(err, replay.ErrSessionAudioSampleRateConflict) {
		t.Fatalf("conflict error = %v, want replay.ErrSessionAudioSampleRateConflict", err)
	}

	cause := errors.New("consumer cancellation")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if err := service.ReplayCapture(ctx, path, nil); !errors.Is(err, cause) {
		t.Fatalf("cancelled drain error = %v, want %v", err, cause)
	}
}

func TestPublicReplayConsumerReport(t *testing.T) {
	result, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Schema != "audio-runtime-c77-replay-consumer/v1" || result.ConstructedVia != "replay/wire.NewService" {
		t.Fatalf("report = %#v", result)
	}
}

func event(sequence int, direction, eventType string, timestamp int64, payload string) capturedEvent {
	return capturedEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: timestamp,
		Type:        eventType,
		PayloadType: websocket,
		Payload:     json.RawMessage(payload),
	}
}

func writeCapture(t *testing.T, records []capturedEvent) string {
	t.Helper()
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "capture.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
