// Package room defines the manifest contract used to configure a bounded
// multi-participant self-play room.
package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	yamlv3 "gopkg.in/yaml.v3"
)

const (
	// SchemaVersion is the only room manifest schema understood by this
	// package. New fields must be added without changing the meaning of this
	// version, or a later schema version must be introduced.
	SchemaVersion = 1
)

var (
	ErrInvalidManifest        = errors.New("invalid room manifest")
	ErrUnsupportedSchema      = errors.New("unsupported room manifest schema")
	ErrMissingBound           = errors.New("room manifest requires a bound")
	ErrInvalidBound           = errors.New("invalid room manifest bound")
	ErrTooFewParticipants     = errors.New("room manifest requires at least two participants")
	ErrInvalidParticipant     = errors.New("invalid room manifest participant")
	ErrUnknownParticipantKind = errors.New("unknown room manifest participant kind")
	ErrDuplicateParticipant   = errors.New("room manifest contains duplicate participant")
	ErrCredential             = errors.New("invalid room manifest credential")
	ErrUnknownProvider        = errors.New("unknown room manifest provider")
	ErrUnknownModel           = errors.New("unknown room manifest model")
	ErrUnknownTool            = errors.New("unknown room manifest tool")
	ErrUnknownVoice           = errors.New("unknown room manifest voice")
	ErrDuplicateTool          = errors.New("room manifest contains duplicate tool")
	ErrInvalidRecording       = errors.New("invalid room manifest recording")
	ErrInvalidDocument        = errors.New("invalid room manifest document")
)

// ParticipantKind identifies the owner of a room participant's media and
// conversation lifecycle. An omitted kind is normalized to agent for
// compatibility with schema-version-1 manifests written before human
// participants were supported.
type ParticipantKind string

const (
	ParticipantKindAgent    ParticipantKind = "agent"
	ParticipantKindHuman    ParticipantKind = "human"
	ParticipantKindCustomer ParticipantKind = "customer"
)

// NormalizeParticipantKind returns the compatibility default for a manifest
// participant. Customer is accepted as a descriptive alias for human at the
// composition boundary.
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

// ValidationError identifies the exact manifest field that made a document
// unusable. Value is populated only for non-secret identifiers such as
// participant IDs, providers, models, tools, and voices. Credential fields
// deliberately omit their value so an accidentally pasted API key cannot be
// reflected in an error.
type ValidationError struct {
	Field   string
	Value   string
	Problem string
	Cause   error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := fmt.Sprintf("room manifest field %q", e.Field)
	if e.Value != "" {
		message += fmt.Sprintf(" %q", e.Value)
	}
	if e.Problem != "" {
		message += ": " + e.Problem
	}
	return message
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ValidationError) Is(target error) bool {
	if e == nil {
		return false
	}
	return target == ErrInvalidManifest || target == e.Cause
}

// Manifest is the normalized, credential-free representation of a room
// manifest. API keys are intentionally not a field here; callers resolve the
// named environment variable only after this complete document validates.
type Manifest struct {
	SchemaVersion int           `json:"schema_version" yaml:"schema_version"`
	Room          Room          `json:"room" yaml:"room"`
	Participants  []Participant `json:"participants" yaml:"participants"`
}

// Room contains optional positive bounds. An interactive room may omit both
// bounds and remains alive until cancellation or terminal failure.
type Room struct {
	MaxTurns    int           `json:"max_turns,omitempty" yaml:"max_turns,omitempty"`
	MaxDuration time.Duration `json:"-" yaml:"-"`
	// Interactive permits a room without a turn or duration bound. It is used
	// by the bare customer-plus-agent launch and remains opt-in for manifests.
	Interactive bool `json:"interactive,omitempty" yaml:"interactive,omitempty"`
	// Recording is optional for compatibility with existing room documents. A
	// missing policy means recording is enabled with the command/service
	// default destination; an explicit Enabled=false disables room evidence.
	Recording *RoomRecordingConfig `json:"recording,omitempty" yaml:"recording,omitempty"`
}

// RoomRecordingConfig controls the room evidence bundle. Directory is an
// explicit destination from the authoritative room document; the loader
// trims surrounding whitespace, and the command resolves a bare room's
// omitted destination to a fresh directory below the effective config
// directory.
//
// Enabled is a pointer so an omitted field can be distinguished from an
// explicit false without changing the schema of older manifests.
type RoomRecordingConfig struct {
	Enabled   *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Directory string `json:"directory,omitempty" yaml:"directory,omitempty"`
}

// RecordingConfig is a descriptive alias for callers that use the shorter
// configuration terminology.
type RecordingConfig = RoomRecordingConfig

