package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	roomReplayTestAlphaParticipant  = "alpha"
	roomReplayTestAlphaOutputStream = "alpha:output"
	roomReplayTestAlphaSentStream   = "alpha:sent"
)

func assertRoomReplayBundleMetadata(t *testing.T, got RoomReplayAudioBundle, resolvedBundle string) {
	t.Helper()
	if got.Plan.BundlePath != resolvedBundle || got.Format.SampleRate != 24000 || got.Format.Channels != 1 || got.Format.SampleWidthBits != 16 {
		t.Fatalf("bundle metadata = %+v, want resolved bundle and PCM16 format", got)
	}
	defaults := DefaultRoomReplayToleranceProfile()
	if got.Tolerances.StreamConfig != defaults.StreamConfig || got.Tolerances.RoomConfig != defaults.RoomConfig {
		t.Fatalf("default tolerance profile = %+v, want suite defaults", got.Tolerances)
	}
	if len(got.Participants) != 2 || got.Participants[0].ID != roomReplayTestAlphaParticipant || got.Participants[1].ID != "beta" {
		t.Fatalf("participants = %+v, want stable manifest identities", got.Participants)
	}
}

func assertRoomReplayBundleParticipant(t *testing.T, got RoomReplayAudioBundle, want map[string][]int16) {
	t.Helper()
	alpha, ok := got.Participant(roomReplayTestAlphaParticipant)
	if !ok {
		t.Fatal("alpha participant missing")
	}
	if alpha.WAV.StreamID != roomReplayTestAlphaOutputStream || alpha.Sent.StreamID != roomReplayTestAlphaSentStream || alpha.Received.StreamID != "alpha:received" {
		t.Fatalf("alpha stream identities = %q/%q/%q", alpha.WAV.StreamID, alpha.Sent.StreamID, alpha.Received.StreamID)
	}
	if !bytes.Equal(alpha.WAV.PCM, roomReplayAudioPCM16Bytes(want["alpha:wav"])) || len(alpha.WAV.Deltas) != 2 || alpha.WAV.SampleCount != len(want["alpha:wav"]) {
		t.Fatalf("alpha WAV evidence = bytes:%v deltas:%d samples:%d", alpha.WAV.PCM, len(alpha.WAV.Deltas), alpha.WAV.SampleCount)
	}
	if !bytes.Equal(alpha.Sent.PCM, roomReplayAudioPCM16Bytes(want[roomReplayTestAlphaSentStream])) || !bytes.Equal(alpha.Received.PCM, roomReplayAudioPCM16Bytes(want["alpha:received"])) {
		t.Fatal("alpha sent/received PCM was not resolved exactly")
	}
	if len(alpha.Events) != 2 || len(alpha.Diagnostics) != 1 {
		t.Fatalf("alpha sidecars = events:%d diagnostics:%d, want complete JSONL evidence", len(alpha.Events), len(alpha.Diagnostics))
	}
}

func assertRoomReplayBundleRoomEvidence(t *testing.T, got RoomReplayAudioBundle, want map[string][]int16) {
	t.Helper()
	if got.RoomMix.StreamID != "room:mix" || got.RoomMix.SampleCount != len(want["room:mix"]) {
		t.Fatalf("room mix = %+v, want decoded room-level WAV", got.RoomMix)
	}
	if len(got.Plan.Timeline) != 2 || got.Plan.Timeline[0].OffsetMS != 0 || got.Plan.Timeline[1].ParticipantID != "beta" {
		t.Fatalf("timeline = %+v, want ordered room timeline", got.Plan.Timeline)
	}
	if len(got.Overlaps) != 1 || got.Overlaps[0].A.SentStreamID != "alpha:sent" || got.Overlaps[0].B.ReceivedStreamID != "beta:received" {
		t.Fatalf("overlap annotations = %+v, want independent sent/received identities", got.Overlaps)
	}
}

func assertRoomReplayBundleAnalysis(t *testing.T, got RoomReplayAudioBundle) {
	t.Helper()
	input := got.AnalysisInput()
	if len(input.Streams) != 7 || len(input.Overlaps) != 1 || input.Streams[0].StreamID != roomReplayTestAlphaOutputStream {
		t.Fatalf("analysis input = streams:%d overlaps:%d first:%q, want all resolved streams", len(input.Streams), len(input.Overlaps), input.Streams[0].StreamID)
	}
}

