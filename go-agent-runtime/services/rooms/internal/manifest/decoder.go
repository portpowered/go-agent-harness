// Package manifest owns untrusted room-document decoding. The public rooms
// package only contains normalized values; filesystem and YAML concerns stay
// behind this service-local boundary.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	yamlv3 "gopkg.in/yaml.v3"
)

// Parse strictly decodes one JSON or YAML room document and returns a
// normalized, credential-free contract. Validation options are used only for
// this call and no resolved credential value is retained.
func Parse(data []byte, options ...rooms.ValidationOptions) (rooms.Manifest, error) {
	if len(options) > 1 {
		return rooms.Manifest{}, invalid("options", "at most one validation option set is supported", rooms.ErrInvalidManifest)
	}
	if err := validateManifestBrowserToolsShape(data); err != nil {
		return rooms.Manifest{}, err
	}
	var raw manifestDocument
	decoder := yamlv3.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return rooms.Manifest{}, invalid("document", "must be one valid JSON or YAML object: "+sanitizeDecodeError(err), rooms.ErrInvalidDocument)
	}
	var extra yamlv3.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return rooms.Manifest{}, invalid("document", "must contain exactly one document", rooms.ErrInvalidDocument)
		}
		return rooms.Manifest{}, invalid("document", "must contain exactly one document: "+sanitizeDecodeError(err), rooms.ErrInvalidDocument)
	}
	manifest, err := normalizeManifest(raw)
	if err != nil {
		return rooms.Manifest{}, err
	}
	if err := manifest.Validate(validationOption(options)); err != nil {
		return rooms.Manifest{}, err
	}
	return manifest, nil
}

// Read loads and parses one room document. It deliberately does not default
// credential lookup; the caller decides which host credential source is valid.
func Read(path string, options ...rooms.ValidationOptions) (rooms.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rooms.Manifest{}, fmt.Errorf("read room manifest %q: %w", path, err)
	}
	return Parse(data, options...)
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

