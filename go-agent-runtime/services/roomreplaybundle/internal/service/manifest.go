package service

import (
	"sort"
	"strconv"
	"strings"
	"time"

	room "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type roomReplayManifestDocument struct {
	SchemaVersion int
	Finalized     bool
	ClockBase     time.Time
	StartedAt     time.Time
	EndedAt       time.Time
	PCMFormat     RoomReplayPCMFormat
	Participants  []roomReplayParticipantRef
	RoomArtifacts []roomReplayArtifactRef
	Inventory     []roomReplayArtifactRef
}

type roomReplayParticipantRef struct {
	ID                string
	Kind              room.ParticipantKind
	Provider          string
	Model             string
	Voice             string
	OpeningPrompt     string
	SystemPrompt      string
	RecordedTurnCount int
	Artifacts         map[string]roomReplayArtifactRef
}

type roomReplayArtifactRef struct {
	Name   string
	Owner  string
	Role   string
	Path   string
	Size   *int64
	SHA256 string
	Field  string
}

func validateRoomReplayManifest(root, manifestPath string, data []byte) (RoomReplayPlan, error) {
	document, err := parseRoomReplayManifest(data)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	if err := validateRoomReplayManifestHeader(document); err != nil {
		return RoomReplayPlan{}, err
	}
	participantIDs, err := validateRoomReplayParticipants(document.Participants)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	refs, claims, err := collectRoomReplayClaims(document)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	if err := validateRoomReplayClaims(claims); err != nil {
		return RoomReplayPlan{}, err
	}
	metadata, err := mergeRoomReplayArtifactMetadata(document.Inventory, refs)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	validated, byPath, err := validateRoomReplayArtifacts(root, refs, metadata)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	if err := validateRoomReplayInventory(root, document.Inventory, byPath); err != nil {
		return RoomReplayPlan{}, err
	}
	plan, err := projectRoomReplayPlan(root, manifestPath, document, validated, byPath)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	if err := validateRoomReplayCaptures(&plan); err != nil {
		return RoomReplayPlan{}, err
	}
	timelineArtifact, ok := findRoomReplayArtifact(validated, "room:timeline")
	if !ok {
		return RoomReplayPlan{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "room_timeline", "", "validated timeline artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	plan.Timeline, err = loadRoomReplayTimeline(timelineArtifact, participantIDs, byPath, document.ClockBase, document.StartedAt, document.EndedAt)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	return plan, nil
}

func validateRoomReplayManifestHeader(document roomReplayManifestDocument) error {
	if document.SchemaVersion != 1 && document.SchemaVersion != RoomReplayBundleSchemaVersion {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "schema_version", "", "1 or "+strconv.Itoa(RoomReplayBundleSchemaVersion), strconv.Itoa(document.SchemaVersion), ErrInvalidRoomReplayBundle)
	}
	if !document.Finalized {
		return newRoomReplayBundleError(RoomReplayBundleIncomplete, "finalized", "", "true", "false or missing", ErrRoomReplayBundleIncomplete)
	}
	if len(document.Participants) < 2 {
		return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants", "", "at least two participants", strconv.Itoa(len(document.Participants)), ErrRoomReplayBundleIncomplete)
	}
	if document.ClockBase.IsZero() || document.ClockBase.Year() <= 1970 {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "clock_base", "", "a real UTC timestamp after 1970", document.ClockBase.UTC().Format(time.RFC3339Nano), ErrInvalidRoomReplayBundle)
	}
	if document.EndedAt.Before(document.StartedAt) || document.ClockBase.Before(document.StartedAt) || document.ClockBase.After(document.EndedAt) {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "timing", "", "ended_at >= started_at and clock_base inside interval", document.EndedAt.UTC().Format(time.RFC3339Nano), ErrInvalidRoomReplayBundle)
	}
	return validateRoomReplayPCMFormat(document.PCMFormat)
}

func validateRoomReplayParticipants(participants []roomReplayParticipantRef) (map[string]struct{}, error) {
	ids := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		if _, exists := ids[participant.ID]; exists {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "participants.id", participant.ID, "unique participant ID", "duplicate", ErrInvalidRoomReplayBundle)
		}
		ids[participant.ID] = struct{}{}
		if err := validateRoomReplayParticipantProvider(participant); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func validateRoomReplayParticipantProvider(participant roomReplayParticipantRef) error {
	if participant.Provider == "" && participant.Kind != room.ParticipantKindHuman {
		return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants["+participant.ID+"].provider", "", "recorded provider name", "missing", ErrRoomReplayBundleIncomplete)
	}
	if participant.Model == "" && participant.Kind != room.ParticipantKindHuman {
		return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants["+participant.ID+"].model", "", "recorded provider model", "missing", ErrRoomReplayBundleIncomplete)
	}
	return nil
}

