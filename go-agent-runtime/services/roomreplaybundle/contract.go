// Package roomreplaybundle owns admission of finalized, credential-free room
// replay bundles. Hosts receive immutable projections; parsing and integrity
// policy remain behind the service and its dedicated Wire graph.
package roomreplaybundle

import (
	"encoding/json"
	"fmt"
	"time"

	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const (
	// RoomReplayBundleSchemaVersion is the additive room evidence schema that
	// replay admission understands. Version one remains accepted because the
	// completeness lane evolves the existing room manifest additively.
	RoomReplayBundleSchemaVersion = 2

	// RoomReplayBundleManifestPath is the stable manifest name used by room
	// evidence bundles. Service.Load also accepts this file directly.
	RoomReplayBundleManifestPath = runtimeRooms.RoomReplayBundleManifestPath
)

type roomReplaySentinel string

func (e roomReplaySentinel) Error() string { return string(e) }

const (
	// ErrInvalidRoomReplayBundle identifies a bundle that cannot be used as a
	// replay plan.
	ErrInvalidRoomReplayBundle roomReplaySentinel = "invalid room replay bundle"
	// ErrRoomReplayBundleIncomplete identifies missing or truncated bundle data.
	ErrRoomReplayBundleIncomplete roomReplaySentinel = "room replay bundle incomplete"
	// ErrRoomReplaySourceConflict identifies a mixed live/configured replay.
	ErrRoomReplaySourceConflict roomReplaySentinel = "room replay bundle cannot be combined with room config or manifest"
)

// RoomReplayBundleErrorKind is the stable classification of an admission
// failure. Integrity and schema disagreements are mismatches; missing bytes
// and unfinalized captures are incomplete.
type RoomReplayBundleErrorKind string

const (
	RoomReplayBundleMismatch   RoomReplayBundleErrorKind = "mismatch"
	RoomReplayBundleIncomplete RoomReplayBundleErrorKind = "incomplete"
)

// RoomReplayBundleError carries bounded, non-secret context for an admission
// failure. Expected and Actual contain metadata such as paths, sizes, or
// digests, never artifact payloads or credentials.
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
	label := string(e.Kind)
	if label == "" {
		label = string(RoomReplayBundleMismatch)
	}
	message := "room replay bundle " + label
	if e.Field != "" {
		message += " field " + strconvQuote(e.Field)
	}
	if e.Artifact != "" {
		message += " artifact " + strconvQuote(e.Artifact)
	}
	if e.Expected != "" || e.Actual != "" {
		message += fmt.Sprintf(": expected %s, actual %s", e.Expected, e.Actual)
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

// Is preserves the repository's provider and gateway replay classifications
// while retaining the room-specific admission error for callers that need it.
func (e *RoomReplayBundleError) Is(target error) bool {
	if e == nil {
		return false
	}
	if target == ErrInvalidRoomReplayBundle {
		return e.Kind == RoomReplayBundleMismatch
	}
	if e.Kind == RoomReplayBundleIncomplete {
		return target == ErrRoomReplayBundleIncomplete || target == gateway.ErrReplayIncomplete || target == providers.ErrReplayIncomplete
	}
	return target == gateway.ErrReplayMismatch || target == providers.ErrReplayMismatch
}

func strconvQuote(value string) string {
	return fmt.Sprintf("%q", value)
}

// RoomReplayPCMFormat describes the raw PCM contract declared by a room
// recording. The replay runtime converts the sample rate and channel count to
// the production mixer format after admission.
type RoomReplayPCMFormat struct {
	SampleRate      int
	Channels        int
	SampleWidthBits int
	// SampleWidthBit is retained as an input-compatibility alias for callers
	// that used the original singular field before the public shape settled.
	SampleWidthBit int
	ByteOrder      string
	Encoding       string
}

// RoomReplayArtifact is one validated, bundle-relative file. AbsolutePath is
// resolved once during admission and is never derived from an untrusted path
// again by the replay runtime.
type RoomReplayArtifact struct {
	Name         string
	Role         string
	Owner        string
	Path         string
	AbsolutePath string
	Size         int64
	SHA256       string
}

// RoomReplayParticipant is the immutable normalized participant projection
// used by later room replay composition. CapturePath is empty only for a
// human participant; provider participants always have a strict session
// capture.
type RoomReplayParticipant struct {
	ID                string
	Kind              runtimeRooms.ParticipantKind
	Provider          string
	Model             string
	Voice             string
	OpeningPrompt     string
	SystemPrompt      string
	CapturePath       string
	Capture           RoomReplayArtifact
	Artifacts         []RoomReplayArtifact
	RecordedTurnCount int
}

// RoomReplayTimelineEvent is the validated, lossless projection of one
// room-timeline.jsonl line. Raw retains fields added by the recording lane so
// the scheduler can consume them without a second lossy decode.
type RoomReplayTimelineEvent struct {
	Sequence int64
	OffsetMS int64
	// OffsetNanos retains fractional t_offset_ms values emitted by the room
	// recorder. OffsetMS remains the compatibility projection used by older
	// callers and manifests.
	OffsetNanos   int64
	UnixMS        int64
	Type          string
	ParticipantID string
	Raw           json.RawMessage
}

// RoomReplayPlan is a source-independent replay plan. It owns copies of all
// decoded strings, paths, and JSON lines; changing or replacing the source
// files after this function returns cannot mutate the plan. Callers should
// treat the exported slices as read-only.
type RoomReplayPlan struct {
	BundlePath    string
	ManifestPath  string
	SchemaVersion int
	Finalized     bool
	ClockBase     time.Time
	StartedAt     time.Time
	EndedAt       time.Time
	PCMFormat     RoomReplayPCMFormat
	Participants  []RoomReplayParticipant
	Timeline      []RoomReplayTimelineEvent
	TimelinePath  string
	RoomMixPath   string
	Artifacts     []RoomReplayArtifact
}

// Participant returns a copy of a participant projection by stable ID.
func (p RoomReplayPlan) Participant(id string) (RoomReplayParticipant, bool) {
	for _, participant := range p.Participants {
		if participant.ID == id {
			participant.Artifacts = append([]RoomReplayArtifact(nil), participant.Artifacts...)
			participant.Capture = cloneRoomReplayArtifact(participant.Capture)
			return participant, true
		}
	}
	return RoomReplayParticipant{}, false
}

// Manifest projects the validated replay metadata into the room runtime's
// credential-free participant shape. Provider handshake details remain owned
// by each participant's capture and are loaded by the existing session replay
// planner; this projection supplies only the stable room identity and bundle
// prompts needed by room orchestration.
func (p RoomReplayPlan) Manifest() runtimeRooms.Manifest {
	manifest := runtimeRooms.Manifest{
		SchemaVersion: runtimeRooms.SchemaVersion,
		Room:          runtimeRooms.Room{Interactive: true},
		Participants:  make([]runtimeRooms.Participant, 0, len(p.Participants)),
	}
	for _, participant := range p.Participants {
		kind := runtimeRooms.ParticipantKindNormalizer{}.Normalize(participant.Kind)
		provider := participant.Provider
		model := participant.Model
		if kind == runtimeRooms.ParticipantKindHuman {
			provider = ""
			model = ""
		}
		manifest.Participants = append(manifest.Participants, runtimeRooms.Participant{
			Kind:          kind,
			ID:            participant.ID,
			SystemPrompt:  participant.SystemPrompt,
			OpeningPrompt: participant.OpeningPrompt,
			Provider:      provider,
			Model:         model,
			Voice:         participant.Voice,
			Tools:         []string{},
		})
	}
	return manifest
}

// Service admits one bundle per call. Implementations are stateless and must
// return fresh projections, so concurrent hosts cannot share mutable parser or
// filesystem state through the service graph.
type Service interface {
	Load(string) (RoomReplayPlan, error)
	ValidateOutput(RoomReplayPlan, string) error
}

func cloneRoomReplayArtifact(artifact RoomReplayArtifact) RoomReplayArtifact {
	return artifact
}
