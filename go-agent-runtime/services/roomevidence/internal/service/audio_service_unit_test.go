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
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRoomReplayAudioPayloadForms(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 255})
	cases := []struct {
		name   string
		object roomReplayJSONObject
		kind   string
		want   []byte
	}{
		{name: "content", object: roomReplayJSONObject{"content": json.RawMessage(`"` + encoded + `"`)}, kind: "AUDIO.DELTA"},
		{name: "byte array", object: roomReplayJSONObject{"data": json.RawMessage(`[0,1,2,255]`)}},
		{name: "nested", object: roomReplayJSONObject{"value": json.RawMessage(`{"type":"AUDIO.DELTA","payload":{"data":"` + encoded + `"}}`)}, kind: "event"},
		{name: "pcm alias", object: roomReplayJSONObject{"pcm": json.RawMessage(`{"data":"` + encoded + `"}`)}, kind: "AUDIO.DELTA"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, found, err := roomReplayAudioPayload(test.object, test.kind)
			if err != nil || !found || !bytes.Equal(got, []byte{0, 1, 2, 255}) {
				t.Fatalf("payload = %v, found=%v, err=%v", got, found, err)
			}
		})
	}
	if _, found, err := decodeRoomReplayAudioRaw(json.RawMessage(`null`)); found || err != nil {
		t.Fatalf("null payload = found %v err %v, want absent", found, err)
	}
	if _, _, err := decodeRoomReplayAudioRaw(json.RawMessage(`[-1]`)); err == nil {
		t.Fatal("negative byte was accepted")
	}
	if _, _, err := decodeRoomReplayAudioRaw(json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown payload object was accepted")
	}
	if _, found, err := roomReplayAudioPayload(roomReplayJSONObject{}, "speech"); found || err != nil {
		t.Fatalf("non-audio payload = found %v err %v", found, err)
	}
}

func TestRoomReplayStreamMetadataAndDurationForms(t *testing.T) {
	assertRoomReplayStreamMetadataObjects(t)
	assertRoomReplayStreamMetadataDurations(t)
}

func TestRoomReplayToleranceAndErrorHelpers(t *testing.T) {
	profile, err := parseRoomReplayToleranceProfile(roomReplayJSONObject{"tolerances": json.RawMessage(`{"name":"tight","max_barge_in_latency":"250ms","max_loudness_difference_db":3}`)})
	if err != nil || profile.Name != "tight" || profile.RoomConfig.MaxBargeInLatency != 250*time.Millisecond {
		t.Fatalf("profile = %+v err=%v", profile, err)
	}
	var duration time.Duration
	if err := applyRoomReplayProfileDurationFromObject(roomReplayJSONObject{"duration": json.RawMessage(`"4ms"`)}, "duration", &duration, "duration"); err != nil || duration != 4*time.Millisecond {
		t.Fatalf("object duration = %v err=%v", duration, err)
	}
	var integer int
	if err := applyRoomReplayProfileInt(roomReplayJSONObject{"boundary_delta": json.RawMessage(`2`)}, "boundary_delta", &integer, "boundary_delta"); err != nil || integer != 2 {
		t.Fatalf("profile integer = %d err=%v", integer, err)
	}
	if _, err := roomReplayInt64Raw(json.RawMessage(`"nope"`)); err == nil {
		t.Fatal("invalid profile integer was accepted")
	}
	if err := validateRoomReplayToleranceTightening(DefaultRoomReplayToleranceProfile()); err != nil {
		t.Fatalf("default profile rejected: %v", err)
	}
	loose := DefaultRoomReplayToleranceProfile()
	loose.RoomConfig.MaxBargeInLatency += time.Second
	if err := validateRoomReplayToleranceTightening(loose); err == nil {
		t.Fatal("loosened profile was accepted")
	}
	if errOrDefault(nil, errors.New("fallback")).Error() != "fallback" || errOrDefault(errors.New("cause"), errors.New("fallback")).Error() != "cause" {
		t.Fatal("errOrDefault selected the wrong error")
	}
	if _, err := roomReplayObject(json.RawMessage(`null`)); err == nil {
		t.Fatal("null object was accepted")
	}
}

func TestRoomReplayAnnotationShapes(t *testing.T) {
	participants, streamOwners := testRoomReplayAnnotationParticipants()
	assertRoomReplayAnnotationEvents(t, participants, streamOwners)
	assertRoomReplayAnnotationHelpers(t, participants)
}