func collectRoomReplayClaims(document roomReplayManifestDocument) ([]roomReplayArtifactRef, map[string][]string, error) {
	refs := make([]roomReplayArtifactRef, 0, len(document.Inventory)+len(document.RoomArtifacts))
	claims := make(map[string][]string)
	appendClaim := func(ref roomReplayArtifactRef, owner string) error {
		if strings.TrimSpace(ref.Path) == "" {
			return newRoomReplayBundleError(RoomReplayBundleIncomplete, ref.Field, "", "artifact path", "missing", ErrRoomReplayBundleIncomplete)
		}
		ref.Owner = owner
		refs = append(refs, ref)
		key := roomReplayPathKey(ref.Path)
		claims[key] = append(claims[key], owner)
		return nil
	}
	for index := range document.Participants {
		participant := &document.Participants[index]
		if err := appendParticipantReplayClaims(participant, appendClaim); err != nil {
			return nil, nil, err
		}
	}
	if len(document.RoomArtifacts) != 2 {
		return nil, nil, newRoomReplayBundleError(RoomReplayBundleIncomplete, "artifacts", "", "room timeline and room mix", "missing one or more room artifacts", ErrRoomReplayBundleIncomplete)
	}
	for _, ref := range document.RoomArtifacts {
		if err := appendClaim(ref, "room:"+ref.Role); err != nil {
			return nil, nil, err
		}
	}
	return refs, claims, nil
}

func appendParticipantReplayClaims(participant *roomReplayParticipantRef, appendClaim func(roomReplayArtifactRef, string) error) error {
	for _, role := range roomReplayRequiredParticipantArtifactRoles() {
		ref, ok := participant.Artifacts[role]
		if !ok {
			return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants["+participant.ID+"].artifacts."+role, "", "declared artifact reference", "missing", ErrRoomReplayBundleIncomplete)
		}
		if err := appendClaim(ref, "participant:"+participant.ID+":"+role); err != nil {
			return err
		}
	}
	if participant.Kind != room.ParticipantKindHuman {
		ref, ok := participant.Artifacts[roomReplayArtifactRoleCapture]
		if !ok {
			return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants["+participant.ID+"].artifacts.capture", "", "provider session capture", "missing", ErrRoomReplayBundleIncomplete)
		}
		return appendClaim(ref, "participant:"+participant.ID+":capture")
	}
	return nil
}

func validateRoomReplayClaims(claims map[string][]string) error {
	keys := make([]string, 0, len(claims))
	for key := range claims {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		owners := claims[key]
		if len(owners) > 1 {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact ownership", key, "one logical owner", strings.Join(owners, ", "), ErrInvalidRoomReplayBundle)
		}
	}
	return nil
}

func projectRoomReplayPlan(root, manifestPath string, document roomReplayManifestDocument, validated []RoomReplayArtifact, byPath map[string]RoomReplayArtifact) (RoomReplayPlan, error) {
	plan := RoomReplayPlan{BundlePath: root, ManifestPath: manifestPath, SchemaVersion: document.SchemaVersion, Finalized: document.Finalized, ClockBase: document.ClockBase, StartedAt: document.StartedAt, EndedAt: document.EndedAt, PCMFormat: document.PCMFormat, TimelinePath: artifactPathByRole(validated, "room:timeline"), RoomMixPath: artifactPathByRole(validated, "room:mix"), Artifacts: append([]RoomReplayArtifact(nil), validated...)}
	sort.Slice(plan.Artifacts, func(i, j int) bool {
		if plan.Artifacts[i].Path == plan.Artifacts[j].Path {
			return plan.Artifacts[i].Name < plan.Artifacts[j].Name
		}
		return plan.Artifacts[i].Path < plan.Artifacts[j].Path
	})
	for _, participant := range document.Participants {
		projection, err := projectRoomReplayParticipant(participant, byPath)
		if err != nil {
			return RoomReplayPlan{}, err
		}
		plan.Participants = append(plan.Participants, projection)
	}
	return plan, nil
}

func projectRoomReplayParticipant(participant roomReplayParticipantRef, byPath map[string]RoomReplayArtifact) (RoomReplayParticipant, error) {
	projection := RoomReplayParticipant{ID: participant.ID, Kind: participant.Kind, Provider: participant.Provider, Model: participant.Model, Voice: participant.Voice, OpeningPrompt: participant.OpeningPrompt, SystemPrompt: participant.SystemPrompt, RecordedTurnCount: participant.RecordedTurnCount, Artifacts: make([]RoomReplayArtifact, 0, len(participant.Artifacts))}
	for role, ref := range participant.Artifacts {
		artifact, ok := byPath[roomReplayPathKey(ref.Path)]
		if !ok {
			return RoomReplayParticipant{}, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, ref.Path, "validated artifact", "missing", ErrInvalidRoomReplayBundle)
		}
		artifact.Name, artifact.Role, artifact.Owner = role, role, "participant:"+participant.ID+":"+role
		projection.Artifacts = append(projection.Artifacts, artifact)
		if role == roomReplayArtifactRoleCapture {
			projection.Capture, projection.CapturePath = artifact, artifact.AbsolutePath
		}
	}
	sort.Slice(projection.Artifacts, func(i, j int) bool { return projection.Artifacts[i].Name < projection.Artifacts[j].Name })
	return projection, nil
}
