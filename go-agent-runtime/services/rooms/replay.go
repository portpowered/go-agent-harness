package rooms

import (
	"encoding/json"
	"time"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

const (
	RoomReplayBundleSchemaVersion = 2
	RoomReplayBundleManifestPath  = "run-manifest.json"
	// RoomEvidenceTimelinePath and RoomEvidenceMixPath are the stable room
	// level evidence names. They are shared by the recorder and replay loader
	// so a bundle never relies on a private implementation constant.
	RoomEvidenceTimelinePath = "room-timeline.jsonl"
	RoomEvidenceMixPath      = "room-mix.wav"
)

type RoomReplayBundleErrorKind string

const (
	RoomReplayBundleMismatch   RoomReplayBundleErrorKind = "mismatch"
	RoomReplayBundleIncomplete RoomReplayBundleErrorKind = "incomplete"
)

type RoomReplayBundleError struct {
	Kind     RoomReplayBundleErrorKind
	Field    string
	Artifact string
	Expected string
	Actual   string
	Err      error
}

func (e *RoomReplayBundleError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "room replay bundle " + string(e.Kind)
	if e.Field != "" {
		message += " field " + quote(e.Field)
	}
	if e.Artifact != "" {
		message += " artifact " + quote(e.Artifact)
	}
	if e.Expected != "" || e.Actual != "" {
		message += ": expected " + e.Expected + ", actual " + e.Actual
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *RoomReplayBundleError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type RoomReplayPCMFormat struct {
	SampleRate      int    `json:"sample_rate"`
	Channels        int    `json:"channels"`
	SampleWidthBits int    `json:"sample_width_bits"`
	SampleWidthBit  int    `json:"sample_width_bit,omitempty"`
	ByteOrder       string `json:"byte_order"`
	Encoding        string `json:"encoding"`
}

type RoomReplayArtifact struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	Owner        string `json:"owner"`
	Path         string `json:"path"`
	AbsolutePath string `json:"-"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	// Empty explicitly distinguishes a valid zero byte stream from a missing
	// artifact. Raw PCM and empty JSONL streams can therefore remain honest.
	Empty bool `json:"empty,omitempty"`
}

type RoomReplayParticipant struct {
	ID                string               `json:"id"`
	Kind              ParticipantKind      `json:"kind"`
	Provider          string               `json:"provider"`
	Model             string               `json:"model"`
	Voice             string               `json:"voice"`
	OpeningPrompt     string               `json:"opening_prompt"`
	SystemPrompt      string               `json:"system_prompt"`
	CapturePath       string               `json:"-"`
	Capture           RoomReplayArtifact   `json:"capture"`
	Artifacts         []RoomReplayArtifact `json:"artifacts"`
	RecordedTurnCount int                  `json:"recorded_turn_count"`
}

type RoomReplayTimelineEvent struct {
	Sequence      int64           `json:"sequence"`
	OffsetMS      int64           `json:"offset_ms"`
	OffsetNanos   int64           `json:"offset_nanos,omitempty"`
	UnixMS        int64           `json:"unix_ms"`
	Type          string          `json:"type"`
	ParticipantID string          `json:"participant_id"`
	Raw           json.RawMessage `json:"-"`
}

type RoomReplayPlan struct {
	BundlePath      string                    `json:"bundle_path"`
	ManifestPath    string                    `json:"manifest_path"`
	SchemaVersion   int                       `json:"schema_version"`
	Finalized       bool                      `json:"finalized"`
	ClockBase       time.Time                 `json:"clock_base"`
	StartedAt       time.Time                 `json:"started_at"`
	EndedAt         time.Time                 `json:"ended_at"`
	PCMFormat       RoomReplayPCMFormat       `json:"pcm_format"`
	Participants    []RoomReplayParticipant   `json:"participants"`
	Timeline        []RoomReplayTimelineEvent `json:"timeline"`
	TimelinePath    string                    `json:"timeline_path"`
	RoomMixPath     string                    `json:"room_mix_path"`
	RoomLatencyPath string                    `json:"room_latency_path,omitempty"`
	Artifacts       []RoomReplayArtifact      `json:"artifacts"`
}

func (p RoomReplayPlan) Participant(id string) (RoomReplayParticipant, bool) {
	for _, participant := range p.Participants {
		if participant.ID == id {
			participant.Artifacts = append([]RoomReplayArtifact(nil), participant.Artifacts...)
			return participant, true
		}
	}
	return RoomReplayParticipant{}, false
}

func (p RoomReplayPlan) Manifest() Manifest {
	manifest := Manifest{SchemaVersion: SchemaVersion, Room: Room{Interactive: true}, Participants: make([]Participant, 0, len(p.Participants))}
	for _, participant := range p.Participants {
		kind := normalizeParticipantKind(participant.Kind)
		provider, model := participant.Provider, participant.Model
		apiKeyEnv, inputDevice, outputDevice := "ROOM_REPLAY", "", ""
		if kind == ParticipantKindHuman {
			provider, model = "", ""
			// Replay admission does not open host devices, but Manifest.Validate
			// still requires explicit selectors for a human participant. These
			// sentinel selectors remain inside the replay-derived manifest and
			// never cross the MediaFactory boundary.
			apiKeyEnv, inputDevice, outputDevice = "", "replay", "replay"
		}
		manifest.Participants = append(manifest.Participants, Participant{Kind: kind, ID: participant.ID, SystemPrompt: participant.SystemPrompt, OpeningPrompt: participant.OpeningPrompt, Provider: provider, Model: model, APIKeyEnv: apiKeyEnv, Voice: participant.Voice, Tools: []string{}, InputDevice: inputDevice, OutputDevice: outputDevice})
	}
	return manifest
}

// AnalysisInput provides the canonical audio-analysis projection for a replay
// owner. The replay loader supplies PCM streams separately from this plan.
type AnalysisInput = roomanalysis.PCM16RoomInput
