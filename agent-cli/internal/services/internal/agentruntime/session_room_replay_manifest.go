package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
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

type roomReplayJSONObject map[string]json.RawMessage

func validateRoomReplayManifest(root, manifestPath string, data []byte) (RoomReplayPlan, error) {
	document, err := parseRoomReplayManifest(data)
	if err != nil {
		return RoomReplayPlan{}, err
	}

	if document.SchemaVersion != 1 && document.SchemaVersion != RoomReplayBundleSchemaVersion {
		return RoomReplayPlan{}, newRoomReplayBundleError(
			RoomReplayBundleMismatch,
			"schema_version",
			"",
			"1 or "+strconv.Itoa(RoomReplayBundleSchemaVersion),
			strconv.Itoa(document.SchemaVersion),
			ErrInvalidRoomReplayBundle,
		)
	}
	if !document.Finalized {
		return RoomReplayPlan{}, newRoomReplayBundleError(
			RoomReplayBundleIncomplete,
			"finalized",
			"",
			"true",
			"false or missing",
			ErrRoomReplayBundleIncomplete,
		)
	}
	if len(document.Participants) < 2 {
		return RoomReplayPlan{}, newRoomReplayBundleError(
			RoomReplayBundleIncomplete,
			"participants",
			"",
			"at least two participants",
			strconv.Itoa(len(document.Participants)),
			ErrRoomReplayBundleIncomplete,
		)
	}
	if document.ClockBase.IsZero() || document.ClockBase.Year() <= 1970 {
		return RoomReplayPlan{}, newRoomReplayBundleError(
			RoomReplayBundleMismatch,
			"clock_base",
			"",
			"a real UTC timestamp after 1970",
			document.ClockBase.UTC().Format(time.RFC3339Nano),
			ErrInvalidRoomReplayBundle,
		)
	}
	if document.EndedAt.Before(document.StartedAt) {
		return RoomReplayPlan{}, newRoomReplayBundleError(
			RoomReplayBundleMismatch,
			"timing",
			"",
			"ended_at >= started_at",
			document.EndedAt.UTC().Format(time.RFC3339Nano)+" < "+document.StartedAt.UTC().Format(time.RFC3339Nano),
			ErrInvalidRoomReplayBundle,
		)
	}
	if document.ClockBase.Before(document.StartedAt) || document.ClockBase.After(document.EndedAt) {
		return RoomReplayPlan{}, newRoomReplayBundleError(
			RoomReplayBundleMismatch,
			"clock_base",
			"",
			"timestamp inside timing interval",
			document.ClockBase.UTC().Format(time.RFC3339Nano),
			ErrInvalidRoomReplayBundle,
		)
	}
	if err := validateRoomReplayPCMFormat(document.PCMFormat); err != nil {
		return RoomReplayPlan{}, err
	}

	participantIDs := make(map[string]struct{}, len(document.Participants))
	for _, participant := range document.Participants {
		if _, exists := participantIDs[participant.ID]; exists {
			return RoomReplayPlan{}, newRoomReplayBundleError(
				RoomReplayBundleMismatch,
				"participants.id",
				participant.ID,
				"unique participant ID",
				"duplicate",
				ErrInvalidRoomReplayBundle,
			)
		}
		participantIDs[participant.ID] = struct{}{}
		if participant.Provider == "" && participant.Kind != room.ParticipantKindHuman {
			return RoomReplayPlan{}, newRoomReplayBundleError(
				RoomReplayBundleIncomplete,
				"participants["+participant.ID+"].provider",
				"",
				"recorded provider name",
				"missing",
				ErrRoomReplayBundleIncomplete,
			)
		}
		if participant.Model == "" && participant.Kind != room.ParticipantKindHuman {
			return RoomReplayPlan{}, newRoomReplayBundleError(
				RoomReplayBundleIncomplete,
				"participants["+participant.ID+"].model",
				"",
				"recorded provider model",
				"missing",
				ErrRoomReplayBundleIncomplete,
			)
		}
	}

	refs := make([]roomReplayArtifactRef, 0, len(document.Inventory)+len(document.RoomArtifacts))
	claims := make(map[string][]string)
	appendClaim := func(ref roomReplayArtifactRef, owner string) error {
		if strings.TrimSpace(ref.Path) == "" {
			return newRoomReplayBundleError(RoomReplayBundleIncomplete, ref.Field, "", "artifact path", "missing", ErrRoomReplayBundleIncomplete)
		}
		ref.Owner = owner
		refs = append(refs, ref)
		// The integrity inventory is an index, not an owner. Only participant
		// and room role references participate in duplicate ownership checks.
		normalized := roomReplayPathKey(ref.Path)
		claims[normalized] = append(claims[normalized], owner)
		return nil
	}

	for index := range document.Participants {
		participant := &document.Participants[index]
		for _, role := range roomReplayRequiredParticipantArtifactRoles {
			ref, ok := participant.Artifacts[role]
			if !ok {
				return RoomReplayPlan{}, newRoomReplayBundleError(
					RoomReplayBundleIncomplete,
					"participants["+participant.ID+"].artifacts."+role,
					"",
					"declared artifact reference",
					"missing",
					ErrRoomReplayBundleIncomplete,
				)
			}
			if err := appendClaim(ref, "participant:"+participant.ID+":"+role); err != nil {
				return RoomReplayPlan{}, err
			}
		}
		if participant.Kind != room.ParticipantKindHuman {
			ref, ok := participant.Artifacts[roomReplayArtifactRoleCapture]
			if !ok {
				return RoomReplayPlan{}, newRoomReplayBundleError(
					RoomReplayBundleIncomplete,
					"participants["+participant.ID+"].artifacts.capture",
					"",
					"provider session capture",
					"missing",
					ErrRoomReplayBundleIncomplete,
				)
			}
			if err := appendClaim(ref, "participant:"+participant.ID+":capture"); err != nil {
				return RoomReplayPlan{}, err
			}
		}
	}
	if len(document.RoomArtifacts) != 2 {
		return RoomReplayPlan{}, newRoomReplayBundleError(
			RoomReplayBundleIncomplete,
			"artifacts",
			"",
			"room timeline and room mix",
			"missing one or more room artifacts",
			ErrRoomReplayBundleIncomplete,
		)
	}
	for _, ref := range document.RoomArtifacts {
		if err := appendClaim(ref, "room:"+ref.Role); err != nil {
			return RoomReplayPlan{}, err
		}
	}

	claimPaths := make([]string, 0, len(claims))
	for claimPath := range claims {
		claimPaths = append(claimPaths, claimPath)
	}
	sort.Strings(claimPaths)
	for _, path := range claimPaths {
		owners := claims[path]
		if len(owners) < 2 {
			continue
		}
		return RoomReplayPlan{}, newRoomReplayBundleError(
			RoomReplayBundleMismatch,
			"artifact ownership",
			path,
			"one logical owner",
			strings.Join(owners, ", "),
			ErrInvalidRoomReplayBundle,
		)
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

	plan := RoomReplayPlan{
		BundlePath:    root,
		ManifestPath:  manifestPath,
		SchemaVersion: document.SchemaVersion,
		Finalized:     document.Finalized,
		ClockBase:     document.ClockBase,
		StartedAt:     document.StartedAt,
		EndedAt:       document.EndedAt,
		PCMFormat:     document.PCMFormat,
		TimelinePath:  artifactPathByRole(validated, "room:timeline"),
		RoomMixPath:   artifactPathByRole(validated, "room:mix"),
		Artifacts:     append([]RoomReplayArtifact(nil), validated...),
	}
	sort.Slice(plan.Artifacts, func(i, j int) bool {
		if plan.Artifacts[i].Path == plan.Artifacts[j].Path {
			return plan.Artifacts[i].Name < plan.Artifacts[j].Name
		}
		return plan.Artifacts[i].Path < plan.Artifacts[j].Path
	})

	for _, participant := range document.Participants {
		projection := RoomReplayParticipant{
			ID:                participant.ID,
			Kind:              participant.Kind,
			Provider:          participant.Provider,
			Model:             participant.Model,
			Voice:             participant.Voice,
			OpeningPrompt:     participant.OpeningPrompt,
			SystemPrompt:      participant.SystemPrompt,
			RecordedTurnCount: participant.RecordedTurnCount,
			Artifacts:         make([]RoomReplayArtifact, 0, len(participant.Artifacts)),
		}
		for role, ref := range participant.Artifacts {
			artifact, ok := byPath[roomReplayPathKey(ref.Path)]
			if !ok {
				return RoomReplayPlan{}, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, ref.Path, "validated artifact", "missing", ErrInvalidRoomReplayBundle)
			}
			artifact.Name = role
			artifact.Role = role
			artifact.Owner = "participant:" + participant.ID + ":" + role
			projection.Artifacts = append(projection.Artifacts, artifact)
			if role == roomReplayArtifactRoleCapture {
				projection.Capture = artifact
				projection.CapturePath = artifact.AbsolutePath
			}
		}
		sort.Slice(projection.Artifacts, func(i, j int) bool {
			return projection.Artifacts[i].Name < projection.Artifacts[j].Name
		})
		plan.Participants = append(plan.Participants, projection)
	}

	if err := validateRoomReplayCaptures(&plan); err != nil {
		return RoomReplayPlan{}, err
	}
	timelineArtifact, ok := findRoomReplayArtifact(validated, "room:timeline")
	if !ok {
		return RoomReplayPlan{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "room_timeline", "", "validated timeline artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	timeline, err := loadRoomReplayTimeline(timelineArtifact, participantIDs, byPath, document.ClockBase, document.StartedAt, document.EndedAt)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	plan.Timeline = timeline
	return plan, nil
}

const (
	roomReplayArtifactRoleWAV         = "wav"
	roomReplayArtifactRoleDiagnostics = "diagnostics"
	roomReplayArtifactRoleDeltas      = "deltas"
	roomReplayArtifactRoleSentPCM     = "sent_pcm"
	roomReplayArtifactRoleReceivedPCM = "received_pcm"
	roomReplayArtifactRoleEvents      = "events"
	roomReplayArtifactRoleCapture     = "capture"
)

var roomReplayRequiredParticipantArtifactRoles = []string{
	roomReplayArtifactRoleWAV,
	roomReplayArtifactRoleDiagnostics,
	roomReplayArtifactRoleDeltas,
	roomReplayArtifactRoleSentPCM,
	roomReplayArtifactRoleReceivedPCM,
	roomReplayArtifactRoleEvents,
}

func parseRoomReplayManifest(data []byte) (roomReplayManifestDocument, error) {
	var object roomReplayJSONObject
	if err := json.Unmarshal(data, &object); err != nil {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleMismatch, "run-manifest.json", "", "valid JSON object", "invalid JSON", err)
	}
	if object == nil {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleMismatch, "run-manifest.json", "", "JSON object", "null", ErrInvalidRoomReplayBundle)
	}

	schema, present, err := roomReplayIntField(object, "schema_version")
	if err != nil {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleMismatch, "schema_version", "", "integer", "invalid", err)
	}
	if !present {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "schema_version", "", "supported schema version", "missing", ErrRoomReplayBundleIncomplete)
	}
	finalized, finalizedPresent, err := roomReplayBoolField(object, "finalized")
	if err != nil {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleMismatch, "finalized", "", "boolean", "invalid", err)
	}
	if !finalizedPresent {
		if status, ok, statusErr := roomReplayStringField(object, "status"); statusErr == nil && ok && strings.EqualFold(status, "finalized") {
			finalized = true
			finalizedPresent = true
		}
	}
	if !finalizedPresent {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "finalized", "", "true", "missing", ErrRoomReplayBundleIncomplete)
	}

	clockBase, clockBasePresent, err := roomReplayTimeField(object, "clock_base")
	if err != nil || !clockBasePresent {
		if timingRaw, ok := roomReplayRawField(object, "timing"); ok {
			if timing, timingErr := roomReplayObject(timingRaw); timingErr == nil {
				clockBase, clockBasePresent, err = roomReplayTimeField(timing, "clock_base")
			}
		}
	}
	if err != nil || !clockBasePresent || clockBase.IsZero() {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "clock_base", "", "UTC session start", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	startedAt, endedAt, timingErr := parseRoomReplayTiming(object, clockBase)
	if timingErr != nil {
		return roomReplayManifestDocument{}, timingErr
	}
	pcm, pcmErr := parseRoomReplayPCMFormat(object)
	if pcmErr != nil {
		return roomReplayManifestDocument{}, pcmErr
	}

	participantsRaw, present := roomReplayRawField(object, "participants")
	if !present {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants", "", "participant map or array", "missing", ErrRoomReplayBundleIncomplete)
	}
	participants, err := parseRoomReplayParticipants(participantsRaw)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}

	inventory := make([]roomReplayArtifactRef, 0)
	for _, key := range []string{"artifacts", "artifact_integrity", "integrity", "files"} {
		if raw, ok := roomReplayRawField(object, key); ok {
			entries, parseErr := parseRoomReplayArtifactInventory(raw, key)
			if parseErr != nil {
				return roomReplayManifestDocument{}, parseErr
			}
			inventory = append(inventory, entries...)
		}
	}
	for index := range participants {
		if err := inferRoomReplayParticipantArtifacts(&participants[index], inventory); err != nil {
			return roomReplayManifestDocument{}, err
		}
	}

	roomArtifacts, err := parseRoomReplayArtifacts(object, inventory)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	return roomReplayManifestDocument{
		SchemaVersion: schema,
		Finalized:     finalized,
		ClockBase:     clockBase.UTC(),
		StartedAt:     startedAt.UTC(),
		EndedAt:       endedAt.UTC(),
		PCMFormat:     pcm,
		Participants:  participants,
		RoomArtifacts: roomArtifacts,
		Inventory:     inventory,
	}, nil
}

