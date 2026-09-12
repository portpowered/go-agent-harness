package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
)

func observeInputItem(fields map[string]json.RawMessage, sums map[seriesKey]int64) error {
	item, err := requiredObject(fields, "item")
	if err != nil {
		return err
	}
	itemType, present, err := optionalString(item, "type")
	if err != nil {
		return fmt.Errorf("item type: %w", err)
	}
	if present && itemType != "" && itemType != "message" {
		return nil
	}
	content, err := requiredArray(item, "content")
	if err != nil {
		return err
	}
	for index, raw := range content {
		if err := observeInputContentPart(raw, index, sums); err != nil {
			return err
		}
	}
	return nil
}

func observeInputContentPart(raw json.RawMessage, index int, sums map[seriesKey]int64) error {
	var part map[string]json.RawMessage
	if err := json.Unmarshal(raw, &part); err != nil || part == nil {
		if err == nil {
			err = errors.New("content part must be an object")
		}
		return fmt.Errorf("content part %d: %w", index, err)
	}
	kind, err := requiredString(part, "type")
	if err != nil {
		return fmt.Errorf("content part %d type: %w", index, err)
	}
	switch kind {
	case "input_text":
		return observeInputTextPart(part, index, sums)
	case "input_audio":
		return observeInputAudioPart(part, index, sums)
	default:
		return nil
	}
}

func observeInputTextPart(part map[string]json.RawMessage, index int, sums map[seriesKey]int64) error {
	text, err := requiredString(part, "text")
	if err != nil {
		return fmt.Errorf("content part %d text: %w", index, err)
	}
	return addBytes(sums, seriesKey{metricsreplay.DirectionInput, metricsreplay.ModalityText}, len(text))
}

func observeInputAudioPart(part map[string]json.RawMessage, index int, sums map[seriesKey]int64) error {
	encoded, err := inputAudioData(part)
	if err != nil {
		return fmt.Errorf("content part %d audio data: %w", index, err)
	}
	return addBytes(sums, seriesKey{metricsreplay.DirectionInput, metricsreplay.ModalityAudio}, decodedBase64Len(encoded))
}

func inputAudioData(part map[string]json.RawMessage) (string, error) {
	if encoded, present, err := optionalString(part, "audio"); err != nil {
		return "", err
	} else if present {
		return encoded, nil
	}
	audio, err := requiredObject(part, "input_audio")
	if err != nil {
		return "", err
	}
	if encoded, present, err := optionalString(audio, "data"); err != nil {
		return "", err
	} else if present {
		return encoded, nil
	}
	return requiredString(audio, "audio")
}
