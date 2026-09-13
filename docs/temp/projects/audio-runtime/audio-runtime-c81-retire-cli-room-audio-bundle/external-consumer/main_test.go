package consumer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio/wire"
)

func TestExternalConsumerLoadsLiteralPCM16WAVAndRejectsDeltaMutation(t *testing.T) {
	fixture := newFixture(t)
	service := wire.NewService()
	loaded, err := service.Load(fixture.plan)
	if err != nil {
		t.Fatalf("roomaudio.Load: %v", err)
	}
	alpha, ok := loaded.Participant("alpha")
	if !ok || alpha.WAV.StreamID != "alpha:output" || len(alpha.WAV.Samples) != 2 || alpha.WAV.Samples[0] != 100 || alpha.Sent.Samples[0] != 300 || loaded.RoomMix.Samples[1] != 600 {
		t.Fatalf("loaded audio = participant:%+v mix:%+v", alpha, loaded.RoomMix)
	}
	if len(alpha.WAV.Deltas) != 1 || alpha.WAV.Deltas[0].ID != "alpha-delta" {
		t.Fatalf("delta order = %+v", alpha.WAV.Deltas)
	}
	alpha.WAV.PCM[0] = 0
	if loaded.Participants[0].WAV.PCM[0] == 0 {
		t.Fatal("public participant projection shared mutable PCM")
	}

	mutated := []byte(`{"type":"AUDIO.DELTA","delta_id":"alpha-delta","delta":"` + base64.StdEncoding.EncodeToString(pcm16([]int16{999, 200})) + `"}` + "\n")
	writeFixtureFile(t, fixture.root, "participants/alpha/deltas.jsonl", mutated)
	fixture.plan.Participants[0].Artifacts[1] = fixture.artifact("participants/alpha/deltas.jsonl", roomaudio.RoomReplayAudioRoleDeltas)
	_, err = service.Load(fixture.plan)
	var reconstruction *roomaudio.RoomReplayDeltaReconstructionError
	if err == nil || !errors.Is(err, roomaudio.ErrRoomReplayDeltaReconstruction) || !errors.As(err, &reconstruction) {
		t.Fatalf("mutated delta error = %v, detail = %+v", err, reconstruction)
	}
}

type fixture struct {
	root string
	plan roomaudio.RoomReplayPlan
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	clock := time.Unix(0, 0).UTC()
	files := map[string][]byte{
		"participants/alpha/agent.wav":      wav16(8000, []int16{100, 200}),
		"participants/alpha/sent.pcm":      pcm16([]int16{300, 400}),
		"participants/alpha/received.pcm":  pcm16([]int16{500, 600}),
		"participants/alpha/events.jsonl":  []byte(`{"type":"event"}` + "\n"),
		"participants/alpha/diagnostics.jsonl": []byte(`{"type":"diagnostic"}` + "\n"),
		"participants/alpha/deltas.jsonl": []byte(`{"type":"AUDIO.DELTA","delta_id":"alpha-delta","offset_ms":0,"delta":"` + base64.StdEncoding.EncodeToString(pcm16([]int16{100, 200})) + `"}` + "\n"),
		"room-mix.wav":                       wav16(8000, []int16{500, 600}),
	}
	for name, data := range files {
		writeFixtureFile(t, root, name, data)
	}
	manifest := map[string]any{"participants": map[string]any{"alpha": map[string]any{"id": "alpha"}}}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, root, "run-manifest.json", manifestData)
	f := &fixture{root: root}
	participant := roomaudio.RoomReplayParticipant{ID: "alpha", Kind: roomaudio.ParticipantKind("agent")}
	for _, rolePath := range []struct{ role, path string }{
		{roomaudio.RoomReplayAudioRoleWAV, "participants/alpha/agent.wav"},
		{roomaudio.RoomReplayAudioRoleDeltas, "participants/alpha/deltas.jsonl"},
		{roomaudio.RoomReplayAudioRoleSentPCM, "participants/alpha/sent.pcm"},
		{roomaudio.RoomReplayAudioRoleReceivedPCM, "participants/alpha/received.pcm"},
		{roomaudio.RoomReplayAudioRoleEvents, "participants/alpha/events.jsonl"},
		{roomaudio.RoomReplayAudioRoleDiagnostics, "participants/alpha/diagnostics.jsonl"},
	} {
		participant.Artifacts = append(participant.Artifacts, f.artifact(rolePath.path, rolePath.role))
	}
	f.plan = roomaudio.RoomReplayPlan{
		BundlePath: root, ManifestPath: filepath.Join(root, "run-manifest.json"), SchemaVersion: roomaudio.RoomReplayBundleSchemaVersion,
		Finalized: true, ClockBase: clock, StartedAt: clock, EndedAt: clock.Add(time.Second),
		PCMFormat: roomaudio.RoomReplayPCMFormat{SampleRate: 8000, Channels: 1, SampleWidthBits: 16, ByteOrder: "little", Encoding: "signed_pcm16"},
		Participants: []roomaudio.RoomReplayParticipant{participant},
		Artifacts: []roomaudio.RoomReplayArtifact{f.artifact("room-mix.wav", "room:mix")},
	}
	return f
}

func (f *fixture) artifact(path, role string) roomaudio.RoomReplayArtifact {
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(path)))
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(data)
	owner := "alpha:" + role
	if role == "room:mix" {
		owner = "room:mix"
	}
	return roomaudio.RoomReplayArtifact{Name: role, Role: role, Owner: owner, Path: path, AbsolutePath: filepath.Join(f.root, filepath.FromSlash(path)), Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
}

func writeFixtureFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pcm16(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*2:], uint16(sample))
	}
	return data
}

func wav16(sampleRate int, samples []int16) []byte {
	data := pcm16(samples)
	result := make([]byte, 44+len(data))
	copy(result[0:4], "RIFF")
	binary.LittleEndian.PutUint32(result[4:8], uint32(36+len(data)))
	copy(result[8:12], "WAVE")
	copy(result[12:16], "fmt ")
	binary.LittleEndian.PutUint32(result[16:20], 16)
	binary.LittleEndian.PutUint16(result[20:22], 1)
	binary.LittleEndian.PutUint16(result[22:24], 1)
	binary.LittleEndian.PutUint32(result[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(result[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(result[32:34], 2)
	binary.LittleEndian.PutUint16(result[34:36], 16)
	copy(result[36:40], "data")
	binary.LittleEndian.PutUint32(result[40:44], uint32(len(data)))
	copy(result[44:], data)
	return result
}