func errOrDefault(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func parseRoomReplayTiming(object roomReplayJSONObject, clockBase time.Time) (time.Time, time.Time, error) {
	container := object
	if raw, ok := roomReplayRawField(object, "timing"); ok {
		if nested, err := roomReplayObject(raw); err == nil {
			container = nested
		}
	}
	started, ok, err := roomReplayTimeField(container, "started_at")
	if err != nil || !ok {
		started, ok, err = roomReplayTimeField(object, "started_at")
	}
	if err != nil || !ok {
		return time.Time{}, time.Time{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "timing.started_at", "", "UTC start timestamp", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	ended, ok, err := roomReplayTimeField(container, "ended_at")
	if err != nil || !ok {
		ended, ok, err = roomReplayTimeField(object, "ended_at")
	}
	if err != nil || !ok {
		return time.Time{}, time.Time{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "timing.ended_at", "", "UTC end timestamp", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	if clockBase.Before(started) || clockBase.After(ended) {
		return time.Time{}, time.Time{}, newRoomReplayBundleError(RoomReplayBundleMismatch, "clock_base", "", "inside timing interval", clockBase.UTC().Format(time.RFC3339Nano), ErrInvalidRoomReplayBundle)
	}
	return started.UTC(), ended.UTC(), nil
}

func parseRoomReplayPCMFormat(object roomReplayJSONObject) (RoomReplayPCMFormat, error) {
	format := RoomReplayPCMFormat{}
	container := object
	for _, key := range []string{"pcm_format", "pcm", "audio_format"} {
		if raw, ok := roomReplayRawField(object, key); ok {
			if nested, err := roomReplayObject(raw); err == nil {
				container = nested
				break
			}
		}
	}
	var err error
	format.SampleRate, _, err = firstRoomReplayIntField(container, object, "sample_rate_hz", "sample_rate", "sampleRate")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.sample_rate_hz", "", "positive sample rate", "invalid or missing", err)
	}
	format.Channels, _, err = firstRoomReplayIntField(container, object, "channels", "channel_count")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.channels", "", "positive channel count", "invalid or missing", err)
	}
	format.SampleWidthBits, _, err = firstRoomReplayIntField(container, object, "sample_width_bits", "sample_width", "bits_per_sample")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.sample_width_bits", "", "16", "invalid or missing", err)
	}
	format.SampleWidthBit = format.SampleWidthBits
	format.ByteOrder, _, err = firstRoomReplayStringField(container, object, "byte_order", "endianness")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.byte_order", "", "little", "invalid or missing", err)
	}
	format.Encoding, _, err = firstRoomReplayStringField(container, object, "encoding", "sample_encoding", "format")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.encoding", "", "signed_pcm16", "invalid or missing", err)
	}
	return format, nil
}

