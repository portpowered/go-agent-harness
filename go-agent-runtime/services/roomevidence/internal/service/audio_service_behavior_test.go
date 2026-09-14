package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestLoadRoomReplayAudioBundleResolvesIdentityTimingAndExactDeltas(t *testing.T) {
	bundle, manifest, want := writeRoomReplayAudioBundle(t)
	got, err := loadTestRoomReplayAudioBundle(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayAudioBundle: %v", err)
	}
	resolvedBundle, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		t.Fatalf("resolve fixture bundle: %v", err)
	}
	assertRoomReplayBundleMetadata(t, got, resolvedBundle)
	assertRoomReplayBundleParticipant(t, got, want)
	assertRoomReplayBundleRoomEvidence(t, got, want)
	assertRoomReplayBundleAnalysis(t, got)
	if _, ok := manifest["participants"]; !ok {
		t.Fatal("fixture helper returned malformed manifest")
	}
}

func TestLoadRoomReplayAudioBundleRejectsMissingHashTimelineAndFormatEvidence(t *testing.T) {
	t.Run("missing artifact", func(t *testing.T) {
		bundle, _, _ := writeRoomReplayAudioBundle(t)
		if err := os.Remove(filepath.Join(bundle, "participants", "alpha", "sent.pcm")); err != nil {
			t.Fatalf("remove sent PCM: %v", err)
		}
		_, err := loadTestRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrRoomReplayBundleIncomplete, "sent.pcm")
	})

	t.Run("hash mismatch", func(t *testing.T) {
		bundle, _, _ := writeRoomReplayAudioBundle(t)
		path := filepath.Join(bundle, "participants", "beta", "received.pcm")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read received PCM: %v", err)
		}
		data[0] ^= 0xff
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("mutate received PCM: %v", err)
		}
		_, err = loadTestRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "participants/beta/received.pcm")
		if !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "actual") {
			t.Fatalf("hash error = %v, want actual-versus-expected digest", err)
		}
	})

	t.Run("timeline mismatch", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		path := filepath.Join(bundle, "room-timeline.jsonl")
		data := []byte(fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":20,"unix_ms":%d,"type":"speech_start","participant_id":"alpha"}`+"\n"+`{"sequence":1,"monotonic_offset_ms":10,"unix_ms":%d,"type":"speech_start","participant_id":"beta"}`+"\n", clock.UnixMilli()+20, clock.UnixMilli()+10))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("rewrite timeline: %v", err)
		}
		updateRoomReplayArtifact(t, manifest, "room_timeline", data)
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := loadTestRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "room_timeline")
		if !strings.Contains(err.Error(), "ordered") {
			t.Fatalf("timeline error = %v, want ordering diagnostic", err)
		}
	})

	t.Run("PCM format mismatch", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		format := mustRoomReplayObject(t, manifest["pcm_format"], "fixture pcm_format")
		format["sample_rate_hz"] = 16000
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := loadTestRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "pcm_format")
		if !strings.Contains(err.Error(), "rate=16000") || !strings.Contains(err.Error(), "24000") {
			t.Fatalf("format error = %v, want declared and actual rates", err)
		}
	})

	t.Run("duplicate stream identity", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		participants := mustRoomReplayObject(t, manifest["participants"], "fixture participants")
		beta := mustRoomReplayObject(t, participants["beta"], "fixture beta participant")
		betaStreams := mustRoomReplayObject(t, beta["streams"], "fixture beta streams")
		betaSent := mustRoomReplayObject(t, betaStreams["sent"], "fixture beta sent stream")
		betaSent["stream_id"] = "alpha:sent"
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := loadTestRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "streams.alpha:sent")
	})
}

func TestLoadRoomReplayAudioBundleRejectsEveryDeltaReconstructionMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]map[string]any)
	}{
		{
			name:   "dropped delta",
			mutate: func([]map[string]any) {},
		},
		{
			name: "reordered delta",
			mutate: func(lines []map[string]any) {
				lines[0], lines[1] = lines[1], lines[0]
			},
		},
		{
			name: "duplicated delta",
			mutate: func(lines []map[string]any) {
				lines[1]["delta"] = lines[0]["delta"]
			},
		},
		{
			name: "altered delta",
			mutate: func(lines []map[string]any) {
				delta, ok := lines[0]["delta"].(string)
				if !ok {
					panic("fixture delta is not a string")
				}
				decoded, err := base64.StdEncoding.DecodeString(delta)
				if err != nil {
					panic(err)
				}
				decoded[0] ^= 0xff
				lines[0]["delta"] = base64.StdEncoding.EncodeToString(decoded)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { runRoomReplayDeltaMutationTest(t, test.name, test.mutate) })
	}
}

func TestLoadRoomReplayAudioBundleRejectsDuplicateDeltaIdentity(t *testing.T) {
	bundle, manifest, _ := writeRoomReplayAudioBundle(t)
	path := filepath.Join(bundle, "participants", "alpha", "deltas.jsonl")
	data := jsonLines(t, []map[string]any{
		{"type": "AUDIO.DELTA", "sequence": 0, "delta_id": "same-delta", "delta": base64.StdEncoding.EncodeToString(roomReplayAudioPCM16Bytes([]int16{1000, 2000}))},
		{"type": "AUDIO.DELTA", "sequence": 1, "delta_id": "same-delta", "delta": base64.StdEncoding.EncodeToString(roomReplayAudioPCM16Bytes([]int16{3000, 4000}))},
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("rewrite duplicate deltas: %v", err)
	}
	updateRoomReplayParticipantArtifact(t, manifest, "alpha", "deltas", data)
	writeRoomReplayAudioManifest(t, bundle, manifest)

	_, err := loadTestRoomReplayAudioBundle(bundle)
	assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "unique delta identity")
}

