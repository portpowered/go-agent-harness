package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
)

type wireEvent struct {
	typeName string
	fields   map[string]json.RawMessage
}

type toolState struct {
	deltaSeen bool
	done      bool
}

func observedDeltaSums(fixture metricsreplay.Fixture) (map[seriesKey]int64, error) {
	sums := make(map[seriesKey]int64)
	tools := make(map[string]toolState)
	for index, record := range fixture.Records {
		if !record.Direction.Valid() {
			return nil, malformedRecord(index, record, "invalid wire direction", fmt.Errorf("%q", record.Direction))
		}
		event, err := decodeEvent(record)
		if err != nil {
			return nil, malformedRecord(index, record, "decode payload", err)
		}
		if err := observeWireEvent(record, event, sums, tools); err != nil {
			return nil, malformedRecord(index, record, "observe event", err)
		}
	}
	return sums, nil
}

func observeWireEvent(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64, tools map[string]toolState) error {
	switch event.typeName {
	case "response.audio.delta", "response.output_audio.delta":
		return observeOutputAudio(record, event, sums)
	case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
		return observeOutputText(record, event, sums)
	case "conversation.item.input_audio_transcription.delta", "input_audio_transcription.delta":
		return observeInputTranscript(record, event, sums)
	case "response.function_call_arguments.delta", "TOOLCALL.DELTA":
		return observeToolDelta(record, event, sums, tools)
	case "response.function_call_arguments.done", "TOOLCALL.END":
		return observeToolDone(record, event, sums, tools)
	case "conversation.item.create":
		return observeInputItemEvent(record, event, sums)
	case "input_audio_buffer.append":
		return observeInputAudioEvent(record, event, sums)
	case "TEXT.DELTA", "TRANSCRIPT.DELTA":
		return observeStreamText(record, event, sums)
	case "AUDIO.DELTA":
		return observeStreamAudio(record, event, sums)
	default:
		return nil
	}
}

func observeOutputAudio(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64) error {
	if err := requireDirection(record, metricsreplay.WireDirectionServerToClient); err != nil {
		return err
	}
	encoded, err := requiredString(event.fields, "delta")
	if err != nil {
		return fmt.Errorf("audio delta: %w", err)
	}
	return addBytes(sums, seriesKey{metricsreplay.DirectionOutput, metricsreplay.ModalityAudio}, decodedBase64Len(encoded))
}

func observeOutputText(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64) error {
	if err := requireDirection(record, metricsreplay.WireDirectionServerToClient); err != nil {
		return err
	}
	text, err := requiredString(event.fields, "delta")
	if err != nil {
		return fmt.Errorf("text delta: %w", err)
	}
	return addBytes(sums, seriesKey{metricsreplay.DirectionOutput, metricsreplay.ModalityText}, len(text))
}

func observeInputTranscript(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64) error {
	if err := requireDirection(record, metricsreplay.WireDirectionServerToClient); err != nil {
		return err
	}
	text, err := requiredString(event.fields, "delta")
	if err != nil {
		return fmt.Errorf("input transcript delta: %w", err)
	}
	return addBytes(sums, seriesKey{metricsreplay.DirectionInput, metricsreplay.ModalityText}, len(text))
}

func observeToolDelta(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64, tools map[string]toolState) error {
	if err := requireDirection(record, metricsreplay.WireDirectionServerToClient); err != nil {
		return err
	}
	callID, err := requiredStringForType(event.fields, "call_id", "tool_call_id")
	if err != nil || strings.TrimSpace(callID) == "" {
		if err == nil {
			err = errors.New("call_id must be non-empty")
		}
		return fmt.Errorf("tool delta call_id: %w", err)
	}
	delta, err := requiredStringForType(event.fields, "delta", "partial_json")
	if err != nil {
		return fmt.Errorf("tool delta: %w", err)
	}
	state := tools[callID]
	if state.done {
		return errors.New("tool delta arrived after terminal")
	}
	if err := addBytes(sums, seriesKey{metricsreplay.DirectionOutput, metricsreplay.ModalityTool}, len(delta)); err != nil {
		return err
	}
	state.deltaSeen = true
	tools[callID] = state
	return nil
}

func observeToolDone(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64, tools map[string]toolState) error {
	if err := requireDirection(record, metricsreplay.WireDirectionServerToClient); err != nil {
		return err
	}
	callID, err := requiredStringForType(event.fields, "call_id", "tool_call_id")
	if err != nil || strings.TrimSpace(callID) == "" {
		if err == nil {
			err = errors.New("call_id must be non-empty")
		}
		return fmt.Errorf("tool done call_id: %w", err)
	}
	arguments, err := requiredString(event.fields, "arguments")
	if err != nil {
		return fmt.Errorf("tool done arguments: %w", err)
	}
	state := tools[callID]
	if state.done {
		return errors.New("duplicate tool terminal")
	}
	if !state.deltaSeen {
		if err := addBytes(sums, seriesKey{metricsreplay.DirectionOutput, metricsreplay.ModalityTool}, len(arguments)); err != nil {
			return err
		}
	}
	state.done = true
	tools[callID] = state
	return nil
}

func observeInputItemEvent(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64) error {
	if err := requireDirection(record, metricsreplay.WireDirectionClientToServer); err != nil {
		return err
	}
	return observeInputItem(event.fields, sums)
}

func observeInputAudioEvent(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64) error {
	if err := requireDirection(record, metricsreplay.WireDirectionClientToServer); err != nil {
		return err
	}
	encoded, err := selectedAudio(event.fields)
	if err != nil {
		return err
	}
	return addBytes(sums, seriesKey{metricsreplay.DirectionInput, metricsreplay.ModalityAudio}, decodedBase64Len(encoded))
}

func observeStreamText(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64) error {
	text, err := requiredStringForType(event.fields, "content", "text")
	if err != nil {
		return err
	}
	direction, err := streamDirection(event.fields, record, event.typeName == "TRANSCRIPT.DELTA")
	if err != nil {
		return err
	}
	return addBytes(sums, seriesKey{direction, metricsreplay.ModalityText}, len(text))
}

func observeStreamAudio(record metricsreplay.Record, event wireEvent, sums map[seriesKey]int64) error {
	encoded, err := requiredStringForType(event.fields, "content", "audio")
	if err != nil {
		return err
	}
	direction, err := streamDirection(event.fields, record, false)
	if err != nil {
		return err
	}
	return addBytes(sums, seriesKey{direction, metricsreplay.ModalityAudio}, decodedBase64Len(encoded))
}
