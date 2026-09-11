package rooms

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1

// ParticipantKind identifies who owns a room participant's conversation and
// media lifecycle. Customer is accepted as a compatibility spelling for
// human at the document boundary.
type ParticipantKind string

const (
	ParticipantKindAgent    ParticipantKind = "agent"
	ParticipantKindHuman    ParticipantKind = "human"
	ParticipantKindCustomer ParticipantKind = "customer"
)

// NormalizeParticipantKind applies the schema-version-1 compatibility
// spelling before a participant enters a runtime room contract.
func NormalizeParticipantKind(kind ParticipantKind) ParticipantKind {
	switch ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))) {
	case "", ParticipantKindAgent:
		return ParticipantKindAgent
	case ParticipantKindHuman, ParticipantKindCustomer:
		return ParticipantKindHuman
	default:
		return ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind))))
	}
}

func normalizeParticipantKind(kind ParticipantKind) ParticipantKind {
	return NormalizeParticipantKind(kind)
}

// Manifest is the normalized, credential-free room configuration. API keys
// never enter this value; APIKeyEnv is only an environment variable name.
type Manifest struct {
	SchemaVersion int           `json:"schema_version" yaml:"schema_version"`
	Room          Room          `json:"room" yaml:"room"`
	Participants  []Participant `json:"participants" yaml:"participants"`
}

// Room contains optional positive bounds. An interactive room may omit both
// bounds and remains alive until cancellation or terminal failure.
type Room struct {
	MaxTurns    int                  `json:"max_turns,omitempty" yaml:"max_turns,omitempty"`
	MaxDuration time.Duration        `json:"-" yaml:"-"`
	Interactive bool                 `json:"interactive,omitempty" yaml:"interactive,omitempty"`
	Recording   *RoomRecordingConfig `json:"recording,omitempty" yaml:"recording,omitempty"`
}

// RoomRecordingConfig controls the room evidence bundle. Directory is an
// explicit destination from the authoritative room document; the loader
// trims surrounding whitespace.
type RoomRecordingConfig struct {
	Enabled   *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Directory string `json:"directory,omitempty" yaml:"directory,omitempty"`
}

// RecordingConfig is a descriptive alias for callers using shorter
// configuration terminology.
type RecordingConfig = RoomRecordingConfig

// RecordingEnabled reports whether the manifest requests room evidence. An
// omitted policy and an omitted enabled field preserve recording-on behavior.
func (r Room) RecordingEnabled() bool {
	return r.Recording == nil || r.Recording.Enabled == nil || *r.Recording.Enabled
}

// RecordingDirectory returns the configured evidence destination, or an
// empty string when the host should choose one.
func (r Room) RecordingDirectory() string {
	if r.Recording == nil {
		return ""
	}
	return strings.TrimSpace(r.Recording.Directory)
}

// MarshalJSON keeps normalized output human-readable while retaining the
// time.Duration representation used by the runner internally.
func (r Room) MarshalJSON() ([]byte, error) {
	type roomJSON struct {
		MaxTurns    int                  `json:"max_turns,omitempty"`
		MaxDuration string               `json:"max_duration,omitempty"`
		Interactive bool                 `json:"interactive,omitempty"`
		Recording   *RoomRecordingConfig `json:"recording,omitempty"`
	}
	output := roomJSON{MaxTurns: r.MaxTurns, Interactive: r.Interactive, Recording: r.Recording}
	if r.MaxDuration > 0 {
		output.MaxDuration = r.MaxDuration.String()
	}
	return json.Marshal(output)
}

// MarshalYAML keeps normalized output human-readable while retaining the
// time.Duration representation used by the runner internally.
func (r Room) MarshalYAML() (any, error) {
	type roomYAML struct {
		MaxTurns    int                  `yaml:"max_turns,omitempty"`
		MaxDuration string               `yaml:"max_duration,omitempty"`
		Interactive bool                 `yaml:"interactive,omitempty"`
		Recording   *RoomRecordingConfig `yaml:"recording,omitempty"`
	}
	output := roomYAML{MaxTurns: r.MaxTurns, Interactive: r.Interactive, Recording: r.Recording}
	if r.MaxDuration > 0 {
		output.MaxDuration = r.MaxDuration.String()
	}
	return output, nil
}

