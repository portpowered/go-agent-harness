package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func loadReplaySessionAudioTurns(path string) ([]providersession.AudioInput, error) {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return nil, fmt.Errorf("load replay session capture %s: %w", path, err)
	}
	actions := replayClientActions(loaded.Capture.Records)
	if len(actions) == 0 || actions[0].Type != inputAudioAppendEvent {
		return nil, nil
	}
	var turns []providersession.AudioInput
	position := 0
	for position < len(actions) {
		turn, next, err := decodeReplayAudioTurn(path, actions, position, len(turns)+1)
		if err != nil {
			return nil, err
		}
		turns = append(turns, turn)
		position = next
	}
	return turns, nil
}

func decodeReplayAudioTurn(path string, actions []gwtesting.CapturedSessionEvent, position, number int) (providersession.AudioInput, int, error) {
	pcm, position, err := collectReplayAudio(path, actions, position, number)
	if err != nil {
		return providersession.AudioInput{}, position, err
	}
	if position >= len(actions) {
		return providersession.AudioInput{}, position, fmt.Errorf("replay session capture %s has an incomplete recorded audio turn %d: expected %s after its recorded %s event(s)", path, number, inputAudioCommitEvent, inputAudioAppendEvent)
	}
	if err := validateReplayEvent(path, actions[position], inputAudioCommitEvent); err != nil {
		return providersession.AudioInput{}, position, err
	}
	position++
	if position >= len(actions) {
		return providersession.AudioInput{}, position, fmt.Errorf("replay session capture %s has an incomplete recorded audio turn %d: expected %s after its recorded %s", path, number, responseCreateEvent, inputAudioCommitEvent)
	}
	if err := validateReplayEvent(path, actions[position], responseCreateEvent); err != nil {
		return providersession.AudioInput{}, position, err
	}
	position++
	if pcm.Len() == 0 {
		return providersession.AudioInput{}, position, fmt.Errorf("replay session capture %s has an empty recorded audio turn %d", path, number)
	}
	return providersession.AudioInput{AfterCompletedTurns: number - 1, PCM: pcm.Bytes(), EndOfTurn: true}, position, nil
}

func collectReplayAudio(path string, actions []gwtesting.CapturedSessionEvent, position, number int) (bytes.Buffer, int, error) {
	var pcm bytes.Buffer
	if position >= len(actions) || actions[position].Type != inputAudioAppendEvent {
		sequence := 0
		if position < len(actions) {
			sequence = actions[position].Sequence
		}
		return pcm, position, fmt.Errorf("replay session capture %s has an ambiguous recorded client action at sequence %d: expected %s to begin audio turn %d", path, sequence, inputAudioAppendEvent, number)
	}
	for position < len(actions) && actions[position].Type == inputAudioAppendEvent {
		chunk, err := parseReplayAudioAppendPCM(path, actions[position])
		if err != nil {
			return pcm, position, err
		}
		_, _ = pcm.Write(chunk)
		position++
	}
	return pcm, position, nil
}

func parseReplayAudioAppendPCM(path string, record gwtesting.CapturedSessionEvent) ([]byte, error) {
	payload := replayRecordPayload(record)
	if len(payload) == 0 {
		return nil, fmt.Errorf("replay session capture %s: recorded %s at sequence %d has no payload", path, inputAudioAppendEvent, record.Sequence)
	}
	var envelope struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("replay session capture %s: decode recorded %s at sequence %d: %w", path, inputAudioAppendEvent, record.Sequence, err)
	}
	if envelope.Type != inputAudioAppendEvent {
		return nil, fmt.Errorf("replay session capture %s: recorded %s at sequence %d has payload type %q", path, inputAudioAppendEvent, record.Sequence, envelope.Type)
	}
	if envelope.Audio == "" {
		return nil, fmt.Errorf("replay session capture %s: recorded %s at sequence %d is missing its audio payload", path, inputAudioAppendEvent, record.Sequence)
	}
	pcm, err := codec.DecodeBase64(envelope.Audio)
	if err != nil {
		return nil, fmt.Errorf("replay session capture %s: decode base64 audio at sequence %d: %w", path, record.Sequence, err)
	}
	return pcm, nil
}

func validateReplayEvent(path string, record gwtesting.CapturedSessionEvent, eventType string) error {
	payload := replayRecordPayload(record)
	if len(payload) == 0 {
		return fmt.Errorf("replay session capture %s: recorded %s at sequence %d has no payload", path, eventType, record.Sequence)
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("replay session capture %s: decode recorded %s at sequence %d: %w", path, eventType, record.Sequence, err)
	}
	if envelope.Type != eventType {
		return fmt.Errorf("replay session capture %s: recorded %s at sequence %d has payload type %q", path, eventType, record.Sequence, envelope.Type)
	}
	return nil
}

func replayRecordPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func replayMaxDuration(path, timing string) time.Duration {
	const grace = 3 * time.Second
	if !strings.EqualFold(strings.TrimSpace(timing), "recorded") {
		return grace
	}
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil || len(loaded.Capture.Records) < 2 {
		return grace
	}
	first, last := loaded.Capture.Records[0].TimestampMs, loaded.Capture.Records[len(loaded.Capture.Records)-1].TimestampMs
	if last <= first {
		return grace
	}
	return time.Duration(last-first)*time.Millisecond + grace
}

func replayHasEvent(path, eventType string, serverOnly bool) bool {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return false
	}
	for _, record := range loaded.Capture.Records {
		if (!serverOnly || record.Direction == gwtesting.DirectionServerToClient) && record.Type == eventType {
			return true
		}
	}
	return false
}

type initialSessionUpdateDialer struct {
	inner   transport.Dialer
	payload []byte
	pace    bool
}

func (d *initialSessionUpdateDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.inner == nil {
		return nil, fmt.Errorf("replay initial session update dialer requires an inner dialer")
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	var wait func() error
	if d.pace {
		if pacer, ok := d.inner.(gwtesting.ReplayOutboundPacer); ok {
			wait = pacer.WaitForNextOutbound
		}
	}
	return &initialSessionUpdateConn{inner: conn, payload: append([]byte(nil), d.payload...), wait: wait}, nil
}

type initialSessionUpdateConn struct {
	inner     transport.Conn
	payload   []byte
	wait      func() error
	mu        sync.Mutex
	handshake bool
}

func (c *initialSessionUpdateConn) ReadMessage() (int, []byte, error) { return c.inner.ReadMessage() }
func (c *initialSessionUpdateConn) WriteMessage(kind int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wait != nil {
		if err := c.wait(); err != nil {
			return err
		}
	}
	if !c.handshake {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("replay provider initial event is not valid JSON: %w", err)
		}
		if envelope.Type != sessionUpdateEvent {
			return fmt.Errorf("replay provider expected initial %s, got %q", sessionUpdateEvent, envelope.Type)
		}
		payload, c.handshake = append([]byte(nil), c.payload...), true
	}
	return c.inner.WriteMessage(kind, payload)
}
func (c *initialSessionUpdateConn) Close() error { return c.inner.Close() }

var _ transport.Dialer = (*initialSessionUpdateDialer)(nil)
var _ transport.Conn = (*initialSessionUpdateConn)(nil)