func TestRoomReplayBoundsAndPCMHelpers(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "sample.pcm")
	data := []byte{1, 2, 3, 4}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	artifact := RoomReplayArtifact{Path: "sample.pcm", AbsolutePath: path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
	if got, err := readRoomReplayArtifact(directory, artifact, 16, "sample"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("bounded artifact = %v err=%v", got, err)
	}
	artifact.SHA256 = strings.Repeat("0", 64)
	if _, err := readRoomReplayArtifact(directory, artifact, 16, "sample"); err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) {
		t.Fatalf("digest mismatch = %v", err)
	}
	artifact.SHA256 = ""
	artifact.Empty = true
	if _, err := readRoomReplayArtifact(directory, artifact, 16, "sample"); err == nil {
		t.Fatal("non-empty artifact marked empty was accepted")
	}
	if _, err := readRoomReplayPath(directory, path, 1, "small-bound"); err == nil {
		t.Fatal("oversized bounded path was accepted")
	}
	if _, err := readRoomReplayPath(directory, filepath.Join(directory, "missing"), 16, "missing"); err == nil {
		t.Fatal("missing bounded path was accepted")
	}
	if _, err := decodeRoomReplayWAV([]byte("RIFF"), "short.wav"); err == nil {
		t.Fatal("truncated WAV was accepted")
	}
	if _, err := decodeMonoPCM16([]byte{1}, 1, "odd.pcm"); err == nil {
		t.Fatal("odd PCM16 payload was accepted")
	}
	if _, err := decodeMonoPCM16([]byte{1, 0}, 2, "stereo.pcm"); err == nil {
		t.Fatal("stereo PCM16 payload was accepted")
	}
	stream := roomanalysis.PCM16TimedStream{PCM16Input: roomanalysis.PCM16Input{Samples: []int16{1, 2}, SampleRate: 24000, ExpectedSpeech: []streamanalysis.SpeechAnnotation{{Start: time.Millisecond, End: 2 * time.Millisecond}}}}
	cloned := cloneTimedStream(stream)
	cloned.Samples[0] = 9
	if stream.Samples[0] != 1 {
		t.Fatal("timed stream clone shares samples")
	}
	if err := Validate(nil, RoomReplayPlan{}); err == nil {
		t.Fatal("nil service was accepted")
	}
	if _, err := New().Load(RoomReplayPlan{}); err == nil {
		t.Fatal("empty plan was accepted")
	}
	invalid := RoomReplayPlan{ManifestPath: path, ClockBase: time.Unix(2, 0), EndedAt: time.Unix(1, 0)}
	if _, err := New().Load(invalid); err == nil || !errors.Is(err, ErrRoomReplayAudioTimeline) {
		t.Fatalf("negative room duration = %v", err)
	}
}

func TestRoomReplayParticipantShapesAndWAVPCMStreams(t *testing.T) {
	assertRoomReplayParticipantShapes(t)
	assertRoomReplayPCMStreams(t)
}

func TestRoomReplayServiceRejectsUnadmittedShape(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "run-manifest.json")
	manifest := []byte(`{"participants":{}}`)
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	plan := RoomReplayPlan{ManifestPath: manifestPath, ClockBase: time.Unix(0, 0), EndedAt: time.Unix(0, int64(time.Second)), PCMFormat: RoomReplayPCMFormat{SampleRate: 24000, Channels: 1, SampleWidthBits: 16, ByteOrder: "little"}}
	if _, err := New().Load(plan); err == nil || !errors.Is(err, ErrRoomReplayBundleIncomplete) {
		t.Fatalf("missing room mix = %v", err)
	}
	plan.Participants = []RoomReplayParticipant{{ID: "alpha"}}
	if _, err := New().Load(plan); err == nil || !errors.Is(err, ErrRoomReplayBundleIncomplete) {
		t.Fatalf("missing participant manifest object = %v", err)
	}
	if err := Validate(New(), plan); err == nil {
		t.Fatal("Validate accepted an unadmitted plan")
	}
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

const (
	roomReplayTestSentRole     = "sent"
	roomReplayTestReceivedRole = "received"
)

