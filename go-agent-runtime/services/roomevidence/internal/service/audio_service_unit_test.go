package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
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
	if got, err := readRoomReplayArtifact(artifact, 16, "sample"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("bounded artifact = %v err=%v", got, err)
	}
	artifact.SHA256 = strings.Repeat("0", 64)
	if _, err := readRoomReplayArtifact(artifact, 16, "sample"); err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) {
		t.Fatalf("digest mismatch = %v", err)
	}
	artifact.SHA256 = ""
	artifact.Empty = true
	if _, err := readRoomReplayArtifact(artifact, 16, "sample"); err == nil {
		t.Fatal("non-empty artifact marked empty was accepted")
	}
	if _, err := readRoomReplayPath(path, 1, "small-bound"); err == nil {
		t.Fatal("oversized bounded path was accepted")
	}
	if _, err := readRoomReplayPath(filepath.Join(directory, "missing"), 16, "missing"); err == nil {
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