func runRoomReplayDeltaMutationTest(t *testing.T, name string, mutate func([]map[string]any)) {
	t.Helper()
	bundle, manifest, _ := writeRoomReplayAudioBundle(t)
	path := filepath.Join(bundle, "participants", roomReplayTestAlphaParticipant, "deltas.jsonl")
	lines := roomReplayDeltaMutationLines()
	if name == "dropped delta" {
		lines = lines[1:]
	} else {
		mutate(lines)
	}
	data := jsonLines(t, lines)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("rewrite deltas: %v", err)
	}
	updateRoomReplayParticipantArtifact(t, manifest, roomReplayTestAlphaParticipant, "deltas", data)
	writeRoomReplayAudioManifest(t, bundle, manifest)
	assertRoomReplayDeltaReconstruction(t, name, bundle)
}

func roomReplayDeltaMutationLines() []map[string]any {
	return []map[string]any{
		{"type": "AUDIO.DELTA", "sequence": 0, "delta_id": "alpha-delta-0", "delta": base64.StdEncoding.EncodeToString(roomReplayAudioPCM16Bytes([]int16{1000, 2000}))},
		{"type": "AUDIO.DELTA", "sequence": 1, "delta_id": "alpha-delta-1", "delta": base64.StdEncoding.EncodeToString(roomReplayAudioPCM16Bytes([]int16{3000, 4000}))},
	}
}

func assertRoomReplayDeltaReconstruction(t *testing.T, name, bundle string) {
	t.Helper()
	_, err := loadTestRoomReplayAudioBundle(bundle)
	if err == nil || !errors.Is(err, ErrRoomReplayDeltaReconstruction) {
		t.Fatalf("%s error = %v, want typed reconstruction failure", name, err)
	}
	var reconstruction *RoomReplayDeltaReconstructionError
	if !errors.As(err, &reconstruction) {
		t.Fatalf("%s error = %v, want first-divergence details", name, err)
	}
	if reconstruction.ParticipantID != roomReplayTestAlphaParticipant || reconstruction.StreamID != roomReplayTestAlphaOutputStream || reconstruction.DeltaID == "" {
		t.Fatalf("reconstruction = %+v, want stable participant/stream/delta identity", reconstruction)
	}
	if !strings.Contains(err.Error(), "first divergent byte") || !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "reconstructed") {
		t.Fatalf("reconstruction error = %v, want numeric discrepancy", err)
	}
}

func readTestRoomReplayManifest(bundle string) (string, map[string]any, error) {
	resolvedBundle, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		return "", nil, err
	}
	manifestData, err := os.ReadFile(filepath.Join(resolvedBundle, RoomReplayBundleManifestPath))
	if err != nil {
		return "", nil, err
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", nil, err
	}
	return resolvedBundle, manifest, nil
}

func testRoomReplayPlanHeader(bundle string, manifest map[string]any) (RoomReplayPlan, error) {
	clockBase, err := parseTestRoomReplayTime(manifest["clock_base"])
	if err != nil {
		return RoomReplayPlan{}, err
	}
	timing, ok := manifest["timing"].(map[string]any)
	if !ok {
		return RoomReplayPlan{}, errors.New("test manifest timing must be an object")
	}
	startedAt, err := parseTestRoomReplayTime(timing["started_at"])
	if err != nil {
		return RoomReplayPlan{}, err
	}
	endedAt, err := parseTestRoomReplayTime(timing["ended_at"])
	if err != nil {
		return RoomReplayPlan{}, err
	}
	formatObject, ok := manifest["pcm_format"].(map[string]any)
	if !ok {
		return RoomReplayPlan{}, errors.New("test manifest pcm_format must be an object")
	}
	schemaVersion, err := testRoomReplayNumber(manifest, "schema_version")
	if err != nil {
		return RoomReplayPlan{}, err
	}
	finalized, ok := manifest["finalized"].(bool)
	if !ok {
		return RoomReplayPlan{}, errors.New("test manifest finalized must be a boolean")
	}
	sampleRate, err := testRoomReplayNumber(formatObject, "sample_rate_hz")
	if err != nil {
		return RoomReplayPlan{}, err
	}
	channels, err := testRoomReplayNumber(formatObject, "channels")
	if err != nil {
		return RoomReplayPlan{}, err
	}
	sampleWidthBits, err := testRoomReplayNumber(formatObject, "sample_width_bits")
	if err != nil {
		return RoomReplayPlan{}, err
	}
	byteOrder, err := testRoomReplayString(formatObject, "byte_order")
	if err != nil {
		return RoomReplayPlan{}, err
	}
	encoding, err := testRoomReplayString(formatObject, "encoding")
	if err != nil {
		return RoomReplayPlan{}, err
	}
	return RoomReplayPlan{
		BundlePath: bundle, ManifestPath: filepath.Join(bundle, RoomReplayBundleManifestPath), SchemaVersion: int(schemaVersion), Finalized: finalized,
		ClockBase: clockBase, StartedAt: startedAt, EndedAt: endedAt,
		PCMFormat:    RoomReplayPCMFormat{SampleRate: int(sampleRate), Channels: int(channels), SampleWidthBits: int(sampleWidthBits), ByteOrder: byteOrder, Encoding: encoding},
		TimelinePath: filepath.Join(bundle, "room-timeline.jsonl"), RoomMixPath: filepath.Join(bundle, "room-mix.wav"),
	}, nil
}