// Participant is one independently configured room member. APIKeyEnv is only
// an environment variable name, never the resolved credential value.
type Participant struct {
	Kind          ParticipantKind     `json:"kind,omitempty" yaml:"kind,omitempty"`
	ID            string              `json:"id" yaml:"id"`
	SystemPrompt  string              `json:"system_prompt" yaml:"system_prompt"`
	OpeningPrompt string              `json:"opening_prompt,omitempty" yaml:"opening_prompt,omitempty"`
	Provider      string              `json:"provider" yaml:"provider"`
	Model         string              `json:"model" yaml:"model"`
	APIKeyEnv     string              `json:"api_key_env" yaml:"api_key_env"`
	Voice         string              `json:"voice,omitempty" yaml:"voice,omitempty"`
	Tools         []string            `json:"tools" yaml:"tools"`
	BrowserTools  *BrowserToolsConfig `json:"browserTools,omitempty" yaml:"browserTools,omitempty"`
	InputDevice   string              `json:"input_device,omitempty" yaml:"input_device,omitempty"`
	OutputDevice  string              `json:"output_device,omitempty" yaml:"output_device,omitempty"`
}

// ValidationOptions supplies the registries available at the composition
// root. A nil LookupCredential intentionally means that this validation only
// checks the credential name; host environment access is never implicit in
// the runtime package.
type ValidationOptions struct {
	LookupCredential   func(string) (string, bool)
	LookupProvider     func(string) bool
	LookupModel        func(provider, model string) bool
	LookupTool         func(string) bool
	LookupVoice        func(provider, model, voice string) bool
	AllowMissingOpener bool
}

// ValidationRegistry is a finite registry adapter for deterministic hosts.
// A nil map means that registry is unavailable and is not checked; a non-nil
// empty map means that no value is registered.
type ValidationRegistry struct {
	Providers map[string]struct{}
	Models    map[string]map[string]struct{}
	Tools     map[string]struct{}
	Voices    map[string]map[string]struct{}
}

// Options converts registry sets into validation callbacks. Registry keys are
// expected in their canonical spelling; provider and tool keys are normalized
// by document parsing before lookup.
func (r ValidationRegistry) Options() ValidationOptions {
	options := ValidationOptions{}
	if r.Providers != nil {
		options.LookupProvider = func(provider string) bool {
			_, ok := r.Providers[provider]
			return ok
		}
	}
	if r.Models != nil {
		options.LookupModel = func(provider, model string) bool {
			models, ok := r.Models[provider]
			if !ok {
				return false
			}
			_, ok = models[model]
			return ok
		}
	}
	if r.Tools != nil {
		options.LookupTool = func(tool string) bool {
			_, ok := r.Tools[tool]
			return ok
		}
	}
	if r.Voices != nil {
		options.LookupVoice = func(provider, _, voice string) bool {
			voices, ok := r.Voices[provider]
			if !ok {
				return false
			}
			_, ok = voices[voice]
			return ok
		}
	}
	return options
}

