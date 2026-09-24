package plan

import (
	"encoding/json"
	"fmt"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func replayAudioSampleRates(records []gatewaytesting.CapturedSessionEvent) (int, int, error) {
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != replaySessionUpdate {
			continue
		}
		var envelope struct {
			Session map[string]json.RawMessage `json:"session"`
		}
		if err := json.Unmarshal(replayRecordPayload(record), &envelope); err != nil {
			return 0, 0, fmt.Errorf("decode session configuration at sequence %d: %w", record.Sequence, err)
		}
		return replaySessionAudioSampleRates(envelope.Session)
	}
	return 0, 0, nil
}

func replaySessionAudioSampleRates(session map[string]json.RawMessage) (int, int, error) {
	var audio struct {
		Input struct {
			Format struct {
				Rate int `json:"rate"`
			} `json:"format"`
		} `json:"input"`
		Output struct {
			Format struct {
				Rate int `json:"rate"`
			} `json:"format"`
		} `json:"output"`
	}
	if raw, ok := session["audio"]; ok {
		if err := json.Unmarshal(raw, &audio); err != nil {
			return 0, 0, fmt.Errorf("decode audio configuration: %w", err)
		}
	}
	inputRate := audio.Input.Format.Rate
	outputRate := audio.Output.Format.Rate
	var err error
	if inputRate <= 0 {
		inputRate, err = replayLegacyFormatRate(session["input_audio_format"])
		if err != nil {
			return 0, 0, fmt.Errorf("decode input audio format: %w", err)
		}
	}
	if outputRate <= 0 {
		outputRate, err = replayLegacyFormatRate(session["output_audio_format"])
		if err != nil {
			return 0, 0, fmt.Errorf("decode output audio format: %w", err)
		}
	}
	return inputRate, outputRate, nil
}

func replayLegacyFormatRate(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	// Older captures name a codec without declaring a sample rate.
	if raw[0] == '"' {
		var codec string
		return 0, json.Unmarshal(raw, &codec)
	}
	var format struct {
		Rate int `json:"rate"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return 0, err
	}
	return format.Rate, nil
}
