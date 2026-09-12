package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
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

func assertRoomReplayAudioError(t *testing.T, err, target error, context string) {
	t.Helper()
	if err == nil || !errors.Is(err, target) || !strings.Contains(err.Error(), context) {
		t.Fatalf("error = %v, want %v with %q", err, target, context)
	}
}

func mustRoomReplayObject(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", label)
	}
	return object
}

func writeRoomReplayAudioBundle(t *testing.T) (string, map[string]any, map[string][]int16) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "participants", "alpha"), 0o700); err != nil {
		t.Fatalf("create alpha directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "participants", "beta"), 0o700); err != nil {
		t.Fatalf("create beta directory: %v", err)
	}
	want := map[string][]int16{
		"alpha:wav":      {1000, 2000, 3000, 4000},
		"alpha:sent":     {1100, 2100, 3100, 4100},
		"alpha:received": {1200, 2200, 3200, 4200},
		"beta:wav":       {1400, 2400, 3400, 4400},
		"beta:sent":      {1500, 2500, 3500, 4500},
		"beta:received":  {1600, 2600, 3600, 4600},
		"room:mix":       {1700, 2700, 3700, 4700},
	}
	paths := make(map[string][]byte)
	for _, participantID := range []string{"alpha", "beta"} {
		wav := mustRoomReplayWAV(t, want[participantID+":wav"])
		paths["participants/"+participantID+"/agent.wav"] = wav
		paths["participants/"+participantID+"/sent.pcm"] = roomReplayAudioPCM16Bytes(want[participantID+":sent"])
		paths["participants/"+participantID+"/received.pcm"] = roomReplayAudioPCM16Bytes(want[participantID+":received"])
		paths["participants/"+participantID+"/deltas.jsonl"] = jsonLines(t, []map[string]any{
			{"type": "AUDIO.DELTA", "sequence": 0, "delta_id": participantID + "-delta-0", "offset_ms": 0, "delta": base64.StdEncoding.EncodeToString(roomReplayAudioPCM16Bytes(want[participantID+":wav"][:2]))},
			{"type": "AUDIO.DELTA", "sequence": 1, "delta_id": participantID + "-delta-1", "offset_ms": 0, "delta": base64.StdEncoding.EncodeToString(roomReplayAudioPCM16Bytes(want[participantID+":wav"][2:]))},
		})
		paths["participants/"+participantID+"/events.jsonl"] = jsonLines(t, []map[string]any{
			{"stream_role": "sent", "stream_id": participantID + ":sent", "timeline_start_ms": 0, "timeline_end_ms": 100, "expected_speech": []any{map[string]any{"label": "turn-1", "start_ms": 10, "end_ms": 90}}},
			{"stream_role": "received", "stream_id": participantID + ":received", "timeline_start_ms": 0, "timeline_end_ms": 100},
		})
		paths["participants/"+participantID+"/diagnostics.jsonl"] = jsonLines(t, []map[string]any{{"event": "turn", "turn_id": participantID + "-turn-1"}})
	}
	paths["room-mix.wav"] = mustRoomReplayWAV(t, want["room:mix"])
	clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	paths["room-timeline.jsonl"] = []byte(fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"alpha"}`+"\n"+`{"sequence":1,"monotonic_offset_ms":10,"unix_ms":%d,"type":"speech_start","participant_id":"beta"}`+"\n", clock.UnixMilli(), clock.UnixMilli()+10))
	for name, data := range paths {
		path := filepath.Join(bundle, filepath.FromSlash(name))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	manifest := map[string]any{
		"schema_version": 2,
		"finalized":      true,
		"clock_base":     clock.Format(time.RFC3339Nano),
		"timing": map[string]any{
			"started_at": clock.Format(time.RFC3339Nano),
			"ended_at":   clock.Add(100 * time.Millisecond).Format(time.RFC3339Nano),
			"elapsed":    "100ms",
		},
		"pcm_format":   map[string]any{"sample_rate_hz": 24000, "channels": 1, "sample_width_bits": 16, "byte_order": "little", "encoding": "signed_pcm16"},
		"participants": map[string]any{},
		"artifacts":    map[string]any{"room_timeline": roomReplayAudioArtifactValue(paths["room-timeline.jsonl"], "room-timeline.jsonl"), "room_mix": roomReplayAudioArtifactValue(paths["room-mix.wav"], "room-mix.wav")},
		"annotations":  map[string]any{"overlaps": []any{map[string]any{"kind": "overlap", "id": "overlap-1", "start_ms": 10, "end_ms": 90, "participants": []any{"alpha", "beta"}}}},
	}
	participants, ok := manifest["participants"].(map[string]any)
	if !ok {
		t.Fatal("fixture participants is not an object")
	}
	for _, participantID := range []string{"alpha", "beta"} {
		artifactValues := map[string]any{}
		for role, name := range map[string]string{
			roomReplayArtifactRoleWAV:         "participants/" + participantID + "/agent.wav",
			roomReplayArtifactRoleDiagnostics: "participants/" + participantID + "/diagnostics.jsonl",
			roomReplayArtifactRoleDeltas:      "participants/" + participantID + "/deltas.jsonl",
			roomReplayArtifactRoleSentPCM:     "participants/" + participantID + "/sent.pcm",
			roomReplayArtifactRoleReceivedPCM: "participants/" + participantID + "/received.pcm",
			roomReplayArtifactRoleEvents:      "participants/" + participantID + "/events.jsonl",
		} {
			artifactValues[role] = roomReplayAudioArtifactValue(paths[name], name)
		}
		participantObject := map[string]any{"id": participantID, "kind": "human", "artifacts": artifactValues}
		if participantID == "alpha" {
			participantObject["streams"] = map[string]any{"wav": map[string]any{"stream_id": "alpha:output", "timeline_start_ms": 0, "timeline_end_ms": 100}, "sent": map[string]any{"stream_id": "alpha:sent"}, "received": map[string]any{"stream_id": "alpha:received"}}
		} else {
			participantObject["streams"] = map[string]any{"wav": map[string]any{"stream_id": "beta:output", "timeline_start_ms": 0, "timeline_end_ms": 100}, "sent": map[string]any{"stream_id": "beta:sent"}, "received": map[string]any{"stream_id": "beta:received"}}
		}
		participants[participantID] = participantObject
	}
	writeRoomReplayAudioManifest(t, bundle, manifest)
	return bundle, manifest, want
}

func writeRoomReplayAudioManifest(t *testing.T, bundle string, manifest map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal audio manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, RoomReplayBundleManifestPath), append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write audio manifest: %v", err)
	}
}

func updateRoomReplayArtifact(t *testing.T, manifest map[string]any, role string, data []byte) {
	t.Helper()
	artifacts, ok := manifest["artifacts"].(map[string]any)
	if !ok {
		t.Fatal("fixture artifacts is not an object")
	}
	value, ok := artifacts[role].(map[string]any)
	if !ok {
		t.Fatalf("fixture artifact %q is not an object", role)
	}
	value["size"] = len(data)
	digest := sha256.Sum256(data)
	value["sha256"] = hex.EncodeToString(digest[:])
}

func updateRoomReplayParticipantArtifact(t *testing.T, manifest map[string]any, participantID, role string, data []byte) {
	t.Helper()
	participants, ok := manifest["participants"].(map[string]any)
	if !ok {
		t.Fatal("fixture participants is not an object")
	}
	participant, ok := participants[participantID].(map[string]any)
	if !ok {
		t.Fatalf("fixture participant %q is not an object", participantID)
	}
	artifacts, ok := participant["artifacts"].(map[string]any)
	if !ok {
		t.Fatalf("fixture participant %q artifacts is not an object", participantID)
	}
	value, ok := artifacts[role].(map[string]any)
	if !ok {
		t.Fatalf("fixture artifact %q for participant %q is not an object", role, participantID)
	}
	value["size"] = len(data)
	digest := sha256.Sum256(data)
	value["sha256"] = hex.EncodeToString(digest[:])
}

func roomReplayAudioArtifactValue(data []byte, path string) map[string]any {
	digest := sha256.Sum256(data)
	return map[string]any{"path": path, "size": len(data), "sha256": hex.EncodeToString(digest[:])}
}

func mustRoomReplayWAV(t *testing.T, samples []int16) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := wavio.Write(&buffer, 24000, samples); err != nil {
		t.Fatalf("write WAV: %v", err)
	}
	return buffer.Bytes()
}

func roomReplayAudioPCM16Bytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*2:], uint16(sample))
	}
	return data
}

func jsonLines(t *testing.T, values []map[string]any) []byte {
	t.Helper()
	var buffer bytes.Buffer
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal JSONL value: %v", err)
		}
		buffer.Write(data)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

func loadTestRoomReplayAudioBundle(bundle string) (RoomReplayAudioBundle, error) {
	plan, err := testRoomReplayPlan(bundle)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	return New().Load(plan)
}

func testRoomReplayPlan(bundle string) (RoomReplayPlan, error) {
	resolvedBundle, manifest, err := readTestRoomReplayManifest(bundle)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	plan, err := testRoomReplayPlanHeader(resolvedBundle, manifest)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	participants, ok := manifest["participants"].(map[string]any)
	if !ok {
		return RoomReplayPlan{}, errors.New("test manifest participants must be an object")
	}
	artifacts, ok := manifest["artifacts"].(map[string]any)
	if !ok {
		return RoomReplayPlan{}, errors.New("test manifest artifacts must be an object")
	}
	plan.Participants = testRoomReplayParticipants(resolvedBundle, participants)
	plan.Artifacts = testRoomReplayGlobalArtifacts(resolvedBundle, artifacts)
	if err := validateTestRoomReplayArtifacts(plan); err != nil {
		return RoomReplayPlan{}, err
	}
	timelineData, err := os.ReadFile(plan.TimelinePath)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	plan.Timeline, err = parseTestRoomReplayTimeline(timelineData)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	return plan, nil
}

func testRoomReplayArtifact(bundle string, value map[string]any, role, owner string) RoomReplayArtifact {
	path, ok := value["path"].(string)
	if !ok {
		panic("fixture artifact path is not a string")
	}
	size, ok := value["size"].(float64)
	if !ok {
		panic("fixture artifact size is not a number")
	}
	sha256Value, ok := value["sha256"].(string)
	if !ok {
		panic("fixture artifact sha256 is not a string")
	}
	return RoomReplayArtifact{
		Name: role, Role: role, Owner: owner, Path: path,
		AbsolutePath: filepath.Join(bundle, filepath.FromSlash(path)),
		Size:         int64(size), SHA256: sha256Value,
	}
}

func validateTestRoomReplayArtifacts(plan RoomReplayPlan) error {
	artifacts := append([]RoomReplayArtifact(nil), plan.Artifacts...)
	for _, participant := range plan.Participants {
		artifacts = append(artifacts, participant.Artifacts...)
	}
	for _, artifact := range artifacts {
		data, err := os.ReadFile(artifact.AbsolutePath)
		if err != nil {
			return roomReplayAudioIncomplete(artifact.Path, artifact.Path, "readable artifact", err.Error(), err)
		}
		if int64(len(data)) != artifact.Size {
			return roomReplayAudioMismatch(artifact.Path, artifact.Path, fmt.Sprintf("size %d", artifact.Size), fmt.Sprintf("size %d", len(data)), nil)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != strings.ToLower(artifact.SHA256) {
			return roomReplayAudioMismatch(artifact.Path, artifact.Path, artifact.SHA256, hex.EncodeToString(digest[:]), nil)
		}
	}
	return nil
}
