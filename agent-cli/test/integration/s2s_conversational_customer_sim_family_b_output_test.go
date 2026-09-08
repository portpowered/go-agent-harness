package integration

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

func (f *familyBProviderFixture) sendOriginalOutput(connection *websocket.Conn) error {
	// Complete the response that owns the original tool call first. The
	// session is explicitly held open below so the correction can target a
	// distinct assistant response without leaving the tool continuation
	// observer in a partial state.
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-original-tool-continuation"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.output_audio.delta", "response_id": "response-original-tool-continuation", "delta": base64.StdEncoding.EncodeToString([]byte{9, 0x42, 0x52, 0x42}), "format": "pcm16",
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":        "response.output_audio.done",
		"response_id": "response-original-tool-continuation",
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": "response-original-tool-continuation", "status": "completed"},
	}); err != nil {
		return err
	}

	f.mu.Lock()
	startedAt := f.elapsedLocked()
	f.originalOutputStarted = startedAt
	f.activeResponse = "response-original-output"
	f.mu.Unlock()
	if err := f.send(connection, map[string]string{"type": "input_audio_buffer.speech_started"}); err != nil {
		return err
	}
	text := "Created draft/brief.md and kept the original draft while I explained the next step."
	f.recordProductTranscript(probe.TranscriptEvent{ID: "product-turn-1", TurnID: "turn-1", Speaker: probe.TranscriptProduct, Text: text, At: startedAt, Final: true})
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-original-output"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.delta", "response_id": "response-original-output", "delta": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "response_id": "response-original-output", "transcript": text}); err != nil {
		return err
	}
	audio := []byte{1, 0x42, 0x52, 0x42}
	return f.send(connection, map[string]any{
		"type": "response.output_audio.delta", "response_id": "response-original-output", "delta": base64.StdEncoding.EncodeToString(audio), "format": "pcm16",
	})
}

func (f *familyBProviderFixture) finishCancelledOriginalResponse(connection *websocket.Conn) error {
	f.mu.Lock()
	responseID := f.activeResponse
	endedAt := f.elapsedLocked()
	f.originalOutputEnded = endedAt
	f.activeResponse = ""
	f.cancelPending = false
	f.mu.Unlock()
	if err := f.send(connection, map[string]any{
		"type":        "response.output_audio.done",
		"response_id": responseID,
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": responseID, "status": "cancelled"},
	}); err != nil {
		return err
	}
	f.recordResponseTerminal("cancelled")
	return nil
}

func (f *familyBProviderFixture) sendReplacementOutput(connection *websocket.Conn) error {
	f.mu.Lock()
	startedAt := f.elapsedLocked()
	f.replacementOutputStarted = startedAt
	f.mu.Unlock()
	text := "Created final/brief.md as the corrected release note."
	f.recordProductTranscript(probe.TranscriptEvent{ID: "product-turn-2", TurnID: "turn-2", Speaker: probe.TranscriptProduct, Text: text, At: startedAt, Final: true})
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-replacement-output"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.delta", "response_id": "response-replacement-output", "delta": text}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio_transcript.done", "response_id": "response-replacement-output", "transcript": text}); err != nil {
		return err
	}
	audio := []byte{2, 0x42, 0x52, 0x42}
	if err := f.send(connection, map[string]any{
		"type": "response.output_audio.delta", "response_id": "response-replacement-output", "delta": base64.StdEncoding.EncodeToString(audio), "format": "pcm16",
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]string{"type": "response.output_audio.done", "response_id": "response-replacement-output"}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": "response-replacement-output", "status": "completed"},
	}); err != nil {
		return err
	}
	f.mu.Lock()
	f.replacementOutputEnded = f.elapsedLocked()
	f.mu.Unlock()
	f.recordResponseTerminal("completed")
	time.Sleep(25 * time.Millisecond)
	return f.send(connection, map[string]string{"type": "session.closed", "reason": "family_b_correction_complete"})
}

func (f *familyBProviderFixture) sendSessionReady(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{
		"type":    "session.created",
		"session": map[string]string{"id": "family-b", "model": "gpt-realtime"},
	}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{
		"type":    "session.updated",
		"session": map[string]string{"id": "family-b"},
	})
}

func (f *familyBProviderFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *familyBProviderFixture) recordCustomerTranscript(event probe.TranscriptEvent) {
	f.mu.Lock()
	f.customerTranscript = append(f.customerTranscript, event)
	f.mu.Unlock()
}

func (f *familyBProviderFixture) recordProductTranscript(event probe.TranscriptEvent) {
	f.mu.Lock()
	f.productTranscript = append(f.productTranscript, event)
	f.mu.Unlock()
}

func (f *familyBProviderFixture) recordResponseTerminal(status string) {
	f.mu.Lock()
	f.responseTerminalStatuses = append(f.responseTerminalStatuses, status)
	f.mu.Unlock()
}

func (f *familyBProviderFixture) elapsedLocked() time.Duration {
	if f.startedAt.IsZero() {
		return 0
	}
	return time.Since(f.startedAt)
}

func (f *familyBProviderFixture) failProtocol(message string) {
	f.mu.Lock()
	if f.protocolError == "" {
		f.protocolError = message
	}
	f.mu.Unlock()
}

func familyBToolArguments(path, content string) string {
	data, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		panic("marshal Family B tool arguments: " + err.Error())
	}
	return string(data)
}

func familyBFrame(seed byte) []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = seed
	}
	return frame
}

func familyBSilent(audio []byte) bool {
	if len(audio) == 0 {
		return true
	}
	for _, value := range audio {
		if value != 0 {
			return false
		}
	}
	return true
}