func TestLoadRoomReplayAudioBundleEnforcesAnnotationIdentityAndToleranceBounds(t *testing.T) {
	t.Run("unknown overlap participant", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		manifest["annotations"] = []any{map[string]any{"kind": "overlap", "id": "bad-overlap", "start_ms": 10, "end_ms": 20, "participants": []any{"alpha", "missing"}}}
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := loadTestRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "bad-overlap")
		if !strings.Contains(err.Error(), "missing") {
			t.Fatalf("annotation error = %v, want absent participant identity", err)
		}
	})

	t.Run("profile may tighten", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		manifest["tolerances"] = map[string]any{"name": "tight", "max_barge_in_latency": "250ms", "max_loudness_difference_db": 3}
		writeRoomReplayAudioManifest(t, bundle, manifest)
		got, err := loadTestRoomReplayAudioBundle(bundle)
		if err != nil {
			t.Fatalf("tightened profile: %v", err)
		}
		if got.Tolerances.Name != "tight" || got.Tolerances.RoomConfig.MaxBargeInLatency != 250*time.Millisecond || got.Tolerances.RoomConfig.MaxLoudnessDifferenceDB != 3 {
			t.Fatalf("tightened profile = %+v", got.Tolerances)
		}
	})

	t.Run("profile cannot loosen", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		manifest["tolerances"] = map[string]any{"max_barge_in_latency": "1s"}
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := loadTestRoomReplayAudioBundle(bundle)
		if err == nil || !errors.Is(err, ErrRoomReplayToleranceProfile) || !strings.Contains(err.Error(), "max_barge_in_latency") {
			t.Fatalf("loosened profile error = %v, want explicit profile rejection", err)
		}
	})

	t.Run("stream samples cannot exceed room duration", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		// Keep the declared stream interval inside the room while making the
		// four-sample payload (about 167us at 24kHz) exceed the room by 67us.
		timing := mustRoomReplayObject(t, manifest["timing"], "fixture timing")
		timing["ended_at"] = clock.Add(100 * time.Microsecond).Format(time.RFC3339Nano)
		participants := mustRoomReplayObject(t, manifest["participants"], "fixture participants")
		for _, participantID := range []string{"alpha", "beta"} {
			participant := mustRoomReplayObject(t, participants[participantID], "fixture participant "+participantID)
			streams := mustRoomReplayObject(t, participant["streams"], "fixture participant "+participantID+" streams")
			wav := mustRoomReplayObject(t, streams["wav"], "fixture participant "+participantID+" wav stream")
			wav["timeline_end_ms"] = "100us"
		}
		timeline := []byte(fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"alpha"}`+"\n"+`{"sequence":1,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"beta"}`+"\n", clock.UnixMilli(), clock.UnixMilli()))
		if err := os.WriteFile(filepath.Join(bundle, "room-timeline.jsonl"), timeline, 0o600); err != nil {
			t.Fatalf("rewrite zero-duration timeline: %v", err)
		}
		updateRoomReplayArtifact(t, manifest, "room_timeline", timeline)
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := loadTestRoomReplayAudioBundle(bundle)
		if err == nil || !errors.Is(err, ErrRoomReplayAudioTimeline) || !strings.Contains(err.Error(), "samples") {
			t.Fatalf("out-of-range sample error = %v, want timeline diagnostic", err)
		}
	})
}

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

func assertRoomReplayStreamMetadataDurations(t *testing.T) {
	t.Helper()
	if normalizeRoomReplayAudioRole("output-stream") != "wav" || normalizeRoomReplayAudioRole("downlink") != roomReplayTestReceivedRole || normalizeRoomReplayAudioRole("other") != "" {
		t.Fatal("audio role normalization lost an alias")
	}
	if value, err := roomReplayDurationValue(json.RawMessage(`"5"`), true); err != nil || value != 5*time.Millisecond {
		t.Fatalf("numeric duration string = %v %v", value, err)
	}
	if value, err := roomReplayDurationValue(json.RawMessage(`5`), true); err != nil || value != 5*time.Millisecond {
		t.Fatalf("numeric duration = %v %v", value, err)
	}
	if _, err := roomReplayDurationValue(json.RawMessage(`5`), false); err == nil {
		t.Fatal("numeric duration without units was accepted")
	}
	if _, err := roomReplayDurationValue(json.RawMessage(`"not-a-duration"`), true); err == nil {
		t.Fatal("invalid duration was accepted")
	}
	if _, err := roomReplayDurationValue(json.RawMessage(`9223372036854775807`), true); err == nil {
		t.Fatal("overflowing duration was accepted")
	}
	if value, _, present, err := roomReplayFirstIntField(roomReplayJSONObject{"sample": json.RawMessage(`4`)}, "sample"); err != nil || !present || value != 4 {
		t.Fatalf("integer field = %d %v %v", value, present, err)
	}
	if _, _, _, err := roomReplayFirstIntField(roomReplayJSONObject{"sample": json.RawMessage(`"bad"`)}, "sample"); err == nil {
		t.Fatal("invalid integer field was accepted")
	}
	if got := parseRoomReplayChunkBoundaries(json.RawMessage(`[{"id":"x","sample_index":2},{"sample_index":0},null]`)); len(got) != 1 || got[0].ID != "x" {
		t.Fatalf("chunk boundaries = %+v", got)
	}
}
