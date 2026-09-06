package evidence

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type artifactRef struct {
	artifact rooms.RoomReplayArtifact
	role     string
	owner    string
}

const roomArtifactRoleCapacity = 3

func parseArtifact(raw json.RawMessage, owner string) (artifactRef, error) {
	value, err := decodeObject(raw)
	if err != nil {
		return artifactRef{}, mismatch(owner, err)
	}
	path := strings.TrimSpace(stringValue(value, "path"))
	if path == "" {
		return artifactRef{}, incomplete(owner, fmt.Errorf("artifact path is missing"))
	}
	size, _ := integer64(value, "size")
	empty := value["empty"]
	var isEmpty bool
	if len(empty) > 0 && json.Unmarshal(empty, &isEmpty) != nil {
		return artifactRef{}, mismatch(owner, fmt.Errorf("empty must be a boolean"))
	}
	return artifactRef{artifact: rooms.RoomReplayArtifact{Name: stringValue(value, "name"), Path: path, Size: size, SHA256: strings.ToLower(strings.TrimSpace(stringValue(value, "sha256"))), Empty: isEmpty}, owner: owner}, nil
}

func parseRoomArtifacts(object object) ([]artifactRef, error) {
	value, ok := object["artifacts"]
	if !ok {
		return nil, incomplete("artifacts", fmt.Errorf("room artifacts are missing"))
	}
	entries, err := decodeObject(value)
	if err != nil {
		return nil, mismatch("artifacts", err)
	}
	refs := make([]artifactRef, 0, roomArtifactRoleCapacity)
	seen := make(map[string]struct{}, roomArtifactRoleCapacity)
	for role, raw := range entries {
		canonicalRole, ok := canonicalRoomArtifactRole(role)
		if !ok {
			continue
		}
		ref, err := parseArtifact(raw, "room:"+role)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[canonicalRole]; duplicate {
			return nil, mismatch("artifacts", fmt.Errorf("duplicate room artifact role %q", canonicalRole))
		}
		seen[canonicalRole] = struct{}{}
		ref.role, ref.owner = canonicalRole, "room:"+canonicalRole
		refs = append(refs, ref)
	}
	if err := validateRoomArtifactRoles(seen); err != nil {
		return nil, err
	}
	return refs, nil
}

func canonicalRoomArtifactRole(role string) (string, bool) {
	switch role {
	case roomMixRole, "room.mix":
		return roomMixRole, true
	case roomTimelineRole, "room.timeline":
		return roomTimelineRole, true
	case roomLatencyRole, "room.latency":
		return roomLatencyRole, true
	default:
		return "", false
	}
}

func validateRoomArtifactRoles(seen map[string]struct{}) error {
	if _, ok := seen[roomMixRole]; !ok {
		return incomplete("artifacts", fmt.Errorf("room mix is required"))
	}
	if _, ok := seen[roomTimelineRole]; !ok {
		return incomplete("artifacts", fmt.Errorf("room timeline and room mix are required"))
	}
	return nil
}

func assignArtifacts(plan *rooms.RoomReplayPlan, refs []rooms.RoomReplayArtifact) {
	if plan == nil {
		return
	}
	for _, artifact := range refs {
		assignRoomArtifact(plan, artifact)
	}
	for i := range plan.Participants {
		assignParticipantArtifacts(&plan.Participants[i], refs)
	}
}

func assignRoomArtifact(plan *rooms.RoomReplayPlan, artifact rooms.RoomReplayArtifact) {
	plan.Artifacts = append(plan.Artifacts, artifact)
	switch artifact.Owner {
	case "room:" + roomTimelineRole:
		plan.TimelinePath = artifact.AbsolutePath
	case "room:" + roomMixRole:
		plan.RoomMixPath = artifact.AbsolutePath
	case "room:" + roomLatencyRole:
		plan.RoomLatencyPath = artifact.AbsolutePath
	}
}

func assignParticipantArtifacts(participant *rooms.RoomReplayParticipant, refs []rooms.RoomReplayArtifact) {
	if participant == nil {
		return
	}
	prefix := "participant:" + participant.ID + ":"
	for _, artifact := range refs {
		if !strings.HasPrefix(artifact.Owner, prefix) {
			continue
		}
		participant.Artifacts = append(participant.Artifacts, artifact)
		if artifact.Owner == prefix+"capture" {
			participant.Capture = artifact
			participant.CapturePath = artifact.AbsolutePath
		}
	}
}
