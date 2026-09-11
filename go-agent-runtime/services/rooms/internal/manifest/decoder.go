// Package manifest owns untrusted room-document decoding. The public rooms
// package only contains normalized values; filesystem and YAML concerns stay
// behind this service-local boundary.
package manifest

import (
	"bytes"
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
	if err := decoder.Decode(&extra); err != io.EOF {
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
	if raw.SchemaVersion == nil {
		return rooms.Manifest{}, invalid("schema_version", fmt.Sprintf("must be %d", rooms.SchemaVersion), rooms.ErrUnsupportedSchema)
	}
	if *raw.SchemaVersion != rooms.SchemaVersion {
		return rooms.Manifest{}, invalid("schema_version", fmt.Sprintf("must be %d", rooms.SchemaVersion), rooms.ErrUnsupportedSchema)
	}
	if raw.Room == nil {
		return rooms.Manifest{}, invalid("room", "must be provided with a positive max_turns and/or max_duration", rooms.ErrMissingBound)
	}

	roomValue := rooms.Room{}
	if raw.Room.MaxTurns != nil {
		roomValue.MaxTurns = *raw.Room.MaxTurns
		if roomValue.MaxTurns <= 0 {
			return rooms.Manifest{}, invalidValue("room.max_turns", fmt.Sprint(roomValue.MaxTurns), "must be positive", rooms.ErrInvalidBound)
		}
	}
	if raw.Room.MaxDuration != nil {
		durationText := strings.TrimSpace(*raw.Room.MaxDuration)
		duration, err := time.ParseDuration(durationText)
		if err != nil || duration <= 0 {
			return rooms.Manifest{}, invalidValue("room.max_duration", "", "must be a positive Go duration such as 30s or 2m", rooms.ErrInvalidBound)
		}
		roomValue.MaxDuration = duration
	}
	if raw.Room.Interactive != nil {
		roomValue.Interactive = *raw.Room.Interactive
	}
	if raw.Room.Recording != nil {
		directory := ""
		if raw.Room.Recording.Directory != nil {
			directory = strings.TrimSpace(*raw.Room.Recording.Directory)
		}
		roomValue.Recording = &rooms.RoomRecordingConfig{Enabled: cloneBool(raw.Room.Recording.Enabled), Directory: directory}
	}
	if roomValue.MaxTurns == 0 && roomValue.MaxDuration == 0 && !roomValue.Interactive {
		return rooms.Manifest{}, invalid("room", "must set a positive max_turns and/or max_duration", rooms.ErrMissingBound)
	}
	if len(raw.Participants) < 2 {
		return rooms.Manifest{}, invalid("participants", "must contain at least two participants", rooms.ErrTooFewParticipants)
	}

	participants := make([]rooms.Participant, len(raw.Participants))
	for index, rawParticipant := range raw.Participants {
		participant := rooms.Participant{
			Kind:          rooms.NormalizeParticipantKind(rooms.ParticipantKind(normalizeString(rawParticipant.Kind))),
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
			browserTools, err := normalizeBrowser(rawParticipant.BrowserTools, fmt.Sprintf("participants[%d].browserTools", index))
			if err != nil {
				return rooms.Manifest{}, err
			}
			participant.BrowserTools = &browserTools
		}
		participants[index] = participant
	}

	manifest := rooms.Manifest{SchemaVersion: *raw.SchemaVersion, Room: roomValue, Participants: participants}
	if err := validateRawRequiredFields(raw.Participants, manifest.Participants); err != nil {
		return rooms.Manifest{}, err
	}
	return manifest, nil
}

func validateRawRequiredFields(raw []manifestParticipant, normalized []rooms.Participant) error {
	for index, rawParticipant := range raw {
		field := func(name string) string { return fmt.Sprintf("participants[%d].%s", index, name) }
		participant := normalized[index]
		if rawParticipant.ID == nil || participant.ID == "" {
			return invalid(field("id"), "must not be empty", rooms.ErrInvalidParticipant)
		}
		if rawParticipant.SystemPrompt == nil || strings.TrimSpace(participant.SystemPrompt) == "" {
			return invalid(field("system_prompt"), "must not be empty", rooms.ErrInvalidParticipant)
		}
		if participant.Kind != rooms.ParticipantKindAgent && participant.Kind != rooms.ParticipantKindHuman {
			return invalid(field("kind"), "must be agent or human", rooms.ErrUnknownParticipantKind)
		}
		if participant.Kind == rooms.ParticipantKindHuman {
			if rawParticipant.InputDevice == nil || participant.InputDevice == "" {
				return invalid(field("input_device"), "must name a non-empty device ID", rooms.ErrInvalidParticipant)
			}
			if rawParticipant.OutputDevice == nil || participant.OutputDevice == "" {
				return invalid(field("output_device"), "must name a non-empty device ID", rooms.ErrInvalidParticipant)
			}
			if rawParticipant.Tools == nil {
				return invalid(field("tools"), "must be provided as a list; use [] when no tools are enabled", rooms.ErrInvalidParticipant)
			}
			continue
		}
		if rawParticipant.Provider == nil || participant.Provider == "" {
			return invalid(field("provider"), "must not be empty", rooms.ErrInvalidParticipant)
		}
		if rawParticipant.Model == nil || participant.Model == "" {
			return invalid(field("model"), "must not be empty", rooms.ErrInvalidParticipant)
		}
		if rawParticipant.APIKeyEnv == nil || participant.APIKeyEnv == "" {
			return invalid(field("api_key_env"), "must name a non-empty environment variable", rooms.ErrCredential)
		}
		if rawParticipant.Tools == nil {
			return invalid(field("tools"), "must be provided as a list; use [] when no tools are enabled", rooms.ErrInvalidParticipant)
		}
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
	return rooms.NormalizeParticipantKind(kind)
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

var decodeTypePattern = regexp.MustCompile(`type [^\s.]+\.(\w+)`)

var decodeTypeLabels = map[string]string{
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

func sanitizeDecodeError(err error) string {
	if err == nil {
		return ""
	}
	return decodeTypePattern.ReplaceAllStringFunc(err.Error(), func(match string) string {
		name := decodeTypePattern.FindStringSubmatch(match)[1]
		label, ok := decodeTypeLabels[name]
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