// NewValidationRegistry builds a registry from readable slices. It copies the
// provided values so later caller mutations do not change the registry sets.
func NewValidationRegistry(providers []string, models map[string][]string, tools []string, voices map[string][]string) ValidationRegistry {
	registry := ValidationRegistry{}
	if providers != nil {
		registry.Providers = make(map[string]struct{}, len(providers))
		for _, provider := range providers {
			registry.Providers[strings.ToLower(strings.TrimSpace(provider))] = struct{}{}
		}
	}
	if models != nil {
		registry.Models = make(map[string]map[string]struct{}, len(models))
		for provider, modelIDs := range models {
			set := make(map[string]struct{}, len(modelIDs))
			for _, model := range modelIDs {
				set[strings.TrimSpace(model)] = struct{}{}
			}
			registry.Models[strings.ToLower(strings.TrimSpace(provider))] = set
		}
	}
	if tools != nil {
		registry.Tools = make(map[string]struct{}, len(tools))
		for _, tool := range tools {
			registry.Tools[strings.ToLower(strings.TrimSpace(tool))] = struct{}{}
		}
	}
	if voices != nil {
		registry.Voices = make(map[string]map[string]struct{}, len(voices))
		for provider, voiceIDs := range voices {
			set := make(map[string]struct{}, len(voiceIDs))
			for _, voice := range voiceIDs {
				set[strings.TrimSpace(voice)] = struct{}{}
			}
			registry.Voices[strings.ToLower(strings.TrimSpace(provider))] = set
		}
	}
	return registry
}

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate validates an already normalized manifest without reading host
// configuration. Parse and Read in the manifest provider should be preferred
// for untrusted on-disk input because they also reject omitted required
// document fields that a Go zero value cannot distinguish.
func (m Manifest) Validate(options ...ValidationOptions) error {
	if len(options) > 1 {
		return validation("options", "", "at most one validation option set is supported", ErrInvalidManifest)
	}
	if m.SchemaVersion != SchemaVersion {
		return validation("schema_version", fmt.Sprint(m.SchemaVersion), fmt.Sprintf("must be %d", SchemaVersion), ErrUnsupportedSchema)
	}
	if m.Room.MaxTurns < 0 {
		return validation("room.max_turns", fmt.Sprint(m.Room.MaxTurns), "must be positive", ErrInvalidBound)
	}
	if m.Room.MaxDuration < 0 {
		return validation("room.max_duration", "", "must be a positive duration", ErrInvalidBound)
	}
	if err := validateRoomRecording(m.Room.Recording); err != nil {
		return err
	}
	if m.Room.MaxTurns == 0 && m.Room.MaxDuration == 0 && !m.Room.Interactive {
		return validation("room", "", "must set a positive max_turns and/or max_duration", ErrMissingBound)
	}
	if len(m.Participants) < 2 {
		return validation("participants", "", "must contain at least two participants", ErrTooFewParticipants)
	}

	option := validationOption(options)
	seenIDs := make(map[string]struct{}, len(m.Participants))
	for index, participant := range m.Participants {
		field := func(name string) string { return fmt.Sprintf("participants[%d].%s", index, name) }
		if strings.TrimSpace(participant.ID) == "" {
			return validation(field("id"), "", "must not be empty", ErrInvalidParticipant)
		}
		if _, exists := seenIDs[participant.ID]; exists {
			return validation(field("id"), participant.ID, "must be unique", ErrDuplicateParticipant)
		}
		seenIDs[participant.ID] = struct{}{}
		if participant.BrowserTools != nil {
			if err := participant.BrowserTools.ValidateAt(field("browserTools")); err != nil {
				return err
			}
		}
		kind := NormalizeParticipantKind(participant.Kind)
		if kind != ParticipantKindAgent && kind != ParticipantKindHuman {
			return validation(field("kind"), string(participant.Kind), "must be agent or human", ErrUnknownParticipantKind)
		}
		if strings.TrimSpace(participant.SystemPrompt) == "" {
			return validation(field("system_prompt"), "", "must not be empty", ErrInvalidParticipant)
		}
		if kind == ParticipantKindHuman {
			if participant.Provider != "" || participant.Model != "" || participant.APIKeyEnv != "" {
				return validation(field("kind"), string(kind), "human participants must not configure a provider, model, or credential", ErrInvalidParticipant)
			}
			if strings.TrimSpace(participant.InputDevice) == "" {
				return validation(field("input_device"), "", "must name a non-empty device ID", ErrInvalidParticipant)
			}
			if strings.TrimSpace(participant.OutputDevice) == "" {
				return validation(field("output_device"), "", "must name a non-empty device ID", ErrInvalidParticipant)
			}
			if participant.Voice != "" {
				return validation(field("voice"), participant.Voice, "human participants must not configure a provider voice", ErrInvalidParticipant)
			}
			if participant.Tools == nil {
				return validation(field("tools"), "", "must be provided as a list; use [] when no tools are enabled", ErrInvalidParticipant)
			}
			if len(participant.Tools) > 0 {
				return validation(field("tools"), "", "human participants cannot enable provider tools", ErrInvalidParticipant)
			}
			continue
		}
		if participant.Provider == "" {
			return validation(field("provider"), "", "must not be empty", ErrInvalidParticipant)
		}
		if option.LookupProvider != nil && !option.LookupProvider(participant.Provider) {
			return validation(field("provider"), participant.Provider, "is not registered", ErrUnknownProvider)
		}
		if participant.Model == "" {
			return validation(field("model"), "", "must not be empty", ErrInvalidParticipant)
		}
		if option.LookupModel != nil && !option.LookupModel(participant.Provider, participant.Model) {
			return validation(field("model"), participant.Model, "is not registered for provider "+fmt.Sprintf("%q", participant.Provider), ErrUnknownModel)
		}
		if err := validateCredential(field("api_key_env"), participant.APIKeyEnv, option.LookupCredential); err != nil {
			return err
		}
		if participant.Voice != "" && option.LookupVoice != nil && !option.LookupVoice(participant.Provider, participant.Model, participant.Voice) {
			return validation(field("voice"), participant.Voice, "is not registered for provider/model", ErrUnknownVoice)
		}
		if participant.Tools == nil {
			return validation(field("tools"), "", "must be provided as a list; use [] when no tools are enabled", ErrInvalidParticipant)
		}
		seenTools := make(map[string]struct{}, len(participant.Tools))
		for toolIndex, tool := range participant.Tools {
			toolField := fmt.Sprintf("%s[%d]", field("tools"), toolIndex)
			if tool == "" {
				return validation(toolField, "", "must not be empty", ErrInvalidParticipant)
			}
			if _, exists := seenTools[tool]; exists {
				return validation(toolField, tool, "must be unique per participant", ErrDuplicateTool)
			}
			seenTools[tool] = struct{}{}
			if option.LookupTool != nil && !option.LookupTool(tool) {
				return validation(toolField, tool, "is not registered", ErrUnknownTool)
			}
		}
	}
	if !option.AllowMissingOpener {
		if err := validateRoomHasOpener(m.Participants); err != nil {
			return err
		}
	}
	return nil
}

