package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type replayConfiguration struct {
	payload               []byte
	model                 string
	inputAudioSampleRate  int
	outputAudioSampleRate int
	initialToolNames      []string
	initialToolsKnown     bool
}

var _ replay.ReplayConfiguration = (*replayConfiguration)(nil)

func (c *replayConfiguration) Payload() []byte {
	if c == nil {
		return nil
	}
	return append([]byte(nil), c.payload...)
}

func (c *replayConfiguration) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

func (c *replayConfiguration) InputAudioSampleRate() int {
	if c == nil {
		return 0
	}
	return c.inputAudioSampleRate
}

func (c *replayConfiguration) OutputAudioSampleRate() int {
	if c == nil {
		return 0
	}
	return c.outputAudioSampleRate
}

func (c *replayConfiguration) InitialToolNames() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.initialToolNames...)
}

func (c *replayConfiguration) InitialToolsKnown() bool {
	return c != nil && c.initialToolsKnown
}

// LoadSessionConfiguration validates the initial provider session.update and
// admits its raw bytes plus the small metadata set needed by a live host.
func (s *Service) LoadSessionConfiguration(ctx context.Context, path string) (replay.ReplayConfiguration, error) {
	capture, err := s.admitCapture(ctx, path)
	if err != nil {
		return nil, err
	}
	return parseReplayConfiguration(path, capture.Records)
}

func (s *Service) admitCapture(ctx context.Context, path string) (gatewaytesting.SessionCapture, error) {
	if err := replayContextError(ctx); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	loaded, err := loadReplayCapture(ctx, capturePath)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("load replay session capture %s: %w", path, err)
	}
	return loaded.Capture, nil
}

func parseReplayConfiguration(sourcePath string, records []gatewaytesting.CapturedSessionEvent) (replay.ReplayConfiguration, error) {
	sawClientAction := false
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		if record.Type != replaySessionUpdate {
			sawClientAction = true
			continue
		}
		if sawClientAction {
			return nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d must be the first client action", sourcePath, replaySessionUpdate, record.Sequence)
		}
		return parseReplayConfigurationRecord(sourcePath, record)
	}
	return nil, fmt.Errorf("replay session capture %s: missing initial outbound %s configuration", sourcePath, replaySessionUpdate)
}

