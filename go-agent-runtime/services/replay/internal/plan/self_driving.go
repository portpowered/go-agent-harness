package plan

import (
	"bytes"
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// LoadCapturedTextPrompt admits the one text action shape supported by a bare
// live replay. It rejects extra or incomplete caller actions rather than
// silently inventing provider traffic.
func (s *Service) LoadCapturedTextPrompt(ctx context.Context, path string) (*replay.CapturedTextPrompt, error) {
	capture, err := s.admitCapture(ctx, path)
	if err != nil {
		return nil, err
	}
	if _, err := parseReplayConfiguration(path, capture.Records); err != nil {
		return nil, err
	}
	return loadCapturedTextPrompt(path, capture.Records)
}

func loadCapturedTextPrompt(path string, records []gatewaytesting.CapturedSessionEvent) (*replay.CapturedTextPrompt, error) {
	actions := replayClientActions(records)
	promptPosition := -1
	var prompt *replay.CapturedTextPrompt
	for position, record := range actions {
		if record.Type != replayCreateItem {
			continue
		}
		text, isPrompt, err := replayTextPrompt(path, record)
		if err != nil {
			return nil, err
		}
		if !isPrompt {
			continue
		}
		if prompt != nil {
			return nil, fmt.Errorf("replay session capture %s has ambiguous recorded text prompts at sequences %d and %d", path, actions[promptPosition].Sequence, record.Sequence)
		}
		prompt = &replay.CapturedTextPrompt{Text: text}
		promptPosition = position
	}
	if prompt == nil {
		return nil, nil
	}
	if promptPosition != 0 {
		return nil, fmt.Errorf("replay session capture %s has an ambiguous recorded prompt at sequence %d: it must be the first client action after session.update", path, actions[promptPosition].Sequence)
	}
	if promptPosition+1 >= len(actions) || actions[promptPosition+1].Type != replayResponseCreate {
		return nil, fmt.Errorf("replay session capture %s has an incomplete recorded prompt at sequence %d: expected the next client action to be %s", path, actions[promptPosition].Sequence, replayResponseCreate)
	}
	if promptPosition+2 != len(actions) {
		return nil, fmt.Errorf("replay session capture %s has an ambiguous recorded prompt at sequence %d: expected only %s after it", path, actions[promptPosition].Sequence, replayResponseCreate)
	}
	if err := validateReplayEventPayload(path, actions[promptPosition+1], replayResponseCreate); err != nil {
		return nil, err
	}
	return prompt, nil
}

// LoadCapturedAudioTurns admits bounded append/commit/response.create groups
// and returns independent PCM bytes for the session service's input adapter.
func (s *Service) LoadCapturedAudioTurns(ctx context.Context, path string) ([]replay.CapturedAudioTurn, error) {
	capture, err := s.admitCapture(ctx, path)
	if err != nil {
		return nil, err
	}
	if _, err := parseReplayConfiguration(path, capture.Records); err != nil {
		return nil, err
	}
	return loadCapturedAudioTurns(path, capture.Records, false)
}

// LoadCapturedAudioTurnsRaw is a Deprecated compatibility port for old CLI
// fixtures whose opaque audio bytes predate the strict PCM16 alignment check.
func (s *Service) LoadCapturedAudioTurnsRaw(ctx context.Context, path string) ([]replay.CapturedAudioTurn, error) {
	capture, err := s.admitCapture(ctx, path)
	if err != nil {
		return nil, err
	}
	if _, err := parseReplayConfiguration(path, capture.Records); err != nil {
		return nil, err
	}
	return loadCapturedAudioTurns(path, capture.Records, true)
}

func loadCapturedAudioTurns(path string, records []gatewaytesting.CapturedSessionEvent, allowUnaligned bool) ([]replay.CapturedAudioTurn, error) {
	actions := replayClientActions(records)
	if len(actions) == 0 || actions[0].Type != replayAppend {
		return nil, nil
	}
	turns := make([]replay.CapturedAudioTurn, 0)
	remaining := replaySampleLimit * 2
	for position := 0; position < len(actions); {
		turnIndex := len(turns) + 1
		turn, next, err := loadCapturedAudioTurn(path, actions, position, turnIndex, &remaining, allowUnaligned)
		if err != nil {
			return nil, err
		}
		turn.AfterCompletedTurns = len(turns)
		turns = append(turns, turn)
		position = next
	}
	return turns, nil
}

func loadCapturedAudioTurn(path string, actions []gatewaytesting.CapturedSessionEvent, position, turnIndex int, remaining *int, allowUnaligned bool) (replay.CapturedAudioTurn, int, error) {
	if actions[position].Type != replayAppend {
		return replay.CapturedAudioTurn{}, position, fmt.Errorf("replay session capture %s has an ambiguous recorded client action at sequence %d: expected %s to begin audio turn %d", path, actions[position].Sequence, replayAppend, turnIndex)
	}
	pcm, next, err := collectCapturedAudio(path, actions, position, remaining, allowUnaligned)
	if err != nil {
		return replay.CapturedAudioTurn{}, position, err
	}
	if next >= len(actions) {
		return replay.CapturedAudioTurn{}, next, incompleteAudioTurn(path, turnIndex, actions[next-1], replayCommit)
	}
	if err := validateReplayEventPayload(path, actions[next], replayCommit); err != nil {
		return replay.CapturedAudioTurn{}, next, err
	}
	next++
	if next >= len(actions) {
		return replay.CapturedAudioTurn{}, next, incompleteAudioTurn(path, turnIndex, actions[next-1], replayResponseCreate)
	}
	if err := validateReplayEventPayload(path, actions[next], replayResponseCreate); err != nil {
		return replay.CapturedAudioTurn{}, next, err
	}
	next++
	if pcm.Len() == 0 {
		return replay.CapturedAudioTurn{}, next, fmt.Errorf("replay session capture %s has an empty recorded audio turn %d", path, turnIndex)
	}
	return replay.CapturedAudioTurn{PCM: append([]byte(nil), pcm.Bytes()...), EndOfTurn: true}, next, nil
}

func collectCapturedAudio(path string, actions []gatewaytesting.CapturedSessionEvent, position int, remaining *int, allowUnaligned bool) (bytes.Buffer, int, error) {
	var pcm bytes.Buffer
	for position < len(actions) && actions[position].Type == replayAppend {
		pcmChunk, err := replayRawAudioChunk(path, actions[position])
		if err != nil {
			return bytes.Buffer{}, position, err
		}
		if len(pcmChunk) > *remaining {
			return bytes.Buffer{}, position, fmt.Errorf("replay session capture %s: audio exceeds bounded replay capacity", path)
		}
		if !allowUnaligned && len(pcmChunk)%2 != 0 {
			return bytes.Buffer{}, position, fmt.Errorf("replay session capture %s: decode audio append at sequence %d: odd byte length", path, actions[position].Sequence)
		}
		*remaining -= len(pcmChunk)
		pcm.Write(pcmChunk)
		position++
	}
	return pcm, position, nil
}

const (
	replayCommit = "input_audio_buffer.commit"
)

func incompleteAudioTurn(path string, turnIndex int, previous gatewaytesting.CapturedSessionEvent, expected string) error {
	eventSuffix := "event"
	if expected == replayCommit {
		eventSuffix = "event(s)"
	}
	return fmt.Errorf("replay session capture %s has an incomplete recorded audio turn %d: expected %s after its recorded %s %s at sequence %d", path, turnIndex, expected, previous.Type, eventSuffix, previous.Sequence)
}

func replayClientActions(records []gatewaytesting.CapturedSessionEvent) []gatewaytesting.CapturedSessionEvent {
	actions := make([]gatewaytesting.CapturedSessionEvent, 0, len(records))
	for _, record := range records {
		if record.Direction == gatewaytesting.DirectionClientToServer && record.Type != replaySessionUpdate {
			actions = append(actions, record)
		}
	}
	return actions
}

func validateReplayEventPayload(path string, record gatewaytesting.CapturedSessionEvent, eventType string) error {
	payload := replayRecordPayload(record)
	if len(payload) == 0 {
		return fmt.Errorf("replay session capture %s: recorded %s at sequence %d has no payload", path, eventType, record.Sequence)
	}
	if err := replayPayloadType(record, eventType); err != nil {
		return fmt.Errorf("replay session capture %s: recorded %s at sequence %d: %w", path, eventType, record.Sequence, err)
	}
	return nil
}