func validateRoomReplayPCMFormat(format RoomReplayPCMFormat) error {
	sampleWidth := format.SampleWidthBits
	if sampleWidth == 0 {
		sampleWidth = format.SampleWidthBit
	}
	if format.SampleRate <= 0 || format.Channels <= 0 || sampleWidth != 16 || !strings.EqualFold(format.ByteOrder, "little") {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "pcm_format", "", "signed little-endian PCM16 with positive rate/channels", fmt.Sprintf("rate=%d channels=%d width=%d byte_order=%q", format.SampleRate, format.Channels, sampleWidth, format.ByteOrder), ErrInvalidRoomReplayBundle)
	}
	encoding := strings.ToLower(strings.TrimSpace(format.Encoding))
	if encoding != "signed_pcm16" && encoding != "pcm_s16le" && encoding != "pcm16" && encoding != "signed 16-bit pcm" {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "pcm_format.encoding", "", "signed_pcm16", format.Encoding, ErrInvalidRoomReplayBundle)
	}
	return nil
}

func parseRoomReplayParticipants(raw json.RawMessage) ([]roomReplayParticipantRef, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var values []roomReplayJSONObject
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "participants", "", "array of participant objects", "invalid", err)
		}
		participants := make([]roomReplayParticipantRef, 0, len(values))
		for index, object := range values {
			participant, err := parseRoomReplayParticipant(object, fmt.Sprintf("participants[%d]", index), "")
			if err != nil {
				return nil, err
			}
			participants = append(participants, participant)
		}
		return participants, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "participants", "", "map of participant objects", "invalid", err)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	participants := make([]roomReplayParticipantRef, 0, len(keys))
	for _, key := range keys {
		object, err := roomReplayObject(values[key])
		if err != nil {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+key+"]", "", "participant object", "invalid", err)
		}
		participant, err := parseRoomReplayParticipant(object, "participants["+key+"]", key)
		if err != nil {
			return nil, err
		}
		participants = append(participants, participant)
	}
	return participants, nil
}