func assertRoomReplayStreamMetadataObjects(t *testing.T) {
	t.Helper()
	metadata := parseRoomReplayStreamMetadataObject(json.RawMessage(`{"stream_id":"sent","timeline_start_ms":5,"timeline_end":"20ms","duration_ms":30,"chunk_boundaries":[{"id":"first","sample_index":2},{"sample_index":4}],"expected_speech":[{"label":"turn","start_ms":2,"end_ms":10},{"start_ms":4,"end_ms":3}]}`))
	if metadata.StreamID != roomReplayTestSentRole || metadata.TimelineStart != 5*time.Millisecond || metadata.TimelineEnd != 20*time.Millisecond || metadata.Duration != 30*time.Millisecond || len(metadata.ChunkBoundaries) != 2 || len(metadata.ExpectedSpeech) != 1 {
		t.Fatalf("metadata = %+v", metadata)
	}
	parsed := parseRoomReplayStreamMetadata(roomReplayJSONObject{"streams": json.RawMessage(`{"sent_pcm":{"stream_id":"uplink"}}`), "sent": json.RawMessage(`{"timeline_start_ms":1}`)}, "sent")
	if parsed.StreamID != "uplink" || !parsed.HasStart {
		t.Fatalf("metadata aliases = %+v", parsed)
	}
	var destination roomReplayAudioStreamMetadata
	mergeRoomReplayAudioStreamMetadata(&destination, metadata)
	mergeRoomReplayAudioStreamMetadata(nil, metadata)
	if destination.StreamID != roomReplayTestSentRole {
		t.Fatalf("merged metadata = %+v", destination)
	}
	mergeRoomReplaySidecarMetadata(map[string]roomReplayAudioStreamMetadata{}, []json.RawMessage{json.RawMessage(`{"role":"unknown"}`), json.RawMessage(`not-json`)})
}

func testRoomReplayAnnotationParticipants() (map[string]RoomReplayAudioParticipant, map[string]string) {
	participants := map[string]RoomReplayAudioParticipant{}
	streamOwners := map[string]string{}
	for _, id := range []string{"a", "b"} {
		participant := RoomReplayAudioParticipant{ID: id}
		participant.WAV.StreamID = id + ":output"
		participant.Sent.StreamID = id + ":sent"
		participant.Received.StreamID = id + ":received"
		participants[id] = participant
		streamOwners[participant.WAV.StreamID] = id
		streamOwners[participant.Sent.StreamID] = id
		streamOwners[participant.Received.StreamID] = id
	}
	return participants, streamOwners
}

func assertRoomReplayAnnotationEvents(t *testing.T, participants map[string]RoomReplayAudioParticipant, streamOwners map[string]string) {
	t.Helper()
	plan := RoomReplayPlan{ClockBase: time.Unix(0, 0), EndedAt: time.Unix(0, int64(time.Second))}
	barge, _, bargeResult, _, recognized, err := parseRoomReplayAudioAnnotation(json.RawMessage(`{"kind":"barge_in","id":"barge","start_ms":10,"duration_ms":20,"source":{"participant_id":"a"},"target":"b"}`), 0, plan, participants, streamOwners)
	if err != nil || !recognized || barge.InterrupterParticipantID != "a" || bargeResult == nil {
		t.Fatalf("barge = %+v result=%+v recognized=%v err=%v", barge, bargeResult, recognized, err)
	}
	loudness, _, _, loudnessResult, recognized, err := parseRoomReplayAudioAnnotation(json.RawMessage(`{"kind":"loudness","id":"loud","start":"10ms","end":"20ms","left":"a","right":"b"}`), 1, plan, participants, streamOwners)
	if err != nil || !recognized || loudness.Participants[0] != "a" || loudnessResult == nil {
		t.Fatalf("loudness = %+v result=%+v recognized=%v err=%v", loudness, loudnessResult, recognized, err)
	}
}

func assertRoomReplayAnnotationHelpers(t *testing.T, participants map[string]RoomReplayAudioParticipant) {
	t.Helper()
	if endpoint := roomReplayAnnotationEndpoint(roomReplayJSONObject{"nested": json.RawMessage(`{"participant_id":"a"}`)}, "nested"); endpoint != "a" {
		t.Fatalf("nested endpoint = %q", endpoint)
	}
	if values := roomReplayAnnotationParticipantList(roomReplayJSONObject{"participants": json.RawMessage(`[{"participant_id":"a"},"b"]`)}); len(values) != 2 || values[0] != "a" || values[1] != "b" {
		t.Fatalf("participant list = %v", values)
	}
	if entries, err := roomReplayAnnotationEntries(json.RawMessage(`{"overlaps":[{"kind":"overlap"}],"loudness":[{"kind":"loudness"}]}`), "annotations"); err != nil || len(entries) != 2 {
		t.Fatalf("nested annotation entries = %v err=%v", entries, err)
	}
	if start, end, err := roomReplayAnnotationInterval(roomReplayJSONObject{"start_ms": json.RawMessage(`1`), "duration_ms": json.RawMessage(`2`)}); err != nil || start != time.Millisecond || end != 3*time.Millisecond {
		t.Fatalf("duration interval = %v..%v err=%v", start, end, err)
	}
	if _, _, err := roomReplayAnnotationInterval(roomReplayJSONObject{}); err == nil {
		t.Fatal("missing annotation interval was accepted")
	}
	if err := validateRoomReplayAnnotationParticipants("x", []string{"a", "missing"}, participants); err == nil {
		t.Fatal("unknown annotation participant was accepted")
	}
}

