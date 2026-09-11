package rooms

import (
	"encoding/json"
	"fmt"
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

// ParticipantKindNormalizer applies schema-version-1 compatibility spellings
// before a participant enters a runtime room contract.
type ParticipantKindNormalizer struct{}
func (ParticipantKindNormalizer) Normalize(kind ParticipantKind) ParticipantKind {
	switch normalized := ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))); normalized {
	case "", ParticipantKindAgent:
		return ParticipantKindAgent
	case ParticipantKindHuman, ParticipantKindCustomer:
		return ParticipantKindHuman
	default:
		return normalized
	}
}
func normalizeParticipantKind(kind ParticipantKind) ParticipantKind {
	return ParticipantKindNormalizer{}.Normalize(kind)
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
// Validate validates an already normalized manifest without reading host
// configuration. Parse and Read in the manifest provider should be preferred
// for untrusted on-disk input because they also reject omitted required
// document fields that a Go zero value cannot distinguish.
func (m Manifest) Validate(options ...ValidationOptions) error {
	if len(options) > 1 {
		return validation("options", "", "at most one validation option set is supported", ErrInvalidManifest)
	}
	if err := validateManifestSchema(m.SchemaVersion); err != nil {
		return err
	}
	if err := m.Room.validate(); err != nil {
		return err
	}
	if err := validateParticipants(m.Participants, validationOption(options)); err != nil {
		return err
	}
	if !validationOption(options).AllowMissingOpener {
		if err := validateRoomHasOpener(m.Participants); err != nil {
			return err
		}
	}
	return nil
}
func validateManifestSchema(schemaVersion int) error {
	if schemaVersion == SchemaVersion {
		return nil
	}
	return validation("schema_version", fmt.Sprint(schemaVersion), fmt.Sprintf("must be %d", SchemaVersion), ErrUnsupportedSchema)
}
func (r Room) validate() error {
	if r.MaxTurns < 0 {
		return validation("room.max_turns", fmt.Sprint(r.MaxTurns), "must be positive", ErrInvalidBound)
	}
	if r.MaxDuration < 0 {
		return validation("room.max_duration", "", "must be a positive duration", ErrInvalidBound)
	}
	if err := validateRoomRecording(r.Recording); err != nil {
		return err
	}
	if r.MaxTurns == 0 && r.MaxDuration == 0 && !r.Interactive {
		return validation("room", "", "must set a positive max_turns and/or max_duration", ErrMissingBound)
	}
	return nil
}
func validateParticipants(participants []Participant, option ValidationOptions) error {
	if len(participants) < 2 {
		return validation("participants", "", "must contain at least two participants", ErrTooFewParticipants)
	}
	seenIDs := make(map[string]struct{}, len(participants))
	for index, participant := range participants {
		if _, exists := seenIDs[participant.ID]; exists {
			return validation(participantField(index, "id"), participant.ID, "must be unique", ErrDuplicateParticipant)
		}
		seenIDs[participant.ID] = struct{}{}
		if err := validateParticipant(index, participant, option); err != nil {
			return err
		}
	}
	return nil
}
func validateParticipant(index int, participant Participant, option ValidationOptions) error {
	field := func(name string) string { return participantField(index, name) }
	if strings.TrimSpace(participant.ID) == "" {
		return validation(field("id"), "", "must not be empty", ErrInvalidParticipant)
	}
	if participant.BrowserTools != nil {
		if err := participant.BrowserTools.ValidateAt(field("browserTools")); err != nil {
			return err
		}
	}
	kind := normalizeParticipantKind(participant.Kind)
	if kind != ParticipantKindAgent && kind != ParticipantKindHuman {
		return validation(field("kind"), string(participant.Kind), "must be agent or human", ErrUnknownParticipantKind)
	}
	if strings.TrimSpace(participant.SystemPrompt) == "" {
		return validation(field("system_prompt"), "", "must not be empty", ErrInvalidParticipant)
	}
	if kind == ParticipantKindHuman {
		return validateHumanParticipant(field, kind, participant)
	}
	return validateAgentParticipant(field, participant, option)
}
func validateHumanParticipant(field func(string) string, kind ParticipantKind, participant Participant) error {
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
	return nil
}
func validateAgentParticipant(field func(string) string, participant Participant, option ValidationOptions) error {
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
	return validateParticipantTools(field("tools"), participant.Tools, option.LookupTool)
}
func validateParticipantTools(field string, tools []string, lookup func(string) bool) error {
	if tools == nil {
		return validation(field, "", "must be provided as a list; use [] when no tools are enabled", ErrInvalidParticipant)
	}
	seenTools := make(map[string]struct{}, len(tools))
	for index, tool := range tools {
		toolField := fmt.Sprintf("%s[%d]", field, index)
		if tool == "" {
			return validation(toolField, "", "must not be empty", ErrInvalidParticipant)
		}
		if _, exists := seenTools[tool]; exists {
			return validation(toolField, tool, "must be unique per participant", ErrDuplicateTool)
		}
		seenTools[tool] = struct{}{}
		if lookup != nil && !lookup(tool) {
			return validation(toolField, tool, "is not registered", ErrUnknownTool)
		}
	}
	return nil
}
func participantField(index int, name string) string {
	return fmt.Sprintf("participants[%d].%s", index, name)
}
func validationOption(options []ValidationOptions) ValidationOptions {
	if len(options) == 1 {
		return options[0]
	}
	return ValidationOptions{}
}
func validateCredential(field, environmentName string, lookup func(string) (string, bool)) error {
	if !validEnvironmentName(environmentName) {
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
func validEnvironmentName(name string) bool {
	if len(name) == 0 || !validEnvironmentNameStart(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		if !validEnvironmentNamePart(name[index]) {
			return false
		}
	}
	return true
}
func validEnvironmentNameStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
func validEnvironmentNamePart(value byte) bool {
	return validEnvironmentNameStart(value) || value >= '0' && value <= '9'
}
func validateRoomHasOpener(participants []Participant) error {
	hasHuman, hasOpener := false, false
	for _, participant := range participants {
		if normalizeParticipantKind(participant.Kind) == ParticipantKindHuman {
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