func parseRoomReplayParticipant(object roomReplayJSONObject, field, mapID string) (roomReplayParticipantRef, error) {
	id, present, err := roomReplayStringField(object, "id")
	if err != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".id", "", "string", "invalid", err)
	}
	if !present {
		id = mapID
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, field+".id", "", "non-empty participant ID", "missing", ErrRoomReplayBundleIncomplete)
	}
	if mapID != "" && id != mapID {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".id", id, mapID, id, ErrInvalidRoomReplayBundle)
	}
	kindValue, _, kindErr := roomReplayStringField(object, "kind")
	if kindErr != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".kind", id, "string", "invalid", kindErr)
	}
	kind := room.NormalizeParticipantKind(room.ParticipantKind(kindValue))
	if kind != room.ParticipantKindAgent && kind != room.ParticipantKindHuman {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".kind", id, "agent or human", kindValue, ErrInvalidRoomReplayBundle)
	}
	provider, _, providerErr := roomReplayStringField(object, "provider")
	if providerErr != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".provider", id, "string", "invalid", providerErr)
	}
	model, _, modelErr := roomReplayStringField(object, "model")
	if modelErr != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".model", id, "string", "invalid", modelErr)
	}
	voice, _, voiceErr := roomReplayStringField(object, "voice")
	if voiceErr != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".voice", id, "string", "invalid", voiceErr)
	}
	openingPrompt, _, openingPromptErr := roomReplayStringField(object, "opening_prompt")
	if openingPromptErr != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".opening_prompt", id, "string", "invalid", openingPromptErr)
	}
	systemPrompt, _, systemPromptErr := roomReplayStringField(object, "system_prompt")
	if systemPromptErr != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".system_prompt", id, "string", "invalid", systemPromptErr)
	}
	turnCount, turnPresent, turnErr := firstRoomReplayIntField(object, nil, "completed_turns", "turns_completed", "turn_count")
	if turnPresent && turnErr != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".completed_turns", id, "non-negative integer", "invalid", turnErr)
	}
	if turnCount < 0 {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".completed_turns", id, "non-negative integer", strconv.Itoa(turnCount), ErrInvalidRoomReplayBundle)
	}
	artifacts := make(map[string]roomReplayArtifactRef)
	if raw, ok := roomReplayRawField(object, "artifacts"); ok {
		entries, err := parseRoomReplayParticipantArtifacts(raw, field+".artifacts")
		if err != nil {
			return roomReplayParticipantRef{}, err
		}
		for role, artifact := range entries {
			if _, exists := artifacts[role]; exists {
				return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".artifacts", artifact.Path, "one reference for role", role, ErrInvalidRoomReplayBundle)
			}
			artifacts[role] = artifact
		}
	}
	for _, direct := range []struct {
		role string
		keys []string
	}{
		{roomReplayArtifactRoleCapture, []string{"capture", "capture_path", "session_capture", "session_capture_path", "replay_capture", "provider_capture", "provider_capture_path"}},
		{roomReplayArtifactRoleSentPCM, []string{"sent_pcm", "sent_pcm_path", "sent"}},
		{roomReplayArtifactRoleReceivedPCM, []string{"received_pcm", "received_pcm_path", "received"}},
		{roomReplayArtifactRoleEvents, []string{"events", "events_path", "participant_events"}},
		{roomReplayArtifactRoleWAV, []string{"wav", "wav_path", "legacy_wav", "audio_wav"}},
		{roomReplayArtifactRoleDiagnostics, []string{"diagnostics", "diagnostics_path", "diagnostic"}},
		{roomReplayArtifactRoleDeltas, []string{"deltas", "deltas_path", "delta"}},
	} {
		if _, exists := artifacts[direct.role]; exists {
			continue
		}
		if raw, ok := roomReplayRawField(object, direct.keys...); ok {
			artifact, err := parseRoomReplayArtifactRef(raw, field+"."+direct.role, direct.role)
			if err != nil {
				return roomReplayParticipantRef{}, err
			}
			artifacts[direct.role] = artifact
		}
	}
	return roomReplayParticipantRef{ID: id, Kind: kind, Provider: strings.TrimSpace(provider), Model: strings.TrimSpace(model), Voice: voice, OpeningPrompt: openingPrompt, SystemPrompt: systemPrompt, RecordedTurnCount: turnCount, Artifacts: artifacts}, nil
}