func assertRoomReplayParticipantShapes(t *testing.T) {
	t.Helper()
	array, err := roomReplayAudioParticipantObjects(roomReplayJSONObject{"participants": json.RawMessage(`[{"id":"alpha"}]`)})
	if err != nil || len(array) != 1 {
		t.Fatalf("participant array = %v err=%v", array, err)
	}
	objects, err := roomReplayAudioParticipantObjects(roomReplayJSONObject{"participants": json.RawMessage(`{"alpha":{"id":"alpha"}}`)})
	if err != nil || len(objects) != 1 {
		t.Fatalf("participant map = %v err=%v", objects, err)
	}
	if _, err := roomReplayAudioParticipantObjects(roomReplayJSONObject{"participants": json.RawMessage(`{"key":{"id":"other"}}`)}); err == nil {
		t.Fatal("participant map identity mismatch was accepted")
	}
	if _, err := roomReplayAudioParticipantObjects(roomReplayJSONObject{"participants": json.RawMessage(`not-json`)}); err == nil {
		t.Fatal("invalid participant map was accepted")
	}
}

func assertRoomReplayPCMStreams(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	wavData := mustRoomReplayWAV(t, []int16{1, 2, 3})
	path := filepath.Join(directory, "stream.wav")
	if err := os.WriteFile(path, wavData, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := RoomReplayArtifact{Path: "stream.wav", AbsolutePath: path}
	plan := RoomReplayPlan{BundlePath: directory, PCMFormat: RoomReplayPCMFormat{SampleRate: 24000, Channels: 1, SampleWidthBit: 16, ByteOrder: "little", Encoding: "signed_pcm16"}}
	stream, err := loadRoomReplayPCMStream(plan, artifact, roomReplayTestSentRole, "alpha", roomReplayTestSentRole)
	if err != nil || stream.SampleCount != 3 || stream.StreamID != roomReplayTestSentRole {
		t.Fatalf("WAV-backed PCM stream = %+v err=%v", stream, err)
	}
	if err := validateRoomReplayWAVFormat(roomReplayWAVPayload{SampleRate: 24000, Channels: 1, Bits: 16}, plan.PCMFormat, "stream.wav"); err != nil {
		t.Fatalf("singular sample width was rejected: %v", err)
	}
	assertRoomReplayRawPCMAndJSONL(t, directory, plan)
}

func assertRoomReplayRawPCMAndJSONL(t *testing.T, directory string, plan RoomReplayPlan) {
	t.Helper()
	rawPath := filepath.Join(directory, "stream.pcm")
	if err := os.WriteFile(rawPath, []byte{1, 0, 2, 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	rawArtifact := RoomReplayArtifact{Path: "stream.pcm", AbsolutePath: rawPath}
	if stream, err := loadRoomReplayPCMStream(plan, rawArtifact, roomReplayTestReceivedRole, "alpha", roomReplayTestReceivedRole); err != nil || stream.SampleCount != 2 {
		t.Fatalf("raw PCM stream = %+v err=%v", stream, err)
	}
	jsonlPath := filepath.Join(directory, "events.jsonl")
	if err := os.WriteFile(jsonlPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRoomReplayJSONL(directory, RoomReplayArtifact{Path: "events.jsonl", AbsolutePath: jsonlPath}, "events"); err == nil {
		t.Fatal("malformed JSONL was accepted")
	}
	assertRoomReplayDirectDeltas(t, directory)
}

func assertRoomReplayDirectDeltas(t *testing.T, directory string) {
	t.Helper()
	deltaPath := filepath.Join(directory, "deltas.jsonl")
	deltaData := []byte("{\"type\":\"turn\"}\n{\"type\":\"AUDIO.DELTA\",\"content\":[1,0],\"sequence\":0,\"offset_ms\":0,\"stream_id\":\"stream\",\"participant_id\":\"alpha\",\"turn_id\":\"turn-1\"}\n")
	if err := os.WriteFile(deltaPath, deltaData, 0o600); err != nil {
		t.Fatal(err)
	}
	deltaPlan := RoomReplayPlan{BundlePath: directory, ClockBase: time.Unix(0, 0), EndedAt: time.Unix(0, int64(time.Second))}
	deltas, err := loadRoomReplayAudioDeltas(RoomReplayArtifact{Path: "deltas.jsonl", AbsolutePath: deltaPath}, "alpha", "stream", deltaPlan)
	if err != nil || len(deltas) != 1 || deltas[0].TurnID != "turn-1" {
		t.Fatalf("direct deltas = %+v err=%v", deltas, err)
	}
}