func normalizeManifest(raw manifestDocument) (rooms.Manifest, error) {
	if err := validateRawDocument(raw); err != nil {
		return rooms.Manifest{}, err
	}
	roomValue, err := normalizeRoom(raw.Room)
	if err != nil {
		return rooms.Manifest{}, err
	}
	if err := validateRoomBound(roomValue); err != nil {
		return rooms.Manifest{}, err
	}
	if len(raw.Participants) < 2 {
		return rooms.Manifest{}, invalid("participants", "must contain at least two participants", rooms.ErrTooFewParticipants)
	}
	participants, err := normalizeParticipants(raw.Participants)
	if err != nil {
		return rooms.Manifest{}, err
	}
	manifest := rooms.Manifest{SchemaVersion: *raw.SchemaVersion, Room: roomValue, Participants: participants}
	if err := validateRawRequiredFields(raw.Participants, manifest.Participants); err != nil {
		return rooms.Manifest{}, err
	}
	return manifest, nil
}
func validateRawDocument(raw manifestDocument) error {
	if raw.SchemaVersion == nil || *raw.SchemaVersion != rooms.SchemaVersion {
		return invalid("schema_version", fmt.Sprintf("must be %d", rooms.SchemaVersion), rooms.ErrUnsupportedSchema)
	}
	if raw.Room == nil {
		return invalid("room", "must be provided with a positive max_turns and/or max_duration", rooms.ErrMissingBound)
	}
	return nil
}
func normalizeRoom(raw *manifestRoomDocument) (rooms.Room, error) {
	roomValue := rooms.Room{}
	if raw.MaxTurns != nil {
		roomValue.MaxTurns = *raw.MaxTurns
		if roomValue.MaxTurns <= 0 {
			return rooms.Room{}, invalidValue("room.max_turns", fmt.Sprint(roomValue.MaxTurns), "must be positive", rooms.ErrInvalidBound)
		}
	}
	if raw.MaxDuration != nil {
		duration, err := time.ParseDuration(strings.TrimSpace(*raw.MaxDuration))
		if err != nil || duration <= 0 {
			return rooms.Room{}, invalidValue("room.max_duration", "", "must be a positive Go duration such as 30s or 2m", rooms.ErrInvalidBound)
		}
		roomValue.MaxDuration = duration
	}
	if raw.Interactive != nil {
		roomValue.Interactive = *raw.Interactive
	}
	if raw.Recording != nil {
		directory := ""
		if raw.Recording.Directory != nil {
			directory = strings.TrimSpace(*raw.Recording.Directory)
		}
		roomValue.Recording = &rooms.RoomRecordingConfig{Enabled: cloneBool(raw.Recording.Enabled), Directory: directory}
	}
	return roomValue, nil
}
func validateRoomBound(room rooms.Room) error {
	if room.MaxTurns == 0 && room.MaxDuration == 0 && !room.Interactive {
		return invalid("room", "must set a positive max_turns and/or max_duration", rooms.ErrMissingBound)
	}
	return nil
}
func normalizeParticipants(raw []manifestParticipant) ([]rooms.Participant, error) {
	participants := make([]rooms.Participant, len(raw))
	for index, rawParticipant := range raw {
		participant, err := normalizeParticipant(rawParticipant, index)
		if err != nil {
			return nil, err
		}
		participants[index] = participant
	}
	return participants, nil
}
func normalizeParticipant(raw manifestParticipant, index int) (rooms.Participant, error) {
	participant := rooms.Participant{
		Kind:          NormalizeParticipantKind(rooms.ParticipantKind(normalizeString(raw.Kind))),
		ID:            normalizeString(raw.ID),
		SystemPrompt:  normalizeString(raw.SystemPrompt),
		OpeningPrompt: normalizeString(raw.OpeningPrompt),
		Provider:      strings.ToLower(normalizeString(raw.Provider)),
		Model:         normalizeString(raw.Model),
		APIKeyEnv:     normalizeString(raw.APIKeyEnv),
		Voice:         normalizeString(raw.Voice),
		InputDevice:   normalizeString(raw.InputDevice),
		OutputDevice:  normalizeString(raw.OutputDevice),
	}
	if raw.Tools != nil {
		participant.Tools = make([]string, len(*raw.Tools))
		for toolIndex, tool := range *raw.Tools {
			participant.Tools[toolIndex] = strings.ToLower(strings.TrimSpace(tool))
		}
	}
	if raw.BrowserTools == nil {
		return participant, nil
	}
	browserTools, err := normalizeBrowser(raw.BrowserTools, fmt.Sprintf("participants[%d].browserTools", index))
	if err != nil {
		return rooms.Participant{}, err
	}
	participant.BrowserTools = &browserTools
	return participant, nil
}
func validateRawRequiredFields(raw []manifestParticipant, normalized []rooms.Participant) error {
	for index, rawParticipant := range raw {
		if err := validateRawParticipant(index, rawParticipant, normalized[index]); err != nil {
			return err
		}
	}
	return nil
}
func validateRawParticipant(index int, raw manifestParticipant, participant rooms.Participant) error {
	field := func(name string) string { return fmt.Sprintf("participants[%d].%s", index, name) }
	if raw.ID == nil || participant.ID == "" {
		return invalid(field("id"), "must not be empty", rooms.ErrInvalidParticipant)
	}
	if raw.SystemPrompt == nil || strings.TrimSpace(participant.SystemPrompt) == "" {
		return invalid(field("system_prompt"), "must not be empty", rooms.ErrInvalidParticipant)
	}
	if participant.Kind != rooms.ParticipantKindAgent && participant.Kind != rooms.ParticipantKindHuman {
		return invalid(field("kind"), "must be agent or human", rooms.ErrUnknownParticipantKind)
	}
	if participant.Kind == rooms.ParticipantKindHuman {
		return validateRawHumanParticipant(field, raw, participant)
	}
	return validateRawAgentParticipant(field, raw, participant)
}
func validateRawHumanParticipant(field func(string) string, raw manifestParticipant, participant rooms.Participant) error {
	if raw.InputDevice == nil || participant.InputDevice == "" {
		return invalid(field("input_device"), "must name a non-empty device ID", rooms.ErrInvalidParticipant)
	}
	if raw.OutputDevice == nil || participant.OutputDevice == "" {
		return invalid(field("output_device"), "must name a non-empty device ID", rooms.ErrInvalidParticipant)
	}
	if raw.Tools == nil {
		return invalid(field("tools"), "must be provided as a list; use [] when no tools are enabled", rooms.ErrInvalidParticipant)
	}
	return nil
}
func validateRawAgentParticipant(field func(string) string, raw manifestParticipant, participant rooms.Participant) error {
	if raw.Provider == nil || participant.Provider == "" {
		return invalid(field("provider"), "must not be empty", rooms.ErrInvalidParticipant)
	}
	if raw.Model == nil || participant.Model == "" {
		return invalid(field("model"), "must not be empty", rooms.ErrInvalidParticipant)
	}
	if raw.APIKeyEnv == nil || participant.APIKeyEnv == "" {
		return invalid(field("api_key_env"), "must name a non-empty environment variable", rooms.ErrCredential)
	}
	if raw.Tools == nil {
		return invalid(field("tools"), "must be provided as a list; use [] when no tools are enabled", rooms.ErrInvalidParticipant)
	}
	return nil
}
func normalizeString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// NormalizeParticipantKind applies the document-boundary compatibility
// spelling for service-local consumers that serialize room evidence.
func NormalizeParticipantKind(kind rooms.ParticipantKind) rooms.ParticipantKind {
	switch normalized := rooms.ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))); normalized {
	case "", rooms.ParticipantKindAgent:
		return rooms.ParticipantKindAgent
	case rooms.ParticipantKindHuman, rooms.ParticipantKindCustomer:
		return rooms.ParticipantKindHuman
	default:
		return normalized
	}
}
func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func validationOption(options []rooms.ValidationOptions) rooms.ValidationOptions {
	if len(options) == 1 {
		return options[0]
	}
	return rooms.ValidationOptions{}
}
func decodeTypeLabels() map[string]string {
	return map[string]string{
		"manifestDocument":          "the manifest",
		"manifestRoomDocument":      "room",
		"manifestRecordingDocument": "room.recording",
		"manifestParticipant":       "a participant",
		"manifestBrowserTools":      "a participant's browserTools",
		"manifestBrowserConnection": "a participant's browserTools",
		"manifestBrowserSelection":  "a participant's browserTools",
		"manifestBrowserPolicy":     "a participant's browserTools",
		"manifestBrowserLimits":     "a participant's browserTools",
		"manifestBrowserRecording":  "a participant's browserTools",
		"manifestBrowserReplay":     "a participant's browserTools",
	}
}
func sanitizeDecodeError(err error) string {
	if err == nil {
		return ""
	}
	pattern := regexp.MustCompile(`type [^\s.]+\.(\w+)`)
	labels := decodeTypeLabels()
	return pattern.ReplaceAllStringFunc(err.Error(), func(match string) string {
		name := pattern.FindStringSubmatch(match)[1]
		label, ok := labels[name]
		if !ok {
			label = "the manifest"
		}
		return label
	})
}
func invalid(field, problem string, cause error) error {
	return &rooms.ValidationError{Field: field, Problem: problem, Cause: cause}
}
func invalidValue(field, value, problem string, cause error) error {
	return &rooms.ValidationError{Field: field, Value: value, Problem: problem, Cause: cause}
}

