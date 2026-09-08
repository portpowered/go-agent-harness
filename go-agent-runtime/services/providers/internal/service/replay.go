package service

// This file owns the provider-side replay admission shim. A capture's first
// session.update is protocol data, so replay must send those exact bytes while
// retaining the normal provider session implementation for every later
// message.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const replaySessionUpdate = "session.update"

type replaySessionConfiguration struct {
	payload               []byte
	model                 string
	inputAudioSampleRate  int
	outputAudioSampleRate int
}

func loadReplaySessionConfiguration(path string) (replaySessionConfiguration, error) {
	loaded, err := gatewaytesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return replaySessionConfiguration{}, fmt.Errorf("load replay session capture %s: %w", path, err)
	}
	for _, record := range loaded.Capture.Records {
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != replaySessionUpdate {
			continue
		}
		return decodeReplaySessionConfiguration(path, record)
	}
	return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: missing initial outbound session.update configuration", path)
}

func decodeReplaySessionConfiguration(path string, record gatewaytesting.CapturedSessionEvent) (replaySessionConfiguration, error) {
	payload := replayCaptureRecordPayload(record)
	if len(payload) == 0 {
		return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: initial outbound session.update at sequence %d has no payload", path, record.Sequence)
	}
	var envelope struct {
		Type    string          `json:"type"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: decode initial outbound session.update at sequence %d: %w", path, record.Sequence, err)
	}
	if envelope.Type != replaySessionUpdate {
		return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: initial outbound session.update at sequence %d has payload type %q", path, record.Sequence, envelope.Type)
	}
	if len(envelope.Session) == 0 || string(envelope.Session) == "null" {
		return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: initial outbound session.update at sequence %d is missing the session configuration", path, record.Sequence)
	}
	var session map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Session, &session); err != nil {
		return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: decode session configuration at sequence %d: %w", path, record.Sequence, err)
	}
	if len(session) == 0 {
		return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: initial outbound session.update at sequence %d has an empty session configuration", path, record.Sequence)
	}
	model := ""
	if rawModel, ok := session["model"]; ok {
		if err := json.Unmarshal(rawModel, &model); err != nil {
			return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: session.model at sequence %d must be a string: %w", path, record.Sequence, err)
		}
		model = strings.TrimSpace(model)
	}
	inputRate, outputRate, err := replaySessionAudioSampleRates(session)
	if err != nil {
		return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: audio format at sequence %d: %w", path, record.Sequence, err)
	}
	return replaySessionConfiguration{
		payload: append([]byte(nil), payload...), model: model,
		inputAudioSampleRate: inputRate, outputAudioSampleRate: outputRate,
	}, nil
}

func replayCaptureRecordPayload(record gatewaytesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
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

type replayInitialSessionUpdateDialer struct {
	inner   transport.Dialer
	payload []byte
}

func (d *replayInitialSessionUpdateDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.inner == nil {
		return nil, fmt.Errorf("replay initial session update dialer requires an inner dialer")
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	var wait func() error
	if pacer, ok := d.inner.(gatewaytesting.ReplayOutboundPacer); ok {
		wait = pacer.WaitForNextOutbound
	}
	return &replayInitialSessionUpdateConn{inner: conn, payload: append([]byte(nil), d.payload...), waitForNextOutbound: wait}, nil
}

type replayInitialSessionUpdateConn struct {
	inner               transport.Conn
	payload             []byte
	writeMu             sync.Mutex
	handshakeOn         bool
	waitForNextOutbound func() error
}

func (c *replayInitialSessionUpdateConn) ReadMessage() (int, []byte, error) {
	return c.inner.ReadMessage()
}

func (c *replayInitialSessionUpdateConn) WriteMessage(messageType int, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// A self-driving replay must allow interleaved provider events to reach
	// their reader before admitting the next client frame. The underlying
	// transport still validates the actual payload against the next record.
	if c.waitForNextOutbound != nil {
		if err := c.waitForNextOutbound(); err != nil {
			return err
		}
	}
	if !c.handshakeOn {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("replay provider initial event is not valid JSON: %w", err)
		}
		if envelope.Type != replaySessionUpdate {
			return fmt.Errorf("replay provider expected initial session.update, got %q", envelope.Type)
		}
		payload = append([]byte(nil), c.payload...)
		c.handshakeOn = true
	}
	return c.inner.WriteMessage(messageType, payload)
}

func (c *replayInitialSessionUpdateConn) Close() error { return c.inner.Close() }

var _ transport.Dialer = (*replayInitialSessionUpdateDialer)(nil)
var _ transport.Conn = (*replayInitialSessionUpdateConn)(nil)
