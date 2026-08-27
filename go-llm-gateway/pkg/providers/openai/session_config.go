package openai

// This file owns OpenAI Realtime session configuration construction and encoding, including legacy and current session-update payload forms plus their audio and tool helpers.
import (
	"encoding/json"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func (p *OpenAIProvider) buildRealtimeSessionUpdate(config models.SessionConfig, model string) (models.SessionEvent, error) {
	if p.clientOwnsAudioTurnBoundaries {
		config.TurnDetection = clientOwnedAudioTurnDetection(config.TurnDetection)
	}
	if p.realtimeLegacySessionUpdate {
		return buildLegacyRealtimeSessionUpdate(config, model)
	}

	update := map[string]any{
		"type":  "realtime",
		"model": model,
	}
	if len(config.Modalities) > 0 {
		update["output_modalities"] = sessionModalitiesToStrings(config.Modalities)
	}
	if config.Instructions != "" {
		update["instructions"] = config.Instructions
	}
	audio := buildRealtimeAudioConfig(config)
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

func clientOwnedAudioTurnDetection(existing *models.TurnDetectionConfig) *models.TurnDetectionConfig {
	detection := models.TurnDetectionConfig{Type: "server_vad"}
	if existing != nil {
		detection = *existing
		if detection.Type == "" {
			detection.Type = "server_vad"
		}
	}
	createResponse := false
	detection.CreateResponse = &createResponse
	return &detection
}

func buildLegacyRealtimeSessionUpdate(config models.SessionConfig, model string) (models.SessionEvent, error) {
	update := map[string]any{
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
	if config.TurnDetection != nil {
		update["turn_detection"] = config.TurnDetection
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

func buildRealtimeAudioConfig(config models.SessionConfig) map[string]any {
	audio := map[string]any{}
	input := map[string]any{}
	output := map[string]any{}

	if config.InputAudioFormat != "" || config.InputAudioSampleRate != 0 {
		input["format"] = realtimeAudioFormat(config.InputAudioFormat, config.InputAudioSampleRate)
	}
	if config.TurnDetection != nil {
		input["turn_detection"] = config.TurnDetection
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