func parseRoomReplayParticipantArtifacts(raw json.RawMessage, field string) (map[string]roomReplayArtifactRef, error) {
	result := make(map[string]roomReplayArtifactRef)
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		var values []roomReplayJSONObject
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "artifact array", "invalid", err)
		}
		for index, object := range values {
			role, _, _ := firstRoomReplayStringField(object, nil, "role", "name", "type", "kind")
			role = normalizeRoomReplayArtifactRole(role)
			if role == "" {
				return nil, newRoomReplayBundleError(RoomReplayBundleIncomplete, fmt.Sprintf("%s[%d].role", field, index), "", "artifact role", "missing", ErrRoomReplayBundleIncomplete)
			}
			artifact, err := parseRoomReplayArtifactRef(mustMarshal(object), fmt.Sprintf("%s[%d]", field, index), role)
			if err != nil {
				return nil, err
			}
			if _, exists := result[role]; exists {
				return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, artifact.Path, "unique artifact role", role, ErrInvalidRoomReplayBundle)
			}
			result[role] = artifact
		}
		return result, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "artifact map", "invalid", err)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		role := normalizeRoomReplayArtifactRole(key)
		if role == "" {
			role = key
		}
		artifact, err := parseRoomReplayArtifactRef(values[key], field+"."+key, role)
		if err != nil {
			return nil, err
		}
		if _, exists := result[role]; exists {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, artifact.Path, "unique artifact role", role, ErrInvalidRoomReplayBundle)
		}
		result[role] = artifact
	}
	return result, nil
}

