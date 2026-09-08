// Package evidence admits finalized room evidence and produces an immutable
// replay plan. It owns filesystem reads and path checks; replay scheduling
// remains a separate service-local responsibility.
package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
)

const (
	roomMixRole      = "room_mix"
	roomTimelineRole = "room_timeline"
	roomLatencyRole  = "room_latency"
)

func requiredParticipantRoles() []string {
	return []string{"wav", "diagnostics", "deltas", "sent_pcm", "received_pcm", "events", "capture"}
}

// Loader validates one complete bundle before returning. It retains no open
// files or references to mutable decode buffers.
type Loader struct{}

func New() Loader { return Loader{} }

func (Loader) Load(bundle string) (rooms.RoomReplayPlan, error) {
	root, err := filepath.Abs(strings.TrimSpace(bundle))
	if err != nil {
		return rooms.RoomReplayPlan{}, incomplete("bundle", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return rooms.RoomReplayPlan{}, incomplete("bundle", fmt.Errorf("bundle directory is unavailable"))
	}
	manifestPath := filepath.Join(root, rooms.RoomReplayBundleManifestPath)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return rooms.RoomReplayPlan{}, incomplete("manifest", err)
	}
	object, err := decodeObject(data)
	if err != nil {
		return rooms.RoomReplayPlan{}, mismatch("manifest", err)
	}
	plan, err := parseHeader(object, root, manifestPath)
	if err != nil {
		return rooms.RoomReplayPlan{}, err
	}
	participants, participantArtifacts, err := parseParticipants(object)
	if err != nil {
		return rooms.RoomReplayPlan{}, err
	}
	plan.Participants = participants
	refs := participantArtifacts
	roomArtifacts, err := parseRoomArtifacts(object)
	if err != nil {
		return rooms.RoomReplayPlan{}, err
	}
	refs = append(refs, roomArtifacts...)
	validated, err := validateArtifacts(root, refs)
	if err != nil {
		return rooms.RoomReplayPlan{}, err
	}
	assignArtifacts(&plan, validated)
	timeline, err := loadTimeline(plan.TimelinePath, plan.Participants, plan.ClockBase, plan.StartedAt, plan.EndedAt)
	if err != nil {
		return rooms.RoomReplayPlan{}, err
	}
	plan.Timeline = timeline
	return plan, nil
}

func parseHeader(object object, root, manifestPath string) (rooms.RoomReplayPlan, error) {
	schema, schemaOK := integer(object, "schema_version")
	if !schemaOK || (schema != 1 && schema != rooms.RoomReplayBundleSchemaVersion) {
		return rooms.RoomReplayPlan{}, mismatch("schema_version", fmt.Errorf("must be 1 or %d", rooms.RoomReplayBundleSchemaVersion))
	}
	finalized, ok := boolean(object, "finalized")
	if !ok || !finalized {
		return rooms.RoomReplayPlan{}, incomplete("finalized", fmt.Errorf("bundle is not finalized"))
	}
	clockBase, timestampErr := timestamp(object, "clock_base")
	if timestampErr != nil {
		return rooms.RoomReplayPlan{}, mismatch("clock_base", timestampErr)
	}
	started, ended, err := parseHeaderTiming(object, clockBase)
	if err != nil {
		return rooms.RoomReplayPlan{}, err
	}
	if err := validateRecordingHealth(object); err != nil {
		return rooms.RoomReplayPlan{}, err
	}
	format, err := parsePCMFormat(object)
	if err != nil {
		return rooms.RoomReplayPlan{}, err
	}
	return rooms.RoomReplayPlan{
		BundlePath: root, ManifestPath: manifestPath, SchemaVersion: schema,
		Finalized: true, ClockBase: clockBase, StartedAt: started,
		EndedAt: ended, PCMFormat: format,
	}, nil
}

func parseHeaderTiming(object object, clockBase time.Time) (time.Time, time.Time, error) {
	timing, ok := object["timing"]
	if !ok {
		return time.Time{}, time.Time{}, incomplete("timing", fmt.Errorf("timing object is missing"))
	}
	timingObject, err := decodeObject(timing)
	if err != nil {
		return time.Time{}, time.Time{}, mismatch("timing", err)
	}
	started, err := timestamp(timingObject, "started_at")
	if err != nil {
		return time.Time{}, time.Time{}, mismatch("timing.started_at", err)
	}
	ended, err := timestamp(timingObject, "ended_at")
	if err != nil {
		return time.Time{}, time.Time{}, mismatch("timing.ended_at", err)
	}
	if ended.Before(started) || clockBase.Before(started) || clockBase.After(ended) {
		return time.Time{}, time.Time{}, mismatch("timing", fmt.Errorf("timestamps are not ordered"))
	}
	return started, ended, nil
}

func parsePCMFormat(object object) (rooms.RoomReplayPCMFormat, error) {
	raw, ok := object["pcm_format"]
	if !ok {
		return rooms.RoomReplayPCMFormat{}, incomplete("pcm_format", fmt.Errorf("PCM format is missing"))
	}
	value, err := decodeObject(raw)
	if err != nil {
		return rooms.RoomReplayPCMFormat{}, mismatch("pcm_format", err)
	}
	rate, ok := integer(value, "sample_rate_hz")
	if !ok {
		rate, ok = integer(value, "sample_rate")
	}
	channels, channelsOK := integer(value, "channels")
	width, widthOK := integer(value, "sample_width_bits")
	if !ok || !channelsOK || !widthOK || rate <= 0 || channels <= 0 || width != 16 {
		return rooms.RoomReplayPCMFormat{}, mismatch("pcm_format", fmt.Errorf("must describe positive mono or interleaved PCM16"))
	}
	return rooms.RoomReplayPCMFormat{SampleRate: rate, Channels: channels, SampleWidthBits: width, ByteOrder: stringValue(value, "byte_order"), Encoding: stringValue(value, "encoding")}, nil
}

