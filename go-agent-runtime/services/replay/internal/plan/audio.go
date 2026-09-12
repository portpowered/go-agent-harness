package plan

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const replaySampleLimit = replayAudioChunkLimit * 128

type audioCursor struct {
	path      string
	actions   []gatewaytesting.CapturedSessionEvent
	position  int
	remaining int
}

func replayAudioPlan(path string, actions []gatewaytesting.CapturedSessionEvent) (session.LiveReplayPlan, error) {
	plan := session.LiveReplayPlan{}
	cursor := audioCursor{path: path, actions: actions, remaining: replaySampleLimit}
	for cursor.position < len(actions) {
		turn, err := cursor.nextTurn()
		if err != nil {
			return session.LiveReplayPlan{}, fmt.Errorf("live replay plan %s: audio turn %d: %w", path, len(plan.AudioTurns)+1, err)
		}
		plan.AudioTurns = append(plan.AudioTurns, turn)
	}
	return plan, nil
}

func (c *audioCursor) nextTurn() (session.LiveReplayAudioTurn, error) {
	turn := session.LiveReplayAudioTurn{}
	if c.actions[c.position].Type != replayAppend {
		return turn, fmt.Errorf("sequence %d must begin with input_audio_buffer.append", c.actions[c.position].Sequence)
	}
	for c.position < len(c.actions) && c.actions[c.position].Type == replayAppend {
		samples, err := replayAudioChunk(c.path, c.actions[c.position])
		if err != nil {
			return turn, err
		}
		if len(samples) > c.remaining {
			return turn, fmt.Errorf("audio exceeds bounded replay capacity")
		}
		c.remaining -= len(samples)
		turn.Chunks = append(turn.Chunks, samples)
		c.position++
	}
	if err := c.consume("input_audio_buffer.commit"); err != nil {
		return turn, err
	}
	if err := c.consume("response.create"); err != nil {
		return turn, err
	}
	return turn, nil
}

func (c *audioCursor) consume(kind string) error {
	if c.position >= len(c.actions) || c.actions[c.position].Type != kind {
		if c.position >= len(c.actions) {
			if c.position > 0 {
				previous := c.actions[c.position-1]
				return fmt.Errorf("missing %s after %s at sequence %d", kind, previous.Type, previous.Sequence)
			}
			return fmt.Errorf("missing %s", kind)
		}
		actual := c.actions[c.position]
		if c.position > 0 {
			previous := c.actions[c.position-1]
			return fmt.Errorf("missing %s after %s at sequence %d: found %s at sequence %d", kind, previous.Type, previous.Sequence, actual.Type, actual.Sequence)
		}
		return fmt.Errorf("missing %s before %s at sequence %d", kind, actual.Type, actual.Sequence)
	}
	if err := replayPayloadType(c.actions[c.position], kind); err != nil {
		return err
	}
	c.position++
	return nil
}

func replayAudioChunk(path string, record gatewaytesting.CapturedSessionEvent) ([]int16, error) {
	pcm, err := replayRawAudioChunk(path, record)
	if err != nil {
		return nil, err
	}
	samples, err := codec.DecodePCM16WithLimit(pcm, replayAudioChunkLimit)
	if err != nil {
		return nil, fmt.Errorf("live replay plan %s: decode audio append at sequence %d: %w", path, record.Sequence, err)
	}
	return samples, nil
}

func replayRawAudioChunk(path string, record gatewaytesting.CapturedSessionEvent) ([]byte, error) {
	var envelope struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &envelope); err != nil {
		return nil, fmt.Errorf("live replay plan %s: decode audio append at sequence %d: %w", path, record.Sequence, err)
	}
	if envelope.Type != replayAppend || strings.TrimSpace(envelope.Audio) == "" {
		return nil, fmt.Errorf("live replay plan %s: audio append at sequence %d is missing its audio payload", path, record.Sequence)
	}
	pcm, err := codec.DecodeBase64WithLimit(envelope.Audio, replayAudioChunkLimit)
	if err != nil {
		return nil, fmt.Errorf("live replay plan %s: decode audio append at sequence %d: %w", path, record.Sequence, err)
	}
	return pcm, nil
}

func replayPayloadType(record gatewaytesting.CapturedSessionEvent, want string) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &envelope); err != nil {
		return fmt.Errorf("decode %s at sequence %d: %w", want, record.Sequence, err)
	}
	if envelope.Type != want {
		return fmt.Errorf("recorded %s at sequence %d has payload type %q", want, record.Sequence, envelope.Type)
	}
	return nil
}

func replayRecordPayload(record gatewaytesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}