func inferRoomReplayParticipantArtifacts(participant *roomReplayParticipantRef, inventory []roomReplayArtifactRef) error {
	for _, entry := range inventory {
		role := normalizeRoomReplayArtifactRole(entry.Name)
		if !isRoomReplayParticipantArtifactRole(role) {
			role = normalizeRoomReplayArtifactRole(entry.Path)
		}
		if role == "" || role == "timeline" || role == "mix" {
			continue
		}
		if !roomReplayInventoryNameBelongsToParticipant(entry.Name, participant.ID, role) && !roomReplayInventoryNameBelongsToParticipant(entry.Path, participant.ID, role) {
			continue
		}
		if _, exists := participant.Artifacts[role]; exists {
			continue
		}
		candidate := entry
		candidate.Role = role
		candidate.Field = "artifacts." + entry.Name
		participant.Artifacts[role] = candidate
	}
	return nil
}

func roomReplayInventoryNameBelongsToParticipant(name, id, role string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	id = strings.ToLower(strings.TrimSpace(id))
	if name == "" || id == "" || !roomReplayContainsParticipantID(name, id) {
		return false
	}
	for _, suffix := range []string{"." + role, "_" + role, "-" + role, "/" + role} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	for _, suffix := range map[string][]string{
		roomReplayArtifactRoleWAV:         {"/agent.wav", ".wav"},
		roomReplayArtifactRoleDiagnostics: {"/diagnostics.jsonl", ".diagnostics.jsonl"},
		roomReplayArtifactRoleDeltas:      {"/deltas.jsonl", ".deltas.jsonl"},
		roomReplayArtifactRoleSentPCM:     {"/sent.pcm", ".sent.pcm"},
		roomReplayArtifactRoleReceivedPCM: {"/received.pcm", ".received.pcm"},
		roomReplayArtifactRoleEvents:      {"/events.jsonl", ".events.jsonl"},
		roomReplayArtifactRoleCapture:     {".session.json", "/capture.json"},
	}[role] {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func roomReplayContainsParticipantID(name, id string) bool {
	start := 0
	for {
		index := strings.Index(name[start:], id)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !isRoomReplayIdentifierByte(name[index-1])
		after := index + len(id)
		afterOK := after == len(name) || !isRoomReplayIdentifierByte(name[after])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
		if start >= len(name) {
			return false
		}
	}
}

func isRoomReplayIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func isRoomReplayParticipantArtifactRole(role string) bool {
	switch role {
	case roomReplayArtifactRoleWAV, roomReplayArtifactRoleDiagnostics, roomReplayArtifactRoleDeltas, roomReplayArtifactRoleSentPCM, roomReplayArtifactRoleReceivedPCM, roomReplayArtifactRoleEvents, roomReplayArtifactRoleCapture:
		return true
	default:
		return false
	}
}

func parseRoomReplayArtifacts(object roomReplayJSONObject, inventory []roomReplayArtifactRef) ([]roomReplayArtifactRef, error) {
	result := make([]roomReplayArtifactRef, 0, 2)
	for _, roomArtifact := range []struct {
		role string
		keys []string
	}{
		{"timeline", []string{"room_timeline", "room-timeline", "timeline", "room_timeline_path", "timeline_path", "room_timeline_artifact"}},
		{"mix", []string{"room_mix", "room-mix", "mix", "room_mix_path", "mix_path", "room_mix_artifact"}},
	} {
		role := roomArtifact.role
		keys := roomArtifact.keys
		var found *roomReplayArtifactRef
		for _, key := range keys {
			if raw, ok := roomReplayRawField(object, key); ok {
				ref, err := parseRoomReplayArtifactRef(raw, key, role)
				if err != nil {
					return nil, err
				}
				found = &ref
				break
			}
		}
		if found == nil {
			for _, entry := range inventory {
				if normalizeRoomReplayArtifactRole(entry.Name) == role || strings.Contains(normalizeRoomReplayArtifactRole(entry.Name), "room_"+role) {
					copy := entry
					copy.Role = role
					copy.Field = "artifacts." + entry.Name
					found = &copy
					break
				}
			}
		}
		if found == nil {
			continue
		}
		result = append(result, *found)
	}
	return result, nil
}

func parseRoomReplayArtifactRef(raw json.RawMessage, field, role string) (roomReplayArtifactRef, error) {
	artifact := roomReplayArtifactRef{Field: field, Role: normalizeRoomReplayArtifactRole(role), Name: role}
	if len(raw) == 0 || string(raw) == "null" {
		return artifact, newRoomReplayBundleError(RoomReplayBundleIncomplete, field, "", "artifact reference", "null", ErrRoomReplayBundleIncomplete)
	}
	if value, ok := decodeRoomReplayString(raw); ok {
		artifact.Path = strings.TrimSpace(value)
		return artifact, nil
	}
	object, err := roomReplayObject(raw)
	if err != nil {
		return artifact, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "path string or artifact object", "invalid", err)
	}
	pathValue, present, err := firstRoomReplayStringField(object, nil, "path", "relative_path", "file", "filename")
	if err != nil || !present {
		return artifact, newRoomReplayBundleError(RoomReplayBundleIncomplete, field+".path", "", "bundle-relative path", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	artifact.Path = strings.TrimSpace(pathValue)
	if size, present, err := firstRoomReplayIntField(object, nil, "size", "bytes", "byte_size", "byte_count", "declared_size"); present {
		if err != nil || size < 0 {
			return artifact, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".size", artifact.Path, "non-negative integer", "invalid", errOrDefault(err, ErrInvalidRoomReplayBundle))
		}
		artifact.Size = int64Pointer(size)
	}
	if digest, present, err := firstRoomReplayStringField(object, nil, "sha256", "sha_256", "sha256_digest", "digest"); present {
		if err != nil {
			return artifact, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".sha256", artifact.Path, "sha256 hex digest", "invalid", err)
		}
		artifact.SHA256 = normalizeRoomReplayDigest(digest)
	}
	return artifact, nil
}

func parseRoomReplayArtifactInventory(raw json.RawMessage, field string) ([]roomReplayArtifactRef, error) {
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		var values []roomReplayJSONObject
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "artifact inventory array", "invalid", err)
		}
		result := make([]roomReplayArtifactRef, 0, len(values))
		for index, object := range values {
			name, _, _ := firstRoomReplayStringField(object, nil, "name", "id", "key", "role")
			if name == "" {
				name, _, _ = firstRoomReplayStringField(object, nil, "path", "relative_path", "file", "filename")
			}
			if name == "" {
				name = strconv.Itoa(index)
			}
			ref, err := parseRoomReplayArtifactRef(mustMarshal(object), fmt.Sprintf("%s[%d]", field, index), name)
			if err != nil {
				return nil, err
			}
			if ref.Name == strconv.Itoa(index) && ref.Path != "" {
				ref.Name = ref.Path
			}
			result = append(result, ref)
		}
		return result, nil
	}
	object, err := roomReplayObject(raw)
	if err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "artifact inventory object", "invalid", err)
	}
	if _, present, _ := firstRoomReplayStringField(object, nil, "path", "relative_path", "file", "filename"); present {
		name, _, _ := firstRoomReplayStringField(object, nil, "name", "id", "key", "role")
		if name == "" {
			name, _, _ = firstRoomReplayStringField(object, nil, "path", "relative_path", "file", "filename")
		}
		ref, refErr := parseRoomReplayArtifactRef(raw, field, name)
		if refErr != nil {
			return nil, refErr
		}
		if ref.Name == "" {
			ref.Name = ref.Path
		}
		return []roomReplayArtifactRef{ref}, nil
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]roomReplayArtifactRef, 0, len(keys))
	for _, key := range keys {
		value := object[key]
		if strings.EqualFold(key, "artifacts") || strings.EqualFold(key, "files") {
			nested, nestedErr := parseRoomReplayArtifactInventory(value, field+"."+key)
			if nestedErr != nil {
				return nil, nestedErr
			}
			result = append(result, nested...)
			continue
		}
		// Integrity maps often use the path as the key and store only size and
		// digest in the value. Materialize that key as the path before parsing.
		if metadataObject, objectErr := roomReplayObject(value); objectErr == nil {
			if _, present, _ := firstRoomReplayStringField(metadataObject, nil, "path", "relative_path", "file", "filename"); !present {
				metadataObject["path"] = mustMarshal(key)
				value = mustMarshal(metadataObject)
			}
		}
		ref, refErr := parseRoomReplayArtifactRef(value, field+"."+key, key)
		if refErr != nil {
			return nil, refErr
		}
		if ref.Path == "" {
			// A map keyed by path commonly stores metadata as the value.
			ref.Path = key
		}
		result = append(result, ref)
	}
	return result, nil
}