func parseParticipants(object object) ([]rooms.RoomReplayParticipant, []artifactRef, error) {
	values, err := participantValues(object)
	if err != nil {
		return nil, nil, err
	}
	participants := make([]rooms.RoomReplayParticipant, 0, len(values))
	refs := make([]artifactRef, 0, len(values)*participantArtifactRoleCapacity)
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		id, err := participantID(value, index, seen)
		if err != nil {
			return nil, nil, err
		}
		participant, participantRefs, err := parseParticipant(value, id)
		if err != nil {
			return nil, nil, err
		}
		participants = append(participants, participant)
		refs = append(refs, participantRefs...)
	}
	return participants, refs, nil
}

func participantValues(object object) ([]object, error) {
	raw, ok := object["participants"]
	if !ok {
		return nil, incomplete("participants", fmt.Errorf("participants are missing"))
	}
	values, err := participantObjects(raw)
	if err != nil {
		return nil, mismatch("participants", err)
	}
	if len(values) < 2 {
		return nil, incomplete("participants", fmt.Errorf("at least two participants are required"))
	}
	return values, nil
}

func participantID(value object, index int, seen map[string]struct{}) (string, error) {
	id := strings.TrimSpace(stringValue(value, "id"))
	if id == "" {
		return "", incomplete("participants["+strconv.Itoa(index)+"].id", fmt.Errorf("participant ID is missing"))
	}
	if _, exists := seen[id]; exists {
		return "", mismatch("participants.id", fmt.Errorf("duplicate participant %q", id))
	}
	seen[id] = struct{}{}
	return id, nil
}

func parseParticipant(value object, id string) (rooms.RoomReplayParticipant, []artifactRef, error) {
	if err := validateRecordingHealth(value); err != nil {
		return rooms.RoomReplayParticipant{}, nil, err
	}
	kind := roommanifest.NormalizeParticipantKind(rooms.ParticipantKind(stringValue(value, "kind")))
	participant := rooms.RoomReplayParticipant{ID: id, Kind: kind, Provider: stringValue(value, "provider"), Model: stringValue(value, "model"), Voice: stringValue(value, "voice"), OpeningPrompt: stringValue(value, "opening_prompt"), SystemPrompt: stringValue(value, "system_prompt")}
	if turns, ok := integer(value, "completed_turns"); ok {
		participant.RecordedTurnCount = turns
	}
	artifactValues, err := decodeObjectField(value, "artifacts")
	if err != nil {
		return rooms.RoomReplayParticipant{}, nil, incomplete("participants["+id+"].artifacts", err)
	}
	refs, err := parseParticipantArtifacts(id, kind, artifactValues)
	if err != nil {
		return rooms.RoomReplayParticipant{}, nil, err
	}
	return participant, refs, nil
}

func parseParticipantArtifacts(id string, kind rooms.ParticipantKind, values object) ([]artifactRef, error) {
	for _, role := range requiredParticipantRoles() {
		if _, exists := values[role]; !exists && (role != "capture" || kind != rooms.ParticipantKindHuman) {
			return nil, incomplete("participants["+id+"].artifacts."+role, fmt.Errorf("artifact is missing"))
		}
	}
	refs := make([]artifactRef, 0, len(values)+1)
	for role, rawRef := range values {
		ref, err := parseArtifact(rawRef, "participant:"+id+":"+role)
		if err != nil {
			return nil, err
		}
		ref.role, ref.owner = role, "participant:"+id+":"+role
		refs = append(refs, ref)
	}
	if kind != rooms.ParticipantKindHuman {
		capture, ok := values["capture"]
		if !ok {
			return nil, incomplete("participants["+id+"].artifacts.capture", fmt.Errorf("provider capture is missing"))
		}
		ref, err := parseArtifact(capture, "participant:"+id+":capture")
		if err != nil {
			return nil, err
		}
		ref.role, ref.owner = "capture", "participant:"+id+":capture"
		refs = append(refs, ref)
	}
	return refs, nil
}

func validateRecordingHealth(value object) error {
	if raw, ok := value["recording_status"]; ok {
		var status transcript.RecordingStatus
		if err := json.Unmarshal(raw, &status); err != nil {
			return mismatch("recording_status", err)
		}
		if err := status.Validate(); err != nil {
			return mismatch("recording_status", err)
		}
		if status.State == transcript.RecordingStatusPartial {
			return incomplete("recording_status", fmt.Errorf("partial evidence is not replayable: %s", status.Reason))
		}
	}
	if raw, ok := value["degraded_artifacts"]; ok {
		var degraded map[string]string
		if err := json.Unmarshal(raw, &degraded); err != nil {
			return mismatch("degraded_artifacts", err)
		}
		if len(degraded) > 0 {
			return incomplete("degraded_artifacts", fmt.Errorf("evidence contains degraded artifacts"))
		}
	}
	return nil
}

func participantObjects(raw json.RawMessage) ([]object, error) {
	var list []object
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var mapping map[string]object
	if err := json.Unmarshal(raw, &mapping); err != nil {
		return nil, err
	}
	list = make([]object, 0, len(mapping))
	for id, value := range mapping {
		if stringValue(value, "id") == "" {
			value["id"] = json.RawMessage(strconv.Quote(id))
		}
		list = append(list, value)
	}
	return list, nil
}
