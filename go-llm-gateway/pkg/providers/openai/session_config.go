package openai

// This file owns OpenAI Realtime session configuration construction and encoding, including legacy and current session-update payload forms plus their audio and tool helpers.
import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const realtimeSessionType = "realtime"

func (p *OpenAIProvider) buildRealtimeSessionUpdate(config models.SessionConfig, model string) (models.SessionEvent, error) {
	config.Tools = messages.CanonicalToolDefinitions(config.Tools)
	if p.realtimeLegacySessionUpdate {
		return buildLegacyRealtimeSessionUpdate(config, model, p.clientOwnsAudioTurnBoundaries)
	}

	update := map[string]any{
		"type":  realtimeSessionType,
		"model": model,
	}
	if len(config.Modalities) > 0 {
		update["output_modalities"] = sessionModalitiesToStrings(config.Modalities)
	}
	if config.Instructions != "" {
		update["instructions"] = config.Instructions
	}
	audio := buildRealtimeAudioConfig(config, p.clientOwnsAudioTurnBoundaries)
	if len(audio) > 0 {
		update["audio"] = audio
	}
	if len(config.Tools) > 0 {
		update["tools"] = realtimeToolsToParams(config.Tools)
	}

	data, err := json.Marshal(map[string]any{"session": update})
	if err != nil {
		return models.SessionEvent{}, fmt.Errorf("marshal session update: %w", err)
	}
	return models.NewSessionUpdateEvent(data), nil
}

func buildLegacyRealtimeSessionUpdate(config models.SessionConfig, model string, disableTurnDetection bool) (models.SessionEvent, error) {
	config.Tools = messages.CanonicalToolDefinitions(config.Tools)
	update := map[string]any{
		"type":  realtimeSessionType,
		"model": model,
	}
	if len(config.Modalities) > 0 {
		update["modalities"] = sessionModalitiesToStrings(config.Modalities)
	}
	if config.Voice != "" {
		update["voice"] = config.Voice
	}
	if config.Instructions != "" {
		update["instructions"] = config.Instructions
	}
	if config.InputAudioFormat != "" {
		update["input_audio_format"] = config.InputAudioFormat
	}
	if config.OutputAudioFormat != "" {
		update["output_audio_format"] = config.OutputAudioFormat
	}
	if disableTurnDetection {
		// The explicit null is part of the client-owned turn-boundary contract.
		// Leaving this field out would preserve the provider's effective VAD.
		update["turn_detection"] = nil
	} else if config.TurnDetection != nil {
		update["turn_detection"] = config.TurnDetection
	}
	if transcription, ok := realtimeInputAudioTranscription(config, disableTurnDetection); ok {
		update["input_audio_transcription"] = transcription
	}
	if len(config.Tools) > 0 {
		update["tools"] = config.Tools
	}

	data, err := json.Marshal(map[string]any{"session": update})
	if err != nil {
		return models.SessionEvent{}, fmt.Errorf("marshal session update: %w", err)
	}
	return models.NewSessionUpdateEvent(data), nil
}

func sessionModalitiesToStrings(modalities []models.SessionModality) []string {
	out := make([]string, 0, len(modalities))
	for _, modality := range modalities {
		if modality == "" {
			continue
		}
		out = append(out, string(modality))
	}
	return out
}

func buildRealtimeAudioConfig(config models.SessionConfig, disableTurnDetection bool) map[string]any {
	audio := map[string]any{}
	input := map[string]any{}
	output := map[string]any{}

	if config.InputAudioFormat != "" || config.InputAudioSampleRate != 0 {
		input["format"] = realtimeAudioFormat(config.InputAudioFormat, config.InputAudioSampleRate)
	}
	if disableTurnDetection {
		// The GA Realtime schema places turn_detection under audio.input. Keep
		// the key present with a JSON null so the provider cannot retain or
		// select its default VAD configuration.
		input["turn_detection"] = nil
	} else if config.TurnDetection != nil {
		input["turn_detection"] = config.TurnDetection
	}
	if transcription, ok := realtimeInputAudioTranscription(config, disableTurnDetection); ok {
		input["transcription"] = transcription
	}
	if len(input) > 0 {
		audio["input"] = input
	}

	if config.OutputAudioFormat != "" || config.OutputAudioSampleRate != 0 {
		output["format"] = realtimeAudioFormat(config.OutputAudioFormat, config.OutputAudioSampleRate)
	}
	if config.Voice != "" {
		output["voice"] = config.Voice
	}
	if len(output) > 0 {
		audio["output"] = output
	}

	return audio
}

func realtimeInputAudioTranscription(config models.SessionConfig, clientOwnsAudioTurnBoundaries bool) (map[string]any, bool) {
	policy := config.InputAudioTranscription
	if policy == nil || !policy.Enabled || !realtimeSessionAcceptsAudioInput(config, clientOwnsAudioTurnBoundaries) {
		return nil, false
	}
	model := strings.TrimSpace(policy.Model)
	if model == "" {
		model = models.DefaultInputAudioTranscriptionModel
	}
	return map[string]any{"model": model}, true
}

func realtimeSessionAcceptsAudioInput(config models.SessionConfig, clientOwnsAudioTurnBoundaries bool) bool {
	return clientOwnsAudioTurnBoundaries || config.InputAudioFormat != "" || config.InputAudioSampleRate != 0
}

func realtimeAudioFormat(format models.AudioFormat, rate models.SampleRate) map[string]any {
	switch format {
	case models.AudioFormatG711Ulaw:
		return map[string]any{"type": "audio/pcmu"}
	case models.AudioFormatG711Alaw:
		return map[string]any{"type": "audio/pcma"}
	default:
		audioFormat := map[string]any{"type": "audio/pcm"}
		if rate != 0 {
			audioFormat["rate"] = rate
		}
		return audioFormat
	}
}

func realtimeToolsToParams(tools []models.ToolDefinition) []map[string]any {
	tools = messages.CanonicalToolDefinitions(tools)
	params := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		params = append(params, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  buildParameters(tool),
		})
	}
	return params
}