// Provider is the runtime-owned admission implementation exposed through the
// public wire interface. It retains only copied callbacks and no host state.
type Provider struct {
	options rooms.ValidationOptions
}

func NewProvider(options rooms.ValidationOptions) *Provider {
	return &Provider{options: options}
}
func NewProviderFromRegistry(registry rooms.ValidationRegistry, lookupCredential func(string) (string, bool)) *Provider {
	options := cloneValidationRegistry(registry).Options()
	options.LookupCredential = lookupCredential
	return NewProvider(options)
}
func (p *Provider) Parse(data []byte, overrides ...rooms.ValidationOptions) (rooms.Manifest, error) {
	options, err := p.optionsFor(overrides)
	if err != nil {
		return rooms.Manifest{}, err
	}
	return Parse(data, options)
}
func (p *Provider) Read(path string, overrides ...rooms.ValidationOptions) (rooms.Manifest, error) {
	options, err := p.optionsFor(overrides)
	if err != nil {
		return rooms.Manifest{}, err
	}
	return Read(path, options)
}
func (p *Provider) Admit(data []byte, overrides ...rooms.ValidationOptions) (rooms.Manifest, error) {
	return p.Parse(data, overrides...)
}
func (p *Provider) AdmitFile(path string, overrides ...rooms.ValidationOptions) (rooms.Manifest, error) {
	return p.Read(path, overrides...)
}
func (p *Provider) Validate(value rooms.Manifest, overrides ...rooms.ValidationOptions) error {
	options, err := p.optionsFor(overrides)
	if err != nil {
		return err
	}
	return value.Validate(options)
}
func (*Provider) Close() error { return nil }
func (p *Provider) optionsFor(overrides []rooms.ValidationOptions) (rooms.ValidationOptions, error) {
	if p == nil {
		return rooms.ValidationOptions{}, fmt.Errorf("%w: manifest provider is nil", rooms.ErrInvalidManifest)
	}
	if len(overrides) > 1 {
		return rooms.ValidationOptions{}, invalid("options", "at most one validation option set is supported", rooms.ErrInvalidManifest)
	}
	if len(overrides) == 1 {
		return overrides[0], nil
	}
	return p.options, nil
}
func cloneValidationRegistry(value rooms.ValidationRegistry) rooms.ValidationRegistry {
	return rooms.ValidationRegistry{
		Providers: cloneStringSet(value.Providers),
		Models:    cloneNestedStringSet(value.Models),
		Tools:     cloneStringSet(value.Tools),
		Voices:    cloneNestedStringSet(value.Voices),
	}
}
func cloneStringSet(value map[string]struct{}) map[string]struct{} {
	if value == nil {
		return nil
	}
	clone := make(map[string]struct{}, len(value))
	for key := range value {
		clone[key] = struct{}{}
	}
	return clone
}
func cloneNestedStringSet(value map[string]map[string]struct{}) map[string]map[string]struct{} {
	if value == nil {
		return nil
	}
	clone := make(map[string]map[string]struct{}, len(value))
	for key, nested := range value {
		clone[key] = cloneStringSet(nested)
	}
	return clone
}
