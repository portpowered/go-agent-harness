package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

const (
	familyBOriginalCallID      = "call-family-b-original"
	familyBAuthorizationHeader = "Bearer hermetic-key"
	familyBResponseCancelEvent = "response.cancel"
	familyBSessionUpdateEvent  = "session.update"
)

func (f *familyBProviderFixture) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != familyBAuthorizationHeader {
		f.failProtocol("authorization header did not arrive through the supported child environment")
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	connection, err := f.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		f.failProtocol("upgrade websocket: " + err.Error())
		return
	}
	defer func() {
		if err := connection.Close(); err != nil {
			f.failProtocol("close websocket: " + err.Error())
		}
	}()
	f.mu.Lock()
	f.connectionCount++
	f.mu.Unlock()

	for {
		_, payload, readErr := connection.ReadMessage()
		if readErr != nil {
			return
		}
		var event familyBClientEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			f.failProtocol("decode client event: " + err.Error())
			return
		}
		if err := f.handleClientEvent(connection, event); err != nil {
			f.failProtocol(err.Error())
			return
		}
	}
}

type familyBClientEvent struct {
	Type  string `json:"type"`
	Audio string `json:"audio"`
	Item  struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	} `json:"item"`
}

func (f *familyBProviderFixture) handleClientEvent(connection *websocket.Conn, event familyBClientEvent) error {
	switch event.Type {
	case familyBSessionUpdateEvent:
		f.mu.Lock()
		f.sessionUpdates++
		f.mu.Unlock()
		return f.sendSessionReady(connection)
	case "input_audio_buffer.append":
		return f.handleInputAudio(connection, event.Audio)
	case "conversation.item.create":
		if event.Item.Type == "function_call_output" {
			return f.handleToolResult(connection, event.Item.CallID, event.Item.Output)
		}
	case familyBResponseCancelEvent:
		f.recordCancellation()
	case "input_audio_buffer.commit", "response.create":
		// The fixture models the two customer turns from the continuously
		// open stream and accepts the client's explicit end-of-input controls.
	default:
		// Optional provider metadata is not relevant to this filesystem and
		// interruption proof.
	}
	return nil
}

func (f *familyBProviderFixture) handleInputAudio(connection *websocket.Conn, encoded string) error {
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode input audio: %w", err)
	}
	if !familyBSilent(audio) {
		return f.handleCustomerUtterance(connection)
	}
	if err := f.send(connection, map[string]string{"type": "input_audio_buffer.speech_stopped"}); err != nil {
		return err
	}
	return f.send(connection, map[string]string{"type": "input_audio_buffer.committed"})
}

func (f *familyBProviderFixture) recordCancellation() {
	f.mu.Lock()
	f.cancelPending = true
	f.cancellationSent = f.elapsedLocked()
	f.cancellationEventRecorded = true
	f.cancellationResponseID = f.activeResponse
	f.mu.Unlock()
}

func (f *familyBProviderFixture) handleCustomerUtterance(connection *websocket.Conn) error {
	f.mu.Lock()
	index := f.utteranceIndex
	f.utteranceIndex++
	now := f.elapsedLocked()
	f.mu.Unlock()
	if index > 1 {
		return fmt.Errorf("received unexpected third customer utterance")
	}
	turnID := fmt.Sprintf("turn-%d", index+1)
	script := probe.FamilyBSpokenScript()[index]
	f.recordCustomerTranscript(probe.TranscriptEvent{
		ID: "customer-" + turnID, TurnID: turnID, Speaker: probe.TranscriptCustomer,
		Text: script.Text, At: now, Final: true,
	})

	if index == 0 {
		call := familyBFunctionCall{
			ID:       familyBOriginalCallID,
			ActionID: probe.FamilyBOriginalActionID,
			Name:     "write_file",
			Args:     familyBToolArguments("draft/brief.md", probe.FamilyBOriginalReleaseNote),
		}
		f.mu.Lock()
		f.originalToolStarted = now
		f.functionCalls = append(f.functionCalls, call)
		f.mu.Unlock()
		return f.sendToolCall(connection, "response-original-tool", call)
	}

	f.mu.Lock()
	cancelPending := f.cancelPending
	activeResponse := f.activeResponse
	f.correctionStarted = now
	f.mu.Unlock()
	if !cancelPending || activeResponse == "" {
		return fmt.Errorf("correction arrived without a cancellation of the active original response")
	}
	if err := f.finishCancelledOriginalResponse(connection); err != nil {
		return err
	}
	call := familyBFunctionCall{
		ID:       "call-family-b-replacement",
		ActionID: probe.FamilyBReplacementActionID,
		Name:     "write_file",
		Args:     familyBToolArguments("final/brief.md", probe.FamilyBReplacementReleaseNote),
	}
	f.mu.Lock()
	f.replacementToolStarted = now
	f.functionCalls = append(f.functionCalls, call)
	f.mu.Unlock()
	return f.sendToolCall(connection, "response-replacement-tool", call)
}

func (f *familyBProviderFixture) sendToolCall(connection *websocket.Conn, responseID string, call familyBFunctionCall) error {
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]string{
			"type": "function_call", "id": call.ID, "call_id": call.ID,
			"name": call.Name, "arguments": "",
		},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.function_call_arguments.done", "call_id": call.ID,
		"name": call.Name, "arguments": call.Args,
	}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": responseID, "status": "completed"},
	})
}

func (f *familyBProviderFixture) handleToolResult(connection *websocket.Conn, callID, output string) error {
	f.mu.Lock()
	var expected string
	var actionID string
	var turnID string
	var toolStarted time.Duration
	switch callID {
	case familyBOriginalCallID:
		expected = "File written: draft/brief.md"
		actionID = probe.FamilyBOriginalActionID
		turnID = "turn-1"
		toolStarted = f.originalToolStarted
	case "call-family-b-replacement":
		expected = "File written: final/brief.md"
		actionID = probe.FamilyBReplacementActionID
		turnID = "turn-2"
		toolStarted = f.replacementToolStarted
	default:
		f.mu.Unlock()
		return fmt.Errorf("unexpected function call output %q", callID)
	}
	if output != expected {
		f.mu.Unlock()
		return fmt.Errorf("tool result for %q = %q, want %q", callID, output, expected)
	}
	now := f.elapsedLocked()
	observation := probe.ToolObservation{
		ID: "tool-" + strings.TrimPrefix(callID, "call-family-b-"), ActionID: actionID,
		TurnID: turnID, Tool: "write_file", Status: "completed", At: toolStarted,
		Duration: now - toolStarted, ResultSeen: true, Summary: output,
	}
	f.toolObservations = append(f.toolObservations, observation)
	if callID == familyBOriginalCallID {
		f.originalResultSeen = true
	} else {
		f.replacementResultSeen = true
	}
	f.mu.Unlock()

	if callID == familyBOriginalCallID {
		return f.sendOriginalOutput(connection)
	}
	return f.sendReplacementOutput(connection)
}