func validationOption(options []ValidationOptions) ValidationOptions {
	if len(options) == 1 {
		return options[0]
	}
	return ValidationOptions{}
}

func validateCredential(field, environmentName string, lookup func(string) (string, bool)) error {
	if !environmentNamePattern.MatchString(environmentName) {
		return validation(field, "", "must be a valid environment variable name", ErrCredential)
	}
	if lookup == nil {
		return nil
	}
	value, present := lookup(environmentName)
	if !present || strings.TrimSpace(value) == "" {
		return validation(field, "", "environment variable is unset or empty", ErrCredential)
	}
	return nil
}

func validateRoomHasOpener(participants []Participant) error {
	hasHuman, hasOpener := false, false
	for _, participant := range participants {
		if NormalizeParticipantKind(participant.Kind) == ParticipantKindHuman {
			hasHuman = true
		}
		if strings.TrimSpace(participant.OpeningPrompt) != "" {
			hasOpener = true
		}
	}
	if hasHuman || hasOpener {
		return nil
	}
	return validation(
		"participants", "",
		"all-agent room has nobody to speak first: set opening_prompt on at least one participant (or include a human participant)",
		ErrNoRoomOpener,
	)
}

func validateRoomRecording(recording *RoomRecordingConfig) error {
	if recording == nil {
		return nil
	}
	if recording.Enabled != nil && !*recording.Enabled && strings.TrimSpace(recording.Directory) != "" {
		return validation("room.recording.directory", strings.TrimSpace(recording.Directory), "must be empty when recording is disabled", ErrInvalidRecording)
	}
	return nil
}
