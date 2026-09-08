package rooms

import (
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = 1

// ParticipantKind identifies who owns a room participant's conversation and
// media lifecycle. Customer is accepted as a compatibility spelling for
// human at the boundary.
type ParticipantKind string

const (
	ParticipantKindAgent    ParticipantKind = "agent"
	ParticipantKindHuman    ParticipantKind = "human"
	ParticipantKindCustomer ParticipantKind = "customer"
)

func normalizeParticipantKind(kind ParticipantKind) ParticipantKind {
	switch ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))) {
	case "", ParticipantKindAgent:
		return ParticipantKindAgent
	case ParticipantKindHuman, ParticipantKindCustomer:
		return ParticipantKindHuman
	default:
		return ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind))))
	}
}

// Manifest is the normalized, credential-free room configuration. API keys
// never enter this value; APIKeyEnv is only an environment variable name.
type Manifest struct {
	SchemaVersion int           `json:"schema_version" yaml:"schema_version"`
	Room          Room          `json:"room" yaml:"room"`
	Participants  []Participant `json:"participants" yaml:"participants"`
}

// Room contains lifecycle bounds and evidence policy.
type Room struct {
	MaxTurns    int                  `json:"max_turns,omitempty" yaml:"max_turns,omitempty"`
	MaxDuration time.Duration        `json:"-" yaml:"-"`
	Interactive bool                 `json:"interactive,omitempty" yaml:"interactive,omitempty"`
	Recording   *RoomRecordingConfig `json:"recording,omitempty" yaml:"recording,omitempty"`
}

type RoomRecordingConfig struct {
	Enabled   *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Directory string `json:"directory,omitempty" yaml:"directory,omitempty"`
}

type RecordingConfig = RoomRecordingConfig

func (r Room) RecordingEnabled() bool {
	return r.Recording == nil || r.Recording.Enabled == nil || *r.Recording.Enabled
}

func (r Room) RecordingDirectory() string {
	if r.Recording == nil {
		return ""
	}
	return strings.TrimSpace(r.Recording.Directory)
}

// Participant is one independently configured room member. Device selectors
// remain strings until the device composition boundary resolves them.
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

// ValidationOptions supplies registries owned by the composition root.
type ValidationOptions struct {
	LookupCredential   func(string) (string, bool)
	LookupProvider     func(string) bool
	LookupModel        func(provider, model string) bool
	LookupTool         func(string) bool
	LookupVoice        func(provider, model, voice string) bool
	AllowMissingOpener bool
}

// ValidationRegistry is a finite registry adapter for deterministic hosts.
type ValidationRegistry struct {
	Providers map[string]struct{}
	Models    map[string]map[string]struct{}
	Tools     map[string]struct{}
	Voices    map[string]map[string]struct{}
}