func mergeRoomReplayArtifactMetadata(inventory, refs []roomReplayArtifactRef) (map[string]roomReplayArtifactRef, error) {
	metadata := make(map[string]roomReplayArtifactRef, len(inventory)+len(refs))
	merge := func(ref roomReplayArtifactRef) error {
		if ref.Path == "" {
			return nil
		}
		path := roomReplayPathKey(ref.Path)
		if existing, ok := metadata[path]; ok {
			if existing.Size != nil && ref.Size != nil && *existing.Size != *ref.Size {
				return newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, path, strconv.FormatInt(*existing.Size, 10), strconv.FormatInt(*ref.Size, 10), ErrInvalidRoomReplayBundle)
			}
			if existing.SHA256 != "" && ref.SHA256 != "" && existing.SHA256 != normalizeRoomReplayDigest(ref.SHA256) {
				return newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, path, existing.SHA256, normalizeRoomReplayDigest(ref.SHA256), ErrInvalidRoomReplayBundle)
			}
			if existing.Size == nil {
				existing.Size = ref.Size
			}
			if existing.SHA256 == "" {
				existing.SHA256 = normalizeRoomReplayDigest(ref.SHA256)
			}
			metadata[path] = existing
			return nil
		}
		ref.SHA256 = normalizeRoomReplayDigest(ref.SHA256)
		metadata[path] = ref
		return nil
	}
	for _, ref := range inventory {
		if err := merge(ref); err != nil {
			return nil, err
		}
	}
	for _, ref := range refs {
		if err := merge(ref); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func findRoomReplayArtifact(artifacts []RoomReplayArtifact, owner string) (RoomReplayArtifact, bool) {
	for _, artifact := range artifacts {
		if artifact.Owner == owner {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}

func artifactPathByRole(artifacts []RoomReplayArtifact, owner string) string {
	artifact, ok := findRoomReplayArtifact(artifacts, owner)
	if !ok {
		return ""
	}
	return artifact.AbsolutePath
}

func normalizeRoomReplayArtifactRole(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_", " ", "_", "/", "_").Replace(normalized)
	switch {
	case strings.Contains(normalized, "room_mix") || normalized == "mix":
		return "mix"
	case normalized == "wav" || strings.Contains(normalized, "legacy_wav") || strings.HasSuffix(normalized, "_wav") || strings.HasSuffix(normalized, "_audio"):
		return roomReplayArtifactRoleWAV
	case strings.Contains(normalized, "diagnostic"):
		return roomReplayArtifactRoleDiagnostics
	case strings.Contains(normalized, "delta"):
		return roomReplayArtifactRoleDeltas
	case strings.Contains(normalized, "sent") && (strings.Contains(normalized, "pcm") || normalized == "sent"):
		return roomReplayArtifactRoleSentPCM
	case strings.Contains(normalized, "received") && (strings.Contains(normalized, "pcm") || normalized == "received"):
		return roomReplayArtifactRoleReceivedPCM
	case strings.Contains(normalized, "event"):
		return roomReplayArtifactRoleEvents
	case strings.Contains(normalized, "capture") || strings.Contains(normalized, "replay") || strings.Contains(normalized, "session"):
		return roomReplayArtifactRoleCapture
	case strings.Contains(normalized, "timeline"):
		return "timeline"
	case strings.Contains(normalized, "mix"):
		return "mix"
	default:
		return normalized
	}
}

func roomReplayObject(raw json.RawMessage) (roomReplayJSONObject, error) {
	var object roomReplayJSONObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("expected JSON object")
	}
	return object, nil
}

func mustMarshal(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func roomReplayRawField(object roomReplayJSONObject, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			return raw, true
		}
	}
	return nil, false
}

func roomReplayStringField(object roomReplayJSONObject, names ...string) (string, bool, error) {
	raw, ok := roomReplayRawField(object, names...)
	if !ok {
		return "", false, nil
	}
	value, ok := decodeRoomReplayString(raw)
	if !ok {
		return "", true, errors.New("expected string")
	}
	return strings.TrimSpace(value), true, nil
}

func roomReplayBoolField(object roomReplayJSONObject, name string) (bool, bool, error) {
	raw, ok := roomReplayRawField(object, name)
	if !ok {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, true, err
	}
	return value, true, nil
}

func roomReplayIntField(object roomReplayJSONObject, name string) (int, bool, error) {
	raw, ok := roomReplayRawField(object, name)
	if !ok {
		return 0, false, nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, true, err
	}
	value, err := strconv.Atoi(number.String())
	if err != nil {
		return 0, true, err
	}
	return value, true, nil
}

func firstRoomReplayIntField(primary, fallback roomReplayJSONObject, names ...string) (int, bool, error) {
	if primary != nil {
		for _, name := range names {
			if value, present, err := roomReplayIntField(primary, name); present {
				return value, true, err
			}
		}
	}
	if fallback != nil {
		for _, name := range names {
			if value, present, err := roomReplayIntField(fallback, name); present {
				return value, true, err
			}
		}
	}
	return 0, false, errors.New("missing integer")
}

func firstRoomReplayStringField(primary, fallback roomReplayJSONObject, names ...string) (string, bool, error) {
	if primary != nil {
		for _, name := range names {
			if value, present, err := roomReplayStringField(primary, name); present {
				return value, true, err
			}
		}
	}
	if fallback != nil {
		for _, name := range names {
			if value, present, err := roomReplayStringField(fallback, name); present {
				return value, true, err
			}
		}
	}
	return "", false, errors.New("missing string")
}

func roomReplayTimeField(object roomReplayJSONObject, name string) (time.Time, bool, error) {
	raw, ok := roomReplayRawField(object, name)
	if !ok {
		return time.Time{}, false, nil
	}
	value, ok := decodeRoomReplayString(raw)
	if !ok {
		return time.Time{}, true, errors.New("expected RFC3339 string")
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, true, err
	}
	return parsed.UTC(), true, nil
}

func decodeRoomReplayString(raw json.RawMessage) (string, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func int64Pointer(value int) *int64 {
	converted := int64(value)
	return &converted
}

func normalizeRoomReplayDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "sha256:")
	return value
}