func parseTestRoomReplayTime(value any) (time.Time, error) {
	text, ok := value.(string)
	if !ok {
		return time.Time{}, errors.New("test manifest time must be a string")
	}
	return time.Parse(time.RFC3339Nano, text)
}

func testRoomReplayNumber(object map[string]any, field string) (float64, error) {
	value, ok := object[field].(float64)
	if !ok {
		return 0, errors.New("test manifest field must be a number: " + field)
	}
	return value, nil
}

func testRoomReplayString(object map[string]any, field string) (string, error) {
	value, ok := object[field].(string)
	if !ok {
		return "", errors.New("test manifest field must be a string: " + field)
	}
	return value, nil
}

func testRoomReplayParticipants(bundle string, values map[string]any) []RoomReplayParticipant {
	ids := make([]string, 0, len(values))
	for participantID := range values {
		ids = append(ids, participantID)
	}
	sort.Strings(ids)
	participants := make([]RoomReplayParticipant, 0, len(ids))
	for _, participantID := range ids {
		object, ok := values[participantID].(map[string]any)
		if !ok {
			continue
		}
		participant := RoomReplayParticipant{ID: participantID, Kind: ParticipantKind("human")}
		artifacts, ok := object["artifacts"].(map[string]any)
		if !ok {
			continue
		}
		for role, value := range artifacts {
			artifact, ok := value.(map[string]any)
			if !ok {
				continue
			}
			participant.Artifacts = append(participant.Artifacts, testRoomReplayArtifact(bundle, artifact, role, participantID+":"+role))
		}
		participants = append(participants, participant)
	}
	return participants
}

func testRoomReplayGlobalArtifacts(bundle string, values map[string]any) []RoomReplayArtifact {
	artifacts := make([]RoomReplayArtifact, 0, len(values))
	for role, value := range values {
		owner := "room:" + role
		if role == "room_mix" {
			owner = "room:mix"
		}
		artifact, ok := value.(map[string]any)
		if !ok {
			continue
		}
		artifacts = append(artifacts, testRoomReplayArtifact(bundle, artifact, role, owner))
	}
	return artifacts
}

func parseTestRoomReplayTimeline(data []byte) ([]RoomReplayTimelineEvent, error) {
	events := make([]RoomReplayTimelineEvent, 0)
	var previousOffset, previousSequence int64
	first := true
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, err
		}
		sequenceValue, err := testRoomReplayNumber(row, "sequence")
		if err != nil {
			return nil, err
		}
		offsetValue, err := testRoomReplayNumber(row, "monotonic_offset_ms")
		if err != nil {
			return nil, err
		}
		unixValue, err := testRoomReplayNumber(row, "unix_ms")
		if err != nil {
			return nil, err
		}
		sequence, offset, unixMS := int64(sequenceValue), int64(offsetValue), int64(unixValue)
		if !first && (sequence <= previousSequence || offset < previousOffset) {
			return nil, roomReplayAudioMismatch("room_timeline", "room-timeline.jsonl", "ordered timeline", "out of order", nil)
		}
		first = false
		previousSequence, previousOffset = sequence, offset
		participantID, err := testRoomReplayString(row, "participant_id")
		if err != nil {
			return nil, err
		}
		rowType, err := testRoomReplayString(row, "type")
		if err != nil {
			return nil, err
		}
		events = append(events, RoomReplayTimelineEvent{Sequence: sequence, OffsetMS: offset, UnixMS: unixMS, Type: rowType, ParticipantID: participantID, Raw: append(json.RawMessage(nil), line...)})
	}
	return events, nil
}