func (r ValidationRegistry) Options() ValidationOptions {
	options := ValidationOptions{}
	if r.Providers != nil {
		options.LookupProvider = func(provider string) bool { _, ok := r.Providers[provider]; return ok }
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
		options.LookupTool = func(tool string) bool { _, ok := r.Tools[tool]; return ok }
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

// Validate checks an already normalized manifest without reading the host.
func (m Manifest) Validate(options ...ValidationOptions) error {
	if len(options) > 1 {
		return validation("options", "", "at most one validation option set is supported", ErrInvalidManifest)
	}
	if err := validateManifestHeader(m); err != nil {
		return err
	}
	option := validationOption(options)
	hasHuman, hasOpener, err := validateParticipants(m.Participants, option)
	if err != nil {
		return err
	}
	if !option.AllowMissingOpener && !hasHuman && !hasOpener {
		return validation("participants", "", "all-agent room has nobody to speak first", ErrNoRoomOpener)
	}
	return nil
}

func validateManifestHeader(m Manifest) error {
	if m.SchemaVersion != SchemaVersion {
		return validation("schema_version", fmt.Sprint(m.SchemaVersion), fmt.Sprintf("must be %d", SchemaVersion), ErrUnsupportedSchema)
	}
	if m.Room.MaxTurns < 0 || m.Room.MaxDuration < 0 {
		return validation("room", "", "bounds must not be negative", ErrInvalidBound)
	}
	if m.Room.MaxTurns == 0 && m.Room.MaxDuration == 0 && !m.Room.Interactive {
		return validation("room", "", "must set a positive max_turns and/or max_duration", ErrMissingBound)
	}
	if err := validateRecording(m.Room.Recording); err != nil {
		return err
	}
	if len(m.Participants) < 2 {
		return validation("participants", "", "must contain at least two participants", ErrTooFewParticipants)
	}
	return nil
}

func validationOption(options []ValidationOptions) ValidationOptions {
	if len(options) == 1 {
		return options[0]
	}
	return ValidationOptions{}
}

func validateParticipants(participants []Participant, option ValidationOptions) (bool, bool, error) {
	seen := make(map[string]struct{}, len(participants))
	hasHuman, hasOpener := false, false
	for index, participant := range participants {
		field := func(name string) string { return fmt.Sprintf("participants[%d].%s", index, name) }
		kind, err := validateParticipant(participant, field, option, seen)
		if err != nil {
			return false, false, err
		}
		hasHuman = hasHuman || kind == ParticipantKindHuman
		hasOpener = hasOpener || strings.TrimSpace(participant.OpeningPrompt) != ""
	}
	return hasHuman, hasOpener, nil
}

func validateParticipant(participant Participant, field func(string) string, option ValidationOptions, seen map[string]struct{}) (ParticipantKind, error) {
	if strings.TrimSpace(participant.ID) == "" || strings.TrimSpace(participant.SystemPrompt) == "" {
		return "", validation(field("id"), participant.ID, "id and system_prompt must not be empty", ErrInvalidParticipant)
	}
	if _, exists := seen[participant.ID]; exists {
		return "", validation(field("id"), participant.ID, "must be unique", ErrDuplicateParticipant)
	}
	seen[participant.ID] = struct{}{}
	kind := normalizeParticipantKind(participant.Kind)
	if kind != ParticipantKindAgent && kind != ParticipantKindHuman {
		return "", validation(field("kind"), string(participant.Kind), "must be agent or human", ErrUnknownParticipantKind)
	}
	if kind == ParticipantKindHuman {
		if err := validateHumanParticipant(participant, field); err != nil {
			return "", err
		}
	} else if err := validateAgentParticipant(participant, field, option); err != nil {
		return "", err
	}
	if participant.BrowserTools != nil {
		if err := participant.BrowserTools.validateAt(field("browserTools")); err != nil {
			return "", err
		}
	}
	return kind, nil
}

func validateHumanParticipant(participant Participant, field func(string) string) error {
	if participant.Provider != "" || participant.Model != "" || participant.APIKeyEnv != "" || participant.Voice != "" {
		return validation(field("kind"), string(ParticipantKindHuman), "human participants cannot configure provider credentials or voice", ErrInvalidParticipant)
	}
	if strings.TrimSpace(participant.InputDevice) == "" || strings.TrimSpace(participant.OutputDevice) == "" {
		return validation(field("input_device"), "", "human participants require input_device and output_device", ErrInvalidParticipant)
	}
	if participant.Tools == nil || len(participant.Tools) > 0 {
		return validation(field("tools"), "", "human participants require an empty tools list", ErrInvalidParticipant)
	}
	return nil
}

func validateAgentParticipant(participant Participant, field func(string) string, option ValidationOptions) error {
	if participant.Provider == "" || participant.Model == "" {
		return validation(field("provider"), participant.Provider, "agent participants require provider and model", ErrInvalidParticipant)
	}
	if participant.APIKeyEnv == "" || strings.ContainsAny(participant.APIKeyEnv, "= \t\r\n") {
		return validation(field("api_key_env"), "", "must name an environment variable", ErrCredential)
	}
	if err := validateAgentRegistries(participant, field, option); err != nil {
		return err
	}
	if err := validateAgentCredential(participant, field, option); err != nil {
		return err
	}
	return validateAgentTools(participant, field, option)
}

func validateAgentRegistries(participant Participant, field func(string) string, option ValidationOptions) error {
	if option.LookupProvider != nil && !option.LookupProvider(participant.Provider) {
		return validation(field("provider"), participant.Provider, "is not registered", ErrUnknownProvider)
	}
	if option.LookupModel != nil && !option.LookupModel(participant.Provider, participant.Model) {
		return validation(field("model"), participant.Model, "is not registered", ErrUnknownModel)
	}
	if option.LookupVoice != nil && participant.Voice != "" && !option.LookupVoice(participant.Provider, participant.Model, participant.Voice) {
		return validation(field("voice"), participant.Voice, "is not registered", ErrUnknownVoice)
	}
	return nil
}

func validateAgentCredential(participant Participant, field func(string) string, option ValidationOptions) error {
	if option.LookupCredential == nil {
		return nil
	}
	value, ok := option.LookupCredential(participant.APIKeyEnv)
	if !ok || strings.TrimSpace(value) == "" {
		return validation(field("api_key_env"), participant.APIKeyEnv, "credential is unavailable", ErrCredential)
	}
	return nil
}

func validateAgentTools(participant Participant, field func(string) string, option ValidationOptions) error {
	if participant.Tools == nil {
		return validation(field("tools"), "", "must be provided as a list", ErrInvalidParticipant)
	}
	seen := make(map[string]struct{}, len(participant.Tools))
	for index, tool := range participant.Tools {
		if err := validateTool(tool, field("tools"), index, seen, option.LookupTool); err != nil {
			return err
		}
	}
	return nil
}

func validateTool(tool, field string, index int, seen map[string]struct{}, lookup func(string) bool) error {
	toolField := fmt.Sprintf("%s[%d]", field, index)
	if tool == "" {
		return validation(toolField, "", "must not be empty", ErrInvalidParticipant)
	}
	if _, exists := seen[tool]; exists {
		return validation(toolField, tool, "must be unique", ErrDuplicateTool)
	}
	seen[tool] = struct{}{}
	if lookup != nil && !lookup(tool) {
		return validation(toolField, tool, "is not registered", ErrUnknownTool)
	}
	return nil
}

func validateRecording(config *RoomRecordingConfig) error {
	if config == nil {
		return nil
	}
	if config.Enabled == nil && strings.TrimSpace(config.Directory) == "" {
		return nil
	}
	if strings.ContainsAny(config.Directory, "\x00\r\n") {
		return validation("room.recording.directory", "", "must be a valid path", ErrInvalidRecording)
	}
	return nil
}
