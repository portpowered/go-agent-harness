package quicksilver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
)

// ModelCodex is the GPT-Live model a ChatGPT credential can open.
const ModelCodex = "gpt-live-1-codex"

// DefaultVoice is the gpt-live-1-codex default voice.
const DefaultVoice = "cove"

// DelegationClient is the only delegation mode on this route.
const DelegationClient = "client"

// ContextAppendMaxBytes is the largest text one context append carries;
// longer text is split on UTF-8 boundaries into several appends.
const ContextAppendMaxBytes = 500

// Startup-history bounds, as OpenClaw applies them to initial_items
// (extensions/openai/realtime-quicksilver-wire.ts,
// boundOpenAIQuicksilverContextItems): the newest messages win, each is cut
// to MaxInitialItemRunes characters, and the kept text totals at most
// MaxInitialItemsBytes UTF-8 bytes.
const (
	MaxInitialItems      = 16
	MaxInitialItemRunes  = 800
	MaxInitialItemsBytes = 8000
)

// ErrInvalidSessionConfig reports a models.SessionConfig or Options value
// that cannot become a valid session.
var ErrInvalidSessionConfig = errors.New("quicksilver: invalid session config")

// Voices returns the gpt-live-1-codex voices. They differ from the public
// gpt-live-1 voices.
func Voices() []string {
	return []string{"arbor", "breeze", "cove", "ember", "juniper", "maple", "sol", "spruce", "vale"}
}

// SessionConfig is the session object of a call-creation request and of
// session.update. Model is sent only at call creation.
type SessionConfig struct {
	Model        string        `json:"model,omitempty"`
	Instructions string        `json:"instructions"`
	Audio        SessionAudio  `json:"audio"`
	Delegation   Delegation    `json:"delegation"`
	InitialItems []InitialItem `json:"initial_items,omitempty"`
}

// SessionAudio selects the output voice. The media format is negotiated by
// WebRTC, so there is no format field.
type SessionAudio struct {
	Output AudioOutput `json:"output"`
}

// AudioOutput names the output voice.
type AudioOutput struct {
	Voice string `json:"voice"`
}

// Delegation is always client delegation. AckFiller false stops the model
// from speaking a filler acknowledgement when it delegates, for hosts that
// control input themselves.
type Delegation struct {
	Type      string `json:"type"`
	AckFiller *bool  `json:"ack_filler,omitempty"`
}

// InitialItem is one message of startup history.
type InitialItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []ContentPart `json:"content"`
}

// Options are the settings models.SessionConfig has no field for. They travel
// as a JSON object in models.SessionConfig.Config. Unknown keys are rejected.
type Options struct {
	// AckFiller false is sent as delegation.ack_filler; nil omits it.
	AckFiller *bool `json:"ack_filler,omitempty"`
	// InitialItems is the startup history.
	InitialItems []InitialText `json:"initial_items,omitempty"`
}

// InitialText is one startup message: a role (developer, user or assistant)
// and its text.
type InitialText struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// BuildSession turns cfg into the session of a call-creation request.
//
// Model and instructions come from cfg; the voice defaults to cove and must be
// a gpt-live-1-codex voice when the model is gpt-live-1-codex; delegation is
// always client; ack_filler and startup history come from the Options in
// cfg.Config. Audio formats, tools, turn detection, transcription, modalities
// and reasoning effort have no field on this route and are never sent.
func BuildSession(cfg models.SessionConfig) (SessionConfig, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return SessionConfig{}, invalidConfig("model is required")
	}
	if len(cfg.Instructions) > openailive.MaxInstructionsBytes {
		return SessionConfig{}, invalidConfig("instructions are %d bytes, over the %d-byte limit", len(cfg.Instructions), openailive.MaxInstructionsBytes)
	}
	voice, err := sessionVoice(model, cfg.Voice)
	if err != nil {
		return SessionConfig{}, err
	}
	options, err := ParseOptions(cfg.Config)
	if err != nil {
		return SessionConfig{}, err
	}
	session := SessionConfig{
		Model:        model,
		Instructions: strings.TrimSpace(cfg.Instructions),
		Audio:        SessionAudio{Output: AudioOutput{Voice: voice}},
		Delegation:   Delegation{Type: DelegationClient, AckFiller: options.AckFiller},
	}
	for _, item := range BoundInitialItems(options.InitialItems) {
		part := PartInputText
		if item.Role == RoleAssistant {
			part = PartOutputText
		}
		session.InitialItems = append(session.InitialItems, InitialItem{
			Type: ItemTypeMessage, Role: item.Role,
			Content: []ContentPart{{Type: part, Text: item.Text}},
		})
	}
	return session, nil
}