func parseReplayConfigurationRecord(sourcePath string, record gatewaytesting.CapturedSessionEvent) (replay.ReplayConfiguration, error) {
	payload := replayRecordPayload(record)
	if len(payload) == 0 {
		return nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d has no payload", sourcePath, replaySessionUpdate, record.Sequence)
	}
	var envelope struct {
		Type    string          `json:"type"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("replay session capture %s: decode initial outbound %s at sequence %d: %w", sourcePath, replaySessionUpdate, record.Sequence, err)
	}
	if envelope.Type != replaySessionUpdate {
		return nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d has payload type %q", sourcePath, replaySessionUpdate, record.Sequence, envelope.Type)
	}
	session, err := replaySessionObject(sourcePath, record.Sequence, envelope.Session)
	if err != nil {
		return nil, err
	}
	model, err := replaySessionModel(sourcePath, record.Sequence, session)
	if err != nil {
		return nil, err
	}
	tools, toolsKnown, err := replaySessionTools(sourcePath, record.Sequence, session)
	if err != nil {
		return nil, err
	}
	inputRate, outputRate, err := replayStrictAudioSampleRates(sourcePath, record.Sequence, session)
	if err != nil {
		return nil, err
	}
	if inputRate > 0 && outputRate > 0 && inputRate != outputRate {
		return nil, fmt.Errorf("replay session capture %s: %w: input=%d Hz output=%d Hz", sourcePath, replay.ErrSessionAudioSampleRateConflict, inputRate, outputRate)
	}
	return &replayConfiguration{
		payload:               append([]byte(nil), payload...),
		model:                 model,
		inputAudioSampleRate:  inputRate,
		outputAudioSampleRate: outputRate,
		initialToolNames:      tools,
		initialToolsKnown:     toolsKnown,
	}, nil
}

func replaySessionObject(path string, sequence int, raw json.RawMessage) (map[string]json.RawMessage, error) {
	if replayJSONMissingOrNull(raw) {
		return nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d is missing the session configuration", path, replaySessionUpdate, sequence)
	}
	var session map[string]json.RawMessage
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, fmt.Errorf("replay session capture %s: decode session configuration at sequence %d: %w", path, sequence, err)
	}
	if len(session) == 0 {
		return nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d has an empty session configuration", path, replaySessionUpdate, sequence)
	}
	return session, nil
}

func replaySessionModel(path string, sequence int, session map[string]json.RawMessage) (string, error) {
	raw, ok := session["model"]
	if !ok || replayJSONMissingOrNull(raw) {
		return "", nil
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		return "", fmt.Errorf("replay session capture %s: session.model at sequence %d must be a string: %w", path, sequence, err)
	}
	return strings.TrimSpace(model), nil
}

func replaySessionTools(path string, sequence int, session map[string]json.RawMessage) ([]string, bool, error) {
	raw, ok := session["tools"]
	if !ok {
		return []string{}, true, nil
	}
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, true, fmt.Errorf("replay session capture %s: session.tools at sequence %d is invalid: %w", path, sequence, err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool.Name); name != "" {
			names = append(names, name)
		}
	}
	return names, true, nil
}

func replayStrictAudioSampleRates(path string, sequence int, session map[string]json.RawMessage) (int, int, error) {
	inputRate, outputRate, err := replayGAAudioSampleRates(session)
	if err != nil {
		return 0, 0, fmt.Errorf("replay session capture %s: audio configuration at sequence %d: %w", path, sequence, err)
	}
	if inputRate <= 0 {
		inputRate, err = replayLegacyAudioRate(session["input_audio_format"])
		if err != nil {
			return 0, 0, fmt.Errorf("replay session capture %s: decode input audio format at sequence %d: %w", path, sequence, err)
		}
	}
	if outputRate <= 0 {
		outputRate, err = replayLegacyAudioRate(session["output_audio_format"])
		if err != nil {
			return 0, 0, fmt.Errorf("replay session capture %s: decode output audio format at sequence %d: %w", path, sequence, err)
		}
	}
	return inputRate, outputRate, nil
}

func replayGAAudioSampleRates(session map[string]json.RawMessage) (int, int, error) {
	raw, ok := session["audio"]
	if !ok || replayJSONMissingOrNull(raw) {
		return 0, 0, nil
	}
	var audio map[string]json.RawMessage
	if err := json.Unmarshal(raw, &audio); err != nil {
		return 0, 0, fmt.Errorf("decode audio configuration: %w", err)
	}
	input, err := replayNestedAudioRate(audio["input"])
	if err != nil {
		return 0, 0, fmt.Errorf("decode input audio configuration: %w", err)
	}
	output, err := replayNestedAudioRate(audio["output"])
	if err != nil {
		return 0, 0, fmt.Errorf("decode output audio configuration: %w", err)
	}
	return input, output, nil
}

func replayNestedAudioRate(raw json.RawMessage) (int, error) {
	if replayJSONMissingOrNull(raw) {
		return 0, nil
	}
	var direction map[string]json.RawMessage
	if err := json.Unmarshal(raw, &direction); err != nil {
		return 0, err
	}
	format := direction["format"]
	if replayJSONMissingOrNull(format) {
		return 0, nil
	}
	var formatObject map[string]json.RawMessage
	if err := json.Unmarshal(format, &formatObject); err != nil {
		return 0, err
	}
	return replayRateValue(formatObject["rate"])
}

func replayLegacyAudioRate(raw json.RawMessage) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, nil
	}
	if trimmed[0] == '"' {
		var codec string
		if err := json.Unmarshal(trimmed, &codec); err != nil {
			return 0, err
		}
		return 0, nil
	}
	var format map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &format); err != nil {
		return 0, err
	}
	return replayRateValue(format["rate"])
}

func replayRateValue(raw json.RawMessage) (int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, nil
	}
	var rate int
	if err := json.Unmarshal(trimmed, &rate); err != nil {
		return 0, fmt.Errorf("rate must be an integer: %w", err)
	}
	if rate < 0 {
		return 0, fmt.Errorf("rate must not be negative")
	}
	return rate, nil
}

func replayJSONMissingOrNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
