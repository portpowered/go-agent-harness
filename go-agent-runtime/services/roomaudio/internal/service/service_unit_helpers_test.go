package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assertRoomReplayStreamMetadataObjects(t *testing.T) {
	t.Helper()
	metadata := parseRoomReplayStreamMetadataObject(json.RawMessage(`{"stream_id":"sent","timeline_start_ms":5,"timeline_end":"20ms","duration_ms":30,"chunk_boundaries":[{"id":"first","sample_index":2},{"sample_index":4}],"expected_speech":[{"label":"turn","start_ms":2,"end_ms":10},{"start_ms":4,"end_ms":3}]}`))
	if metadata.StreamID != "sent" || metadata.TimelineStart != 5*time.Millisecond || metadata.TimelineEnd != 20*time.Millisecond || metadata.Duration != 30*time.Millisecond || len(metadata.ChunkBoundaries) != 2 || len(metadata.ExpectedSpeech) != 1 {
		t.Fatalf("metadata = %+v", metadata)
	}
	parsed := parseRoomReplayStreamMetadata(roomReplayJSONObject{"streams": json.RawMessage(`{"sent_pcm":{"stream_id":"uplink"}}`), "sent": json.RawMessage(`{"timeline_start_ms":1}`)}, "sent")
	if parsed.StreamID != "uplink" || !parsed.HasStart {
		t.Fatalf("metadata aliases = %+v", parsed)
	}
	var destination roomReplayAudioStreamMetadata
	mergeRoomReplayAudioStreamMetadata(&destination, metadata)
	mergeRoomReplayAudioStreamMetadata(nil, metadata)
	if destination.StreamID != "sent" {
		t.Fatalf("merged metadata = %+v", destination)
	}
	mergeRoomReplaySidecarMetadata(map[string]roomReplayAudioStreamMetadata{}, []json.RawMessage{json.RawMessage(`{"role":"unknown"}`), json.RawMessage(`not-json`)})
}

func assertRoomReplayStreamMetadataDurations(t *testing.T) {
	t.Helper()
	if normalizeRoomReplayAudioRole("output-stream") != "wav" || normalizeRoomReplayAudioRole("downlink") != "received" || normalizeRoomReplayAudioRole("other") != "" {
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
	plan := RoomReplayPlan{PCMFormat: RoomReplayPCMFormat{SampleRate: 24000, Channels: 1, SampleWidthBit: 16, ByteOrder: "little", Encoding: "signed_pcm16"}}
	stream, err := loadRoomReplayPCMStream(plan, artifact, "sent", "alpha", "sent")
	if err != nil || stream.SampleCount != 3 || stream.StreamID != "sent" {
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
	if stream, err := loadRoomReplayPCMStream(plan, rawArtifact, "received", "alpha", "received"); err != nil || stream.SampleCount != 2 {
		t.Fatalf("raw PCM stream = %+v err=%v", stream, err)
	}
	jsonlPath := filepath.Join(directory, "events.jsonl")
	if err := os.WriteFile(jsonlPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRoomReplayJSONL(RoomReplayArtifact{Path: "events.jsonl", AbsolutePath: jsonlPath}, "events"); err == nil {
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
	deltaPlan := RoomReplayPlan{ClockBase: time.Unix(0, 0), EndedAt: time.Unix(0, int64(time.Second))}
	deltas, err := loadRoomReplayAudioDeltas(RoomReplayArtifact{Path: "deltas.jsonl", AbsolutePath: deltaPath}, "alpha", "stream", deltaPlan)
	if err != nil || len(deltas) != 1 || deltas[0].TurnID != "turn-1" {
		t.Fatalf("direct deltas = %+v err=%v", deltas, err)
	}
}
