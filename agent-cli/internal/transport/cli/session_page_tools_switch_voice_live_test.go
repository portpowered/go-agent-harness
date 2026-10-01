//go:build live

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	sessionPageToolsSwitchVoiceArtifactEnv     = "WEBMCP_PAGETOOLS_SWITCH_VOICE_ARTIFACT_DIR"
	sessionPageToolsSwitchVoiceKeyFileEnv      = "OPENAI_API_KEY_FILE"
	sessionPageToolsSwitchVoiceMaxDuration     = 30 * time.Second
	sessionPageToolsSwitchVoiceRunGrace        = 25 * time.Second
	sessionPageToolsSwitchVoiceChromeVersion   = "152.0.7977.64"
	sessionPageToolsSwitchVoiceArtifactMode    = 0o700
	sessionPageToolsSwitchVoiceEvidenceMode    = 0o600
	sessionPageToolsSwitchVoiceCaptureFilename = "provider.json"
	sessionPageToolsSwitchVoiceSessionUpdate   = "session.update"
)

func sessionPageToolsSwitchVoiceAPIKey(t *testing.T) (string, string) {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv(sessionPageToolsSwitchVoiceKeyFileEnv)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read OpenAI API key file: %v", err)
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			t.Fatalf("OpenAI API key file is empty")
		}
		return key, sessionPageToolsSwitchVoiceKeyFileEnv
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return key, "OPENAI_API_KEY"
	}
	t.Fatal("OPENAI_API_KEY_FILE or OPENAI_API_KEY is not set; the credentialed WebMCP voice confirmation requires one")
	return "", ""
}

func inspectSessionPageToolsSwitchVoiceCapture(capture gwtesting.SessionCapture) (sessionPageToolsSwitchVoiceObservation, error) {
	observation := sessionPageToolsSwitchVoiceObservation{
		Provider: capture.Provider.Name,
		Model:    capture.Provider.Model,
	}
	for index, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if len(payload) == 0 {
			continue
		}
		var err error
		switch record.Direction {
		case gwtesting.DirectionClientToServer:
			err = observation.addClientRecord(index, record.Type, payload)
		case gwtesting.DirectionServerToClient:
			err = observation.addServerRecord(index, record.Type, payload)
		}
		if err != nil {
			return observation, err
		}
	}
	return observation, nil
}

func (observation *sessionPageToolsSwitchVoiceObservation) addClientRecord(index int, recordType string, payload json.RawMessage) error {
	switch recordType {
	case sessionPageToolsSwitchVoiceSessionUpdate:
		surface, err := sessionPageToolsSwitchVoiceSurfaceFromUpdate(index, payload)
		if err != nil {
			return err
		}
		if len(surface.Tools) > 0 {
			observation.Surfaces = append(observation.Surfaces, surface)
		}
	case "conversation.item.create":
		output, ok, err := sessionPageToolsSwitchVoiceOutputFromItem(index, payload)
		if err != nil {
			return err
		}
		if ok {
			observation.Outputs = append(observation.Outputs, output)
		}
	}
	return nil
}

func (observation *sessionPageToolsSwitchVoiceObservation) addServerRecord(index int, recordType string, payload json.RawMessage) error {
	switch recordType {
	case "session.created":
		observation.SessionCreated++
	case "response.output_item.added":
		return observation.addFunctionCall(index, payload)
	case "response.function_call_arguments.done":
		return observation.addFunctionArguments(index, payload)
	case "conversation.item.input_audio_transcription.completed":
		if text := sessionPageToolsSwitchVoiceStringField(payload, "transcript"); text != "" {
			observation.UserTranscripts = append(observation.UserTranscripts, text)
		}
	case "response.output_audio_transcript.done", "response.audio_transcript.done":
		if text := sessionPageToolsSwitchVoiceStringField(payload, "transcript"); text != "" {
			observation.AssistantTranscripts = append(observation.AssistantTranscripts, text)
		}
	case "response.output_text.done":
		if text := sessionPageToolsSwitchVoiceStringField(payload, "text"); text != "" {
			observation.AssistantTranscripts = append(observation.AssistantTranscripts, text)
		}
	}
	return nil
}