// RecordingEnabled reports whether the manifest requests room evidence. An
// omitted policy and an omitted enabled field both preserve the historical
// recording-on behavior.
func (r Room) RecordingEnabled() bool {
	return r.Recording == nil || r.Recording.Enabled == nil || *r.Recording.Enabled
}

// RecordingDirectory returns the authoritative configured evidence
// destination, or an empty string when the command should choose one.
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
	Kind          ParticipantKind `json:"kind,omitempty" yaml:"kind,omitempty"`
	ID            string          `json:"id" yaml:"id"`
	SystemPrompt  string          `json:"system_prompt" yaml:"system_prompt"`
	OpeningPrompt string          `json:"opening_prompt,omitempty" yaml:"opening_prompt,omitempty"`
	Provider      string          `json:"provider" yaml:"provider"`
	Model         string          `json:"model" yaml:"model"`
	APIKeyEnv     string          `json:"api_key_env" yaml:"api_key_env"`
	Voice         string          `json:"voice,omitempty" yaml:"voice,omitempty"`
	Tools         []string        `json:"tools" yaml:"tools"`
	// BrowserTools is nil unless the manifest explicitly grants this
	// participant the WebMCP browser capability. Its presence, rather than an
	// endpoint value, is the activation switch.
	BrowserTools *BrowserToolsConfig `json:"browserTools,omitempty" yaml:"browserTools,omitempty"`
	// InputDevice and OutputDevice are stable audio.DeviceID values. They are
	// intentionally strings here so the manifest package does not acquire the
	// runtime audio backend; the composition root resolves and validates them.
	InputDevice  string `json:"input_device,omitempty" yaml:"input_device,omitempty"`
	OutputDevice string `json:"output_device,omitempty" yaml:"output_device,omitempty"`
}

// ValidationOptions supplies the registries that are available in the
// composition root. Nil lookup functions mean that this package cannot make
// that particular registry assertion and therefore leaves it to the runtime;
// credential lookup defaults to os.LookupEnv.
type ValidationOptions struct {
	LookupCredential func(string) (string, bool)
	LookupProvider   func(string) bool
	LookupModel      func(provider, model string) bool
	LookupTool       func(string) bool
	LookupVoice      func(provider, model, voice string) bool
}

// ValidationRegistry is a convenient immutable-by-convention adapter for
// callers that have finite provider, model, tool, or voice registries. A nil
// map means that registry is unavailable and is not checked; a non-nil empty
// map means that no value is registered.
type ValidationRegistry struct {
	Providers map[string]struct{}
	Models    map[string]map[string]struct{}
	Tools     map[string]struct{}
	Voices    map[string]map[string]struct{}
}

// Options converts registry sets into validation callbacks. Registry keys are
// expected in their canonical spelling; provider/tool keys are normalized by
// manifest parsing before lookup.
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
		options.LookupVoice = func(provider, model, voice string) bool {
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

// NewValidationRegistry builds a registry from readable slices. It is useful
// at a composition boundary that already owns provider/model/tool discovery.
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

// Validate validates an already normalized manifest. ParseManifest and
// ReadManifest should be preferred for untrusted on-disk input because they
// also reject missing fields that a Go zero value cannot distinguish.
func (m Manifest) Validate(options ...ValidationOptions) error {
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

	resolved := normalizeValidationOptions(options)
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
			if err := participant.BrowserTools.validateAt(field("browserTools")); err != nil {
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
		if resolved.LookupProvider != nil && !resolved.LookupProvider(participant.Provider) {
			return validation(field("provider"), participant.Provider, "is not registered", ErrUnknownProvider)
		}
		if participant.Model == "" {
			return validation(field("model"), "", "must not be empty", ErrInvalidParticipant)
		}
		if resolved.LookupModel != nil && !resolved.LookupModel(participant.Provider, participant.Model) {
			return validation(field("model"), participant.Model, "is not registered for provider "+fmt.Sprintf("%q", participant.Provider), ErrUnknownModel)
		}
		if err := validateCredential(field("api_key_env"), participant.APIKeyEnv, resolved.LookupCredential); err != nil {
			return err
		}
		if participant.Voice != "" && resolved.LookupVoice != nil && !resolved.LookupVoice(participant.Provider, participant.Model, participant.Voice) {
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
			if resolved.LookupTool != nil && !resolved.LookupTool(tool) {
				return validation(toolField, tool, "is not registered", ErrUnknownTool)
			}
		}
	}
	return nil
}

// ParseManifest strictly decodes one JSON or YAML manifest and returns its
// normalized credential-free form. The optional argument keeps the default
// call ergonomic while allowing a composition root to provide registries.
func ParseManifest(data []byte, options ...ValidationOptions) (Manifest, error) {
	if len(options) > 1 {
		return Manifest{}, validation("options", "", "at most one validation option set is supported", ErrInvalidManifest)
	}
	if err := validateManifestBrowserToolsShape(data); err != nil {
		return Manifest{}, err
	}
	var raw manifestDocument
	decoder := yamlv3.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return Manifest{}, validation("document", "", "must be one valid JSON or YAML object: "+err.Error(), ErrInvalidDocument)
	}
	var extra yamlv3.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, validation("document", "", "must contain exactly one document", ErrInvalidDocument)
		}
		return Manifest{}, validation("document", "", "must contain exactly one document: "+err.Error(), ErrInvalidDocument)
	}
	manifest, err := normalizeManifest(raw, normalizeValidationOptions(options))
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ReadManifest reads, strictly decodes, and validates a manifest file.
func ReadManifest(path string, options ...ValidationOptions) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read room manifest %q: %w", path, err)
	}
	return ParseManifest(data, options...)
}

