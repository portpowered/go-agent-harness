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
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	yamlv3 "gopkg.in/yaml.v3"
)

// Parse decodes one JSON or YAML room document and returns a normalized,
// credential-free contract. The supplied validation options are copied by
// value and are never retained.
func Parse(data []byte, options ...rooms.ValidationOptions) (rooms.Manifest, error) {
	if len(options) > 1 {
		return rooms.Manifest{}, invalid("options", "at most one validation option set is supported", rooms.ErrInvalidManifest)
	}
	var raw rawManifest
	decoder := yamlv3.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return rooms.Manifest{}, invalid("document", "must be one valid JSON or YAML object: "+err.Error(), rooms.ErrInvalidDocument)
	}
	var extra yamlv3.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return rooms.Manifest{}, invalid("document", "must contain exactly one document", rooms.ErrInvalidDocument)
		}
		return rooms.Manifest{}, invalid("document", "must contain exactly one document: "+err.Error(), rooms.ErrInvalidDocument)
	}
	manifest, err := normalize(raw)
	if err != nil {
		return rooms.Manifest{}, err
	}
	var validation rooms.ValidationOptions
	if len(options) == 1 {
		validation = options[0]
	}
	if err := validateBrowserEndpoints(manifest); err != nil {
		return rooms.Manifest{}, err
	}
	if err := manifest.Validate(validation); err != nil {
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

type rawManifest struct {
	SchemaVersion *int             `json:"schema_version" yaml:"schema_version"`
	Room          *rawRoom         `json:"room" yaml:"room"`
	Participants  []rawParticipant `json:"participants" yaml:"participants"`
}

type rawRoom struct {
	MaxTurns    *int          `json:"max_turns" yaml:"max_turns"`
	MaxDuration *string       `json:"max_duration" yaml:"max_duration"`
	Interactive *bool         `json:"interactive" yaml:"interactive"`
	Recording   *rawRecording `json:"recording" yaml:"recording"`
}

type rawRecording struct {
	Enabled   *bool   `json:"enabled" yaml:"enabled"`
	Directory *string `json:"directory" yaml:"directory"`
}

type rawParticipant struct {
	Kind          *string          `json:"kind" yaml:"kind"`
	ID            *string          `json:"id" yaml:"id"`
	SystemPrompt  *string          `json:"system_prompt" yaml:"system_prompt"`
	OpeningPrompt *string          `json:"opening_prompt" yaml:"opening_prompt"`
	Provider      *string          `json:"provider" yaml:"provider"`
	Model         *string          `json:"model" yaml:"model"`
	APIKeyEnv     *string          `json:"api_key_env" yaml:"api_key_env"`
	Voice         *string          `json:"voice" yaml:"voice"`
	Tools         *[]string        `json:"tools" yaml:"tools"`
	BrowserTools  *rawBrowserTools `json:"browserTools" yaml:"browserTools"`
	InputDevice   *string          `json:"input_device" yaml:"input_device"`
	OutputDevice  *string          `json:"output_device" yaml:"output_device"`
}

type rawBrowserTools struct {
	Backend    *string               `json:"backend" yaml:"backend"`
	Connection *rawBrowserConnection `json:"connection" yaml:"connection"`
	Selection  *rawBrowserSelection  `json:"selection" yaml:"selection"`
	Policy     *rawBrowserPolicy     `json:"policy" yaml:"policy"`
	Limits     *rawBrowserLimits     `json:"limits" yaml:"limits"`
	Recording  *rawBrowserRecording  `json:"recording" yaml:"recording"`
	Replay     *rawBrowserReplay     `json:"replay" yaml:"replay"`
}

type rawBrowserConnection struct {
	CDPURL           *string `json:"cdp_url" yaml:"cdp_url"`
	WSEndpoint       *string `json:"ws_endpoint" yaml:"ws_endpoint"`
	UserDataDir      *string `json:"user_data_dir" yaml:"user_data_dir"`
	AllowProcessScan *bool   `json:"allow_process_scan" yaml:"allow_process_scan"`
	AllowRemoteCDP   *bool   `json:"allow_remote_cdp" yaml:"allow_remote_cdp"`
}

type rawBrowserSelection struct {
	Browser     *string `json:"browser" yaml:"browser"`
	Tab         *string `json:"tab" yaml:"tab"`
	Origin      *string `json:"origin" yaml:"origin"`
	AutoSelect  *string `json:"auto_select" yaml:"auto_select"`
	ActivateTab *bool   `json:"activate_tab" yaml:"activate_tab"`
	Persist     *bool   `json:"persist" yaml:"persist"`
}

type rawBrowserPolicy struct {
	AllowedOrigins    *[]string `json:"allowed_origins" yaml:"allowed_origins"`
	DeniedOrigins     *[]string `json:"denied_origins" yaml:"denied_origins"`
	Approval          *string   `json:"approval" yaml:"approval"`
	CancelOnInterrupt *string   `json:"cancel_on_interrupt" yaml:"cancel_on_interrupt"`
}

type rawBrowserLimits struct {
	InvocationTimeout  *string `json:"invocation_timeout" yaml:"invocation_timeout"`
	MaxInputBytes      *int    `json:"max_input_bytes" yaml:"max_input_bytes"`
	MaxResultBytes     *int    `json:"max_result_bytes" yaml:"max_result_bytes"`
	SerializePerTarget *bool   `json:"serialize_per_target" yaml:"serialize_per_target"`
}

type rawBrowserRecording struct {
	Enabled           *bool `json:"enabled" yaml:"enabled"`
	IncludeArguments  *bool `json:"include_arguments" yaml:"include_arguments"`
	IncludeResults    *bool `json:"include_results" yaml:"include_results"`
	RedactURLQuery    *bool `json:"redact_url_query" yaml:"redact_url_query"`
	RedactURLFragment *bool `json:"redact_url_fragment" yaml:"redact_url_fragment"`
}

type rawBrowserReplay struct {
	Path   *string `json:"path" yaml:"path"`
	Strict *bool   `json:"strict" yaml:"strict"`
}

func normalize(raw rawManifest) (rooms.Manifest, error) {
	if raw.SchemaVersion == nil {
		return rooms.Manifest{}, invalid("schema_version", "must be provided", rooms.ErrUnsupportedSchema)
	}
	if *raw.SchemaVersion != rooms.SchemaVersion {
		return rooms.Manifest{}, invalid("schema_version", fmt.Sprintf("must be %d", rooms.SchemaVersion), rooms.ErrUnsupportedSchema)
	}
	if raw.Room == nil {
		return rooms.Manifest{}, invalid("room", "must be provided", rooms.ErrMissingBound)
	}
	roomValue, err := normalizeRoom(raw.Room)
	if err != nil {
		return rooms.Manifest{}, err
	}
	participants, err := normalizeParticipants(raw.Participants)
	if err != nil {
		return rooms.Manifest{}, err
	}
	return rooms.Manifest{SchemaVersion: *raw.SchemaVersion, Room: roomValue, Participants: participants}, nil
}

func normalizeRoom(raw *rawRoom) (rooms.Room, error) {
	roomValue := rooms.Room{}
	if raw.MaxTurns != nil {
		roomValue.MaxTurns = *raw.MaxTurns
		if roomValue.MaxTurns <= 0 {
			return rooms.Room{}, invalid("room.max_turns", "must be positive", rooms.ErrInvalidBound)
		}
	}
	if raw.MaxDuration != nil {
		duration, err := time.ParseDuration(strings.TrimSpace(*raw.MaxDuration))
		if err != nil || duration <= 0 {
			return rooms.Room{}, invalid("room.max_duration", "must be a positive Go duration", rooms.ErrInvalidBound)
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
		roomValue.Recording = &rooms.RoomRecordingConfig{Enabled: raw.Recording.Enabled, Directory: directory}
	}
	return roomValue, nil
}

func normalizeParticipants(values []rawParticipant) ([]rooms.Participant, error) {
	participants := make([]rooms.Participant, len(values))
	for index, value := range values {
		participant, err := normalizeParticipant(value, index)
		if err != nil {
			return nil, err
		}
		participants[index] = participant
	}
	return participants, nil
}

func normalizeParticipant(raw rawParticipant, index int) (rooms.Participant, error) {
	participant := rooms.Participant{}
	if raw.Kind != nil {
		participant.Kind = normalizeParticipantKind(rooms.ParticipantKind(*raw.Kind))
	} else {
		participant.Kind = rooms.ParticipantKindAgent
	}
	participant.ID = optionalString(raw.ID)
	participant.SystemPrompt = optionalString(raw.SystemPrompt)
	participant.OpeningPrompt = optionalString(raw.OpeningPrompt)
	participant.Provider = strings.ToLower(optionalString(raw.Provider))
	participant.Model = optionalString(raw.Model)
	participant.APIKeyEnv = optionalString(raw.APIKeyEnv)
	participant.Voice = optionalString(raw.Voice)
	participant.InputDevice = optionalString(raw.InputDevice)
	participant.OutputDevice = optionalString(raw.OutputDevice)
	if raw.Tools != nil {
		participant.Tools = make([]string, len(*raw.Tools))
		for i, tool := range *raw.Tools {
			participant.Tools[i] = strings.ToLower(strings.TrimSpace(tool))
		}
	}
	if raw.BrowserTools != nil {
		browser, err := normalizeBrowser(raw.BrowserTools)
		if err != nil {
			return rooms.Participant{}, invalid(fmt.Sprintf("participants[%d].browserTools", index), err.Error(), rooms.ErrInvalidBrowserTools)
		}
		participant.BrowserTools = &browser
	}
	return participant, nil
}

// NormalizeParticipantKind applies the document boundary's compatibility
// spelling before the normalized value enters the public room contract.
func NormalizeParticipantKind(kind rooms.ParticipantKind) rooms.ParticipantKind {
	return normalizeParticipantKind(kind)
}

func normalizeParticipantKind(kind rooms.ParticipantKind) rooms.ParticipantKind {
	switch rooms.ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))) {
	case "", rooms.ParticipantKindAgent:
		return rooms.ParticipantKindAgent
	case rooms.ParticipantKindHuman, rooms.ParticipantKindCustomer:
		return rooms.ParticipantKindHuman
	default:
		return rooms.ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind))))
	}
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func invalid(field, problem string, cause error) error {
	return &rooms.ValidationError{Field: field, Problem: problem, Cause: cause}
}