func (observation *sessionPageToolsSwitchVoiceObservation) addFunctionCall(index int, payload json.RawMessage) error {
	var event struct {
		Item struct {
			Type   string `json:"type"`
			Name   string `json:"name"`
			CallID string `json:"call_id"`
			ID     string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode function call at record %d: %w", index, err)
	}
	if event.Item.Type != "function_call" {
		return nil
	}
	callID := event.Item.CallID
	if callID == "" {
		callID = event.Item.ID
	}
	observation.Calls = append(observation.Calls, sessionPageToolsSwitchVoiceCall{Index: index, Name: event.Item.Name, CallID: callID, ArgumentsAt: -1})
	return nil
}

func (observation *sessionPageToolsSwitchVoiceObservation) addFunctionArguments(index int, payload json.RawMessage) error {
	var event struct {
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode function arguments at record %d: %w", index, err)
	}
	callIndex := -1
	for candidate := len(observation.Calls) - 1; candidate >= 0; candidate-- {
		if observation.Calls[candidate].ArgumentsAt >= 0 {
			continue
		}
		if event.CallID == "" || observation.Calls[candidate].CallID == event.CallID {
			callIndex = candidate
			break
		}
	}
	if callIndex < 0 {
		return fmt.Errorf("function arguments at record %d have no matching call_id=%q", index, event.CallID)
	}
	call := &observation.Calls[callIndex]
	call.Arguments = event.Arguments
	call.ArgumentsAt = index
	if call.Name == "" {
		call.Name = event.Name
	}
	if call.CallID == "" {
		call.CallID = event.CallID
	}
	return nil
}

func sessionPageToolsSwitchVoiceSurfaceFromUpdate(index int, payload json.RawMessage) (sessionPageToolsSwitchVoiceSurface, error) {
	var event struct {
		Session struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return sessionPageToolsSwitchVoiceSurface{}, fmt.Errorf("decode session.update at record %d: %w", index, err)
	}
	surface := sessionPageToolsSwitchVoiceSurface{Index: index, Tools: make([]sessionPageToolsSwitchVoiceTool, 0, len(event.Session.Tools))}
	for _, raw := range event.Session.Tools {
		var name struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &name); err != nil {
			return sessionPageToolsSwitchVoiceSurface{}, fmt.Errorf("decode tool in session.update at record %d: %w", index, err)
		}
		if name.Name == "" {
			return sessionPageToolsSwitchVoiceSurface{}, fmt.Errorf("session.update at record %d contains an unnamed tool", index)
		}
		surface.Tools = append(surface.Tools, sessionPageToolsSwitchVoiceTool{Name: name.Name, Raw: append(json.RawMessage(nil), raw...)})
	}
	return surface, nil
}

func sessionPageToolsSwitchVoiceOutputFromItem(index int, payload json.RawMessage) (sessionPageToolsSwitchVoiceOutput, bool, error) {
	var event struct {
		Item struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return sessionPageToolsSwitchVoiceOutput{}, false, fmt.Errorf("decode tool result at record %d: %w", index, err)
	}
	if event.Item.Type != "function_call_output" {
		return sessionPageToolsSwitchVoiceOutput{}, false, nil
	}
	var output string
	if err := json.Unmarshal(event.Item.Output, &output); err != nil {
		output = string(event.Item.Output)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(output))
	if err != nil {
		return sessionPageToolsSwitchVoiceOutput{}, false, fmt.Errorf("decode tool result at record %d: %w", index, err)
	}
	return sessionPageToolsSwitchVoiceOutput{Index: index, CallID: event.Item.CallID, Envelope: envelope}, true, nil
}

func sessionPageToolsSwitchVoiceStringField(payload json.RawMessage, field string) string {
	var object map[string]any
	if json.Unmarshal(payload, &object) != nil {
		return ""
	}
	value, ok := object[field].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func validateSessionPageToolsSwitchVoiceRecordDir(path string) error {
	for _, name := range []string{"manifest.json", "client.transcript.jsonl", "agent.transcript.jsonl"} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			return fmt.Errorf("stat %s: %w", name, err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("record-dir artifact %s is empty", name)
		}
	}
	return nil
}

func directLiveInvoke(t *testing.T, ctx context.Context, binary, cdpURL string, target sessionPageToolsLiveTarget, toolRef string, input any) webmcp.ToolResultEnvelope {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal direct CLI input: %v", err)
	}
	return runDirectLiveCLI(t, ctx, binary, cdpURL, target, "invoke", "--tool-ref", toolRef, "--input-json", string(encoded), "--timeout", "90s", "--invocation-timeout", "120s")
}