// NewSessionUpdate wraps session as a session.update for a plain WebSocket.
// The model is dropped: it is fixed by the connection.
func NewSessionUpdate(session SessionConfig) SessionUpdate {
	session.Model = ""
	return SessionUpdate{Session: session}
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
	if options.AckFiller != nil && *options.AckFiller {
		return Options{}, invalidConfig("ack_filler may only be false; omit it for the default")
	}
	for i, item := range options.InitialItems {
		switch item.Role {
		case RoleDeveloper, RoleUser, RoleAssistant:
		default:
			return Options{}, invalidConfig("initial_items[%d]: role %q is not developer, user or assistant", i, item.Role)
		}
		if strings.TrimSpace(item.Text) == "" {
			return Options{}, invalidConfig("initial_items[%d]: text is empty", i)
		}
	}
	return options, nil
}

// BoundInitialItems keeps the newest items that fit the startup-history
// bounds, in their original order: at most MaxInitialItems messages, each
// truncated to MaxInitialItemRunes characters, and MaxInitialItemsBytes
// bytes of text in total. An item cut to nothing is dropped.
func BoundInitialItems(items []InitialText) []InitialText {
	remaining := MaxInitialItemsBytes
	var newestFirst []InitialText
	for i := len(items) - 1; i >= 0 && len(newestFirst) < MaxInitialItems && remaining > 0; i-- {
		text := truncateText(items[i].Text, remaining)
		if text == "" {
			continue
		}
		newestFirst = append(newestFirst, InitialText{Role: items[i].Role, Text: text})
		remaining -= len(text)
	}
	slices.Reverse(newestFirst)
	return newestFirst
}

// truncateText keeps whole characters of text, at most MaxInitialItemRunes of
// them and maxBytes bytes.
func truncateText(text string, maxBytes int) string {
	end := 0
	for runes := 0; end < len(text) && runes < MaxInitialItemRunes; runes++ {
		_, size := utf8.DecodeRuneInString(text[end:])
		if end+size > maxBytes {
			break
		}
		end += size
	}
	return text[:end]
}

// ContextAppends builds the appends that carry text on channel. A non-empty
// delegationItemID answers that delegation (delegation.context.append);
// otherwise the text is session context (session.context.append). Text over
// ContextAppendMaxBytes is split on UTF-8 boundaries, one append per chunk.
func ContextAppends(text string, channel Channel, delegationItemID string) []Event {
	chunks := chunkText(text)
	events := make([]Event, 0, len(chunks))
	for _, chunk := range chunks {
		content := []ContentPart{{Type: PartInputText, Text: chunk}}
		if delegationItemID != "" {
			events = append(events, DelegationContextAppend{DelegationItemID: delegationItemID, Channel: channel, Content: content})
			continue
		}
		events = append(events, SessionContextAppend{Channel: channel, Content: content})
	}
	return events
}

func chunkText(text string) []string {
	if len(text) <= ContextAppendMaxBytes {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		end := min(ContextAppendMaxBytes, len(text))
		for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		chunks = append(chunks, text[:end])
		text = text[end:]
	}
	return chunks
}

func sessionVoice(model, requested string) (string, error) {
	voice := strings.TrimSpace(requested)
	if voice == "" {
		return DefaultVoice, nil
	}
	if model == ModelCodex && !slices.Contains(Voices(), voice) {
		return "", invalidConfig("voice %q is not a %s voice (%s)", voice, ModelCodex, strings.Join(Voices(), ", "))
	}
	return voice, nil
}

func invalidConfig(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSessionConfig, fmt.Sprintf(format, args...))
}
