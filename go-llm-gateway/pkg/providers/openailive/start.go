package openailive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// Limits the session.start builder enforces before the server would.
const (
	// MaxInstructionsBytes stands in for the 16,384-token instruction limit,
	// whose tokenizer is not published.
	MaxInstructionsBytes = 64 << 10
	// MaxInitialItems is the startup history limit in messages.
	MaxInitialItems = 128
	// MinMaxOutputTokens is the smallest Responses max_output_tokens.
	MinMaxOutputTokens = 16
)

// Initial-item roles, part types and statuses from the startup history spec.
const (
	RoleDeveloper = "developer"
	RoleUser      = "user"
	RoleAssistant = "assistant"

	PartInputText  = "input_text"
	PartText       = "text"
	PartOutputText = "output_text"

	ItemTypeMessage  = "message"
	StatusIncomplete = "incomplete"
	StatusCompleted  = "completed"
)

// ErrInvalidSessionConfig reports a models.SessionConfig or Options value
// that cannot become a valid session.start.
var ErrInvalidSessionConfig = errors.New("openailive: invalid session config")

// Options are the GPT-Live settings that models.SessionConfig has no field
// for. They travel as a JSON object in models.SessionConfig.Config. Unknown
// keys are rejected.
type Options struct {
	// Delegation defaults to {"type":"client"}, which is sent explicitly.
	Delegation *Delegation `json:"delegation,omitempty"`
	// Store keeps the session for forking and recording download.
	Store *bool `json:"store,omitempty"`
	// Input is the text-only startup history.
	Input []InitialItem `json:"input,omitempty"`
}

// BuildSessionStart turns cfg into a session.start event with eventID.
//
// Only documented session keys are produced. Model, instructions (omitted
// when blank), voice (omitted when empty) and one audio format shared by
// input and output come from cfg; delegation, store and startup history come
// from the Options in cfg.Config. Tools, turn detection, transcription,
// modalities and reasoning effort have no GPT-Live field and are never sent.
func BuildSessionStart(eventID string, cfg models.SessionConfig) (SessionStart, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return SessionStart{}, invalidConfig("model is required")
	}
	if len(cfg.Instructions) > MaxInstructionsBytes {
		return SessionStart{}, invalidConfig("instructions are %d bytes, over the %d-byte limit", len(cfg.Instructions), MaxInstructionsBytes)
	}
	format, err := sessionAudioFormat(cfg)
	if err != nil {
		return SessionStart{}, err
	}
	options, err := ParseOptions(cfg.Config)
	if err != nil {
		return SessionStart{}, err
	}
	session := SessionConfig{
		Model:      model,
		Audio:      &SessionAudio{Format: &format},
		Delegation: options.Delegation,
		Input:      options.Input,
		Store:      options.Store,
	}
	if strings.TrimSpace(cfg.Instructions) != "" {
		session.Instructions = cfg.Instructions
	}
	if voice := strings.TrimSpace(cfg.Voice); voice != "" {
		session.Audio.Output = &AudioOutput{Voice: Voice{Name: voice}}
	}
	if session.Delegation == nil {
		session.Delegation = &Delegation{Type: DelegationClient}
	}
	return SessionStart{EventID: eventID, Session: session}, nil
}

// ParseOptions decodes and validates the Options in a raw session Config. An
// empty or null Config gives zero Options.
func ParseOptions(raw json.RawMessage) (Options, error) {
	var options Options
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return options, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		return Options{}, fmt.Errorf("%w: options: %w", ErrInvalidSessionConfig, err)
	}
	if err := validateDelegation(options.Delegation); err != nil {
		return Options{}, err
	}
	if err := validateInput(options.Input); err != nil {
		return Options{}, err
	}
	return options, nil
}

// sessionAudioFormat maps the input and output format pairs to the one
// GPT-Live format. An empty format means PCM16 and a zero rate means the
// format's default rate.
func sessionAudioFormat(cfg models.SessionConfig) (AudioFormat, error) {
	input, err := audioFormat(cfg.InputAudioFormat, cfg.InputAudioSampleRate)
	if err != nil {
		return AudioFormat{}, err
	}
	output, err := audioFormat(cfg.OutputAudioFormat, cfg.OutputAudioSampleRate)
	if err != nil {
		return AudioFormat{}, err
	}
	if input != output {
		return AudioFormat{}, invalidConfig("input audio %s@%d and output audio %s@%d differ; GPT-Live uses one format for both", input.Type, input.Rate, output.Type, output.Rate)
	}
	return input, nil
}

func audioFormat(format models.AudioFormat, rate models.SampleRate) (AudioFormat, error) {
	switch format {
	case models.AudioFormatPCM16, "":
		switch rate {
		case 0, models.SampleRate24000:
			return AudioFormat{Type: AudioTypePCM, Rate: RatePCM24k}, nil
		case models.SampleRate16000:
			return AudioFormat{Type: AudioTypePCM, Rate: RatePCM16k}, nil
		case models.SampleRate8000:
		}
	case models.AudioFormatG711Ulaw:
		if rate == 0 || rate == models.SampleRate8000 {
			return AudioFormat{Type: AudioTypePCMU, Rate: RateG711}, nil
		}
	case models.AudioFormatG711Alaw:
		if rate == 0 || rate == models.SampleRate8000 {
			return AudioFormat{Type: AudioTypePCMA, Rate: RateG711}, nil
		}
	default:
		return AudioFormat{}, invalidConfig("audio format %q is not supported", format)
	}
	return AudioFormat{}, invalidConfig("audio format %q does not support %d Hz", format, rate)
}

func validateDelegation(delegation *Delegation) error {
	if delegation == nil {
		return nil
	}
	switch delegation.Type {
	case DelegationClient:
		if delegation.Responses != nil {
			return invalidConfig("client delegation takes no responses settings")
		}
		return nil
	case DelegationResponses:
		responses := delegation.Responses
		if responses == nil || strings.TrimSpace(responses.Model) == "" {
			return invalidConfig("responses delegation requires responses.model")
		}
		if responses.MaxOutputTokens != nil && *responses.MaxOutputTokens < MinMaxOutputTokens {
			return invalidConfig("responses.max_output_tokens must be at least %d", MinMaxOutputTokens)
		}
		return nil
	default:
		return invalidConfig("delegation type %q is not client or responses", delegation.Type)
	}
}

func validateInput(items []InitialItem) error {
	if len(items) > MaxInitialItems {
		return invalidConfig("input has %d messages, over the %d-message limit", len(items), MaxInitialItems)
	}
	for i, item := range items {
		if err := validateInitialItem(item); err != nil {
			return fmt.Errorf("%w (input[%d])", err, i)
		}
	}
	return nil
}

func validateInitialItem(item InitialItem) error {
	if item.Type != "" && item.Type != ItemTypeMessage {
		return invalidConfig("item type %q is not message", item.Type)
	}
	if item.Status != "" && item.Status != StatusIncomplete && item.Status != StatusCompleted {
		return invalidConfig("item status %q is not incomplete or completed", item.Status)
	}
	if len(item.Content) != 1 {
		return invalidConfig("a startup message has exactly one text part, got %d", len(item.Content))
	}
	part := item.Content[0].Type
	switch item.Role {
	case RoleDeveloper, RoleUser:
		if part == PartInputText {
			return nil
		}
	case RoleAssistant:
		if part == PartText || part == PartOutputText {
			return nil
		}
	default:
		return invalidConfig("role %q is not developer, user or assistant", item.Role)
	}
	return invalidConfig("a %s message cannot use a %q part", item.Role, part)
}

func invalidConfig(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSessionConfig, fmt.Sprintf(format, args...))
}
