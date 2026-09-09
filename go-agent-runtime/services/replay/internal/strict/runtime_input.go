package strict

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type replayInputAction struct {
	startIndex   int
	text         string
	audio        [][]byte
	responseEnds int
}

func deriveInputActions(capture gwtesting.SessionCapture) ([]replayInputAction, error) {
	builder := inputBuilder{audioAction: -1}
	for index, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		if err := builder.consume(record.Sequence, index, record.Payload); err != nil {
			return nil, err
		}
	}
	return finishInputActions(builder.actions, capture), validateInputActions(builder.actions)
}

type inputBuilder struct {
	actions     []replayInputAction
	audioAction int
}

func (b *inputBuilder) consume(sequence int, index int, payload []byte) error {
	var envelope replayInputEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("%w: decode recorded input at sequence %d: %w", replay.ErrBundleIncomplete, sequence, err)
	}
	switch envelope.Type {
	case "conversation.item.create":
		return b.addText(index, envelope)
	case "input_audio_buffer.append":
		return b.addAudio(sequence, index, envelope.Audio)
	case "input_audio_buffer.commit", "response.create":
		b.audioAction = -1
	case "response.cancel":
		return fmt.Errorf("%w: response cancellation and overlapping input are not supported by the headless replay factory", replay.ErrBundleMismatch)
	}
	return nil
}

type replayInputEnvelope struct {
	Type string `json:"type"`
	Item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
	Audio string `json:"audio"`
}

func (b *inputBuilder) addText(index int, envelope replayInputEnvelope) error {
	b.audioAction = -1
	if envelope.Item.Type != "message" || envelope.Item.Role != "user" {
		return nil
	}
	var text strings.Builder
	for _, part := range envelope.Item.Content {
		if part.Type == "input_text" {
			text.WriteString(part.Text)
		}
	}
	if text.Len() > 0 {
		b.actions = append(b.actions, replayInputAction{startIndex: index, text: text.String()})
	}
	return nil
}

func (b *inputBuilder) addAudio(sequence int, index int, encoded string) error {
	if encoded == "" {
		return fmt.Errorf("%w: empty recorded audio input at sequence %d", replay.ErrBundleIncomplete, sequence)
	}
	pcm, err := codec.DecodeBase64(encoded)
	if err != nil {
		return fmt.Errorf("%w: decode recorded audio input at sequence %d: %w", replay.ErrBundleIncomplete, sequence, err)
	}
	if b.audioAction >= 0 {
		b.actions[b.audioAction].audio = append(b.actions[b.audioAction].audio, pcm)
		return nil
	}
	b.actions = append(b.actions, replayInputAction{startIndex: index, audio: [][]byte{pcm}})
	b.audioAction = len(b.actions) - 1
	return nil
}

func finishInputActions(actions []replayInputAction, capture gwtesting.SessionCapture) []replayInputAction {
	for index := range actions {
		end := len(capture.Records)
		if index+1 < len(actions) {
			end = actions[index+1].startIndex
		}
		actions[index].responseEnds = countResponseEnds(capture.Records[actions[index].startIndex:end])
	}
	return actions
}

func countResponseEnds(records []gwtesting.CapturedSessionEvent) int {
	count := 0
	for _, record := range records {
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.done" {
			count++
		}
	}
	return count
}

func validateInputActions(actions []replayInputAction) error {
	for index := range actions {
		if actions[index].responseEnds > 0 {
			continue
		}
		if index+1 < len(actions) {
			return fmt.Errorf("%w: recorded input overlaps an unfinished provider response", replay.ErrBundleMismatch)
		}
		return fmt.Errorf("%w: final recorded input has no response.done completion boundary", replay.ErrBundleIncomplete)
	}
	return nil
}