type manifestDocument struct {
	SchemaVersion *int                  `json:"schema_version" yaml:"schema_version"`
	Room          *manifestRoomDocument `json:"room" yaml:"room"`
	Participants  []manifestParticipant `json:"participants" yaml:"participants"`
}

type manifestRoomDocument struct {
	MaxTurns    *int                       `json:"max_turns" yaml:"max_turns"`
	MaxDuration *string                    `json:"max_duration" yaml:"max_duration"`
	Interactive *bool                      `json:"interactive" yaml:"interactive"`
	Recording   *manifestRecordingDocument `json:"recording" yaml:"recording"`
}

type manifestRecordingDocument struct {
	Enabled   *bool   `json:"enabled" yaml:"enabled"`
	Directory *string `json:"directory" yaml:"directory"`
}

type manifestParticipant struct {
	Kind          *string               `json:"kind" yaml:"kind"`
	ID            *string               `json:"id" yaml:"id"`
	SystemPrompt  *string               `json:"system_prompt" yaml:"system_prompt"`
	OpeningPrompt *string               `json:"opening_prompt" yaml:"opening_prompt"`
	Provider      *string               `json:"provider" yaml:"provider"`
	Model         *string               `json:"model" yaml:"model"`
	APIKeyEnv     *string               `json:"api_key_env" yaml:"api_key_env"`
	Voice         *string               `json:"voice" yaml:"voice"`
	Tools         *[]string             `json:"tools" yaml:"tools"`
	BrowserTools  *manifestBrowserTools `json:"browserTools" yaml:"browserTools"`
	InputDevice   *string               `json:"input_device" yaml:"input_device"`
	OutputDevice  *string               `json:"output_device" yaml:"output_device"`
}

func normalizeManifest(raw manifestDocument, options ValidationOptions) (Manifest, error) {
	if raw.SchemaVersion == nil {
		return Manifest{}, validation("schema_version", "", fmt.Sprintf("must be %d", SchemaVersion), ErrUnsupportedSchema)
	}
	if *raw.SchemaVersion != SchemaVersion {
		return Manifest{}, validation("schema_version", fmt.Sprint(*raw.SchemaVersion), fmt.Sprintf("must be %d", SchemaVersion), ErrUnsupportedSchema)
	}
	if raw.Room == nil {
		return Manifest{}, validation("room", "", "must be provided with a positive max_turns and/or max_duration", ErrMissingBound)
	}

	room := Room{}
	if raw.Room.MaxTurns != nil {
		room.MaxTurns = *raw.Room.MaxTurns
		if room.MaxTurns <= 0 {
			return Manifest{}, validation("room.max_turns", fmt.Sprint(room.MaxTurns), "must be positive", ErrInvalidBound)
		}
	}
	if raw.Room.MaxDuration != nil {
		durationText := strings.TrimSpace(*raw.Room.MaxDuration)
		duration, err := time.ParseDuration(durationText)
		if err != nil || duration <= 0 {
			return Manifest{}, validation("room.max_duration", "", "must be a positive Go duration such as 30s or 2m", ErrInvalidBound)
		}
		room.MaxDuration = duration
	}
	if raw.Room.Interactive != nil {
		room.Interactive = *raw.Room.Interactive
	}
	if raw.Room.Recording != nil {
		directory := ""
		if raw.Room.Recording.Directory != nil {
			directory = strings.TrimSpace(*raw.Room.Recording.Directory)
		}
		room.Recording = &RoomRecordingConfig{
			Enabled:   raw.Room.Recording.Enabled,
			Directory: directory,
		}
	}
	if room.MaxTurns == 0 && room.MaxDuration == 0 && !room.Interactive {
		return Manifest{}, validation("room", "", "must set a positive max_turns and/or max_duration", ErrMissingBound)
	}
	if len(raw.Participants) < 2 {
		return Manifest{}, validation("participants", "", "must contain at least two participants", ErrTooFewParticipants)
	}

	participants := make([]Participant, len(raw.Participants))
	for index, rawParticipant := range raw.Participants {
		participant := Participant{
			Kind:          NormalizeParticipantKind(ParticipantKind(normalizeString(rawParticipant.Kind))),
			ID:            normalizeString(rawParticipant.ID),
			SystemPrompt:  normalizeString(rawParticipant.SystemPrompt),
			OpeningPrompt: normalizeString(rawParticipant.OpeningPrompt),
			Provider:      strings.ToLower(normalizeString(rawParticipant.Provider)),
			Model:         normalizeString(rawParticipant.Model),
			APIKeyEnv:     normalizeString(rawParticipant.APIKeyEnv),
			Voice:         normalizeString(rawParticipant.Voice),
			InputDevice:   normalizeString(rawParticipant.InputDevice),
			OutputDevice:  normalizeString(rawParticipant.OutputDevice),
		}
		if rawParticipant.Tools != nil {
			participant.Tools = make([]string, len(*rawParticipant.Tools))
			for toolIndex, tool := range *rawParticipant.Tools {
				participant.Tools[toolIndex] = strings.ToLower(strings.TrimSpace(tool))
			}
		}
		if rawParticipant.BrowserTools != nil {
			browserTools, browserToolsErr := normalizeManifestBrowserTools(
				rawParticipant.BrowserTools,
				fmt.Sprintf("participants[%d].browserTools", index),
			)
			if browserToolsErr != nil {
				return Manifest{}, browserToolsErr
			}
			participant.BrowserTools = &browserTools
		}
		participants[index] = participant
	}

	manifest := Manifest{
		SchemaVersion: *raw.SchemaVersion,
		Room:          room,
		Participants:  participants,
	}
	if err := validateRawRequiredFields(raw.Participants, manifest.Participants); err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(options); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateRawRequiredFields(raw []manifestParticipant, normalized []Participant) error {
	for index, rawParticipant := range raw {
		field := func(name string) string { return fmt.Sprintf("participants[%d].%s", index, name) }
		participant := normalized[index]
		if rawParticipant.ID == nil || participant.ID == "" {
			return validation(field("id"), "", "must not be empty", ErrInvalidParticipant)
		}
		if rawParticipant.SystemPrompt == nil || strings.TrimSpace(participant.SystemPrompt) == "" {
			return validation(field("system_prompt"), "", "must not be empty", ErrInvalidParticipant)
		}
		if participant.Kind != ParticipantKindAgent && participant.Kind != ParticipantKindHuman {
			return validation(field("kind"), string(participant.Kind), "must be agent or human", ErrUnknownParticipantKind)
		}
		if participant.Kind == ParticipantKindHuman {
			if rawParticipant.InputDevice == nil || participant.InputDevice == "" {
				return validation(field("input_device"), "", "must name a non-empty device ID", ErrInvalidParticipant)
			}
			if rawParticipant.OutputDevice == nil || participant.OutputDevice == "" {
				return validation(field("output_device"), "", "must name a non-empty device ID", ErrInvalidParticipant)
			}
			if rawParticipant.Tools == nil {
				return validation(field("tools"), "", "must be provided as a list; use [] when no tools are enabled", ErrInvalidParticipant)
			}
			continue
		}
		if rawParticipant.Provider == nil || participant.Provider == "" {
			return validation(field("provider"), "", "must not be empty", ErrInvalidParticipant)
		}
		if rawParticipant.Model == nil || participant.Model == "" {
			return validation(field("model"), "", "must not be empty", ErrInvalidParticipant)
		}
		if rawParticipant.APIKeyEnv == nil || participant.APIKeyEnv == "" {
			return validation(field("api_key_env"), "", "must name a non-empty environment variable", ErrCredential)
		}
		if rawParticipant.Tools == nil {
			return validation(field("tools"), "", "must be provided as a list; use [] when no tools are enabled", ErrInvalidParticipant)
		}
	}
	return nil
}

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateCredential(field, environmentName string, lookup func(string) (string, bool)) error {
	if !environmentNamePattern.MatchString(environmentName) {
		return validation(field, "", "must be a valid environment variable name", ErrCredential)
	}
	value, present := lookup(environmentName)
	if !present || strings.TrimSpace(value) == "" {
		return validation(field, "", "environment variable is unset or empty", ErrCredential)
	}
	return nil
}

func normalizeValidationOptions(options []ValidationOptions) ValidationOptions {
	if len(options) == 0 {
		return ValidationOptions{LookupCredential: os.LookupEnv}
	}
	resolved := options[0]
	if resolved.LookupCredential == nil {
		resolved.LookupCredential = os.LookupEnv
	}
	return resolved
}

func normalizeString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
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

func validation(field, value, problem string, cause error) *ValidationError {
	return &ValidationError{Field: field, Value: value, Problem: problem, Cause: cause}
}
