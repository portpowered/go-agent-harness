package agentruntime

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

	got, err := LoadRoomReplayAudioBundle(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayAudioBundle: %v", err)
	}
	resolvedBundle, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		t.Fatalf("resolve fixture bundle: %v", err)
	}
	if got.Plan.BundlePath != resolvedBundle || got.Format.SampleRate != 24000 || got.Format.Channels != 1 || got.Format.SampleWidthBits != 16 {
		t.Fatalf("bundle metadata = %+v, want resolved bundle and PCM16 format", got)
	}
	if got.Tolerances.StreamConfig != DefaultRoomReplayToleranceProfile().StreamConfig || got.Tolerances.RoomConfig != DefaultRoomReplayToleranceProfile().RoomConfig {
		t.Fatalf("default tolerance profile = %+v, want suite defaults", got.Tolerances)
	}
	if len(got.Participants) != 2 || got.Participants[0].ID != "alpha" || got.Participants[1].ID != "beta" {
		t.Fatalf("participants = %+v, want stable manifest identities", got.Participants)
	}
	alpha, ok := got.Participant("alpha")
	if !ok {
		t.Fatal("alpha participant missing")
	}
	if alpha.WAV.StreamID != "alpha:output" || alpha.Sent.StreamID != "alpha:sent" || alpha.Received.StreamID != "alpha:received" {
		t.Fatalf("alpha stream identities = %q/%q/%q", alpha.WAV.StreamID, alpha.Sent.StreamID, alpha.Received.StreamID)
	}
	if !bytes.Equal(alpha.WAV.PCM, roomReplayAudioPCM16Bytes(want["alpha:wav"])) || len(alpha.WAV.Deltas) != 2 || alpha.WAV.SampleCount != len(want["alpha:wav"]) {
		t.Fatalf("alpha WAV evidence = bytes:%v deltas:%d samples:%d", alpha.WAV.PCM, len(alpha.WAV.Deltas), alpha.WAV.SampleCount)
	}
	if !bytes.Equal(alpha.Sent.PCM, roomReplayAudioPCM16Bytes(want["alpha:sent"])) || !bytes.Equal(alpha.Received.PCM, roomReplayAudioPCM16Bytes(want["alpha:received"])) {
		t.Fatalf("alpha sent/received PCM was not resolved exactly")
	}
	if len(alpha.Events) != 2 || len(alpha.Diagnostics) != 1 {
		t.Fatalf("alpha sidecars = events:%d diagnostics:%d, want complete JSONL evidence", len(alpha.Events), len(alpha.Diagnostics))
	}
	if got.RoomMix.StreamID != "room:mix" || got.RoomMix.SampleCount != len(want["room:mix"]) {
		t.Fatalf("room mix = %+v, want decoded room-level WAV", got.RoomMix)
	}
	if len(got.Plan.Timeline) != 2 || got.Plan.Timeline[0].OffsetMS != 0 || got.Plan.Timeline[1].ParticipantID != "beta" {
		t.Fatalf("timeline = %+v, want ordered room timeline", got.Plan.Timeline)
	}
	if len(got.Overlaps) != 1 || got.Overlaps[0].A.SentStreamID != "alpha:sent" || got.Overlaps[0].B.ReceivedStreamID != "beta:received" {
		t.Fatalf("overlap annotations = %+v, want independent sent/received identities", got.Overlaps)
	}
	input := got.AnalysisInput()
	if len(input.Streams) != 7 || len(input.Overlaps) != 1 || input.Streams[0].StreamID != "alpha:output" {
		t.Fatalf("analysis input = streams:%d overlaps:%d first:%q, want all resolved streams", len(input.Streams), len(input.Overlaps), input.Streams[0].StreamID)
	}
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
		_, err := LoadRoomReplayAudioBundle(bundle)
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
		_, err = LoadRoomReplayAudioBundle(bundle)
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
		_, err := LoadRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "room_timeline")
		if !strings.Contains(err.Error(), "ordered") {
			t.Fatalf("timeline error = %v, want ordering diagnostic", err)
		}
	})

	t.Run("PCM format mismatch", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		manifest["pcm_format"].(map[string]any)["sample_rate_hz"] = 16000
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := LoadRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "pcm_format")
		if !strings.Contains(err.Error(), "rate=16000") || !strings.Contains(err.Error(), "24000") {
			t.Fatalf("format error = %v, want declared and actual rates", err)
		}
	})

	t.Run("duplicate stream identity", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		participants := manifest["participants"].(map[string]any)
		betaStreams := participants["beta"].(map[string]any)["streams"].(map[string]any)
		betaStreams["sent"].(map[string]any)["stream_id"] = "alpha:sent"
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := LoadRoomReplayAudioBundle(bundle)
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
				decoded, err := base64.StdEncoding.DecodeString(lines[0]["delta"].(string))
				if err != nil {
					panic(err)
				}
				decoded[0] ^= 0xff
				lines[0]["delta"] = base64.StdEncoding.EncodeToString(decoded)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle, manifest, _ := writeRoomReplayAudioBundle(t)
			path := filepath.Join(bundle, "participants", "alpha", "deltas.jsonl")
			lines := []map[string]any{
				{"type": "AUDIO.DELTA", "sequence": 0, "delta_id": "alpha-delta-0", "delta": base64.StdEncoding.EncodeToString(roomReplayAudioPCM16Bytes([]int16{1000, 2000}))},
				{"type": "AUDIO.DELTA", "sequence": 1, "delta_id": "alpha-delta-1", "delta": base64.StdEncoding.EncodeToString(roomReplayAudioPCM16Bytes([]int16{3000, 4000}))},
			}
			if test.name == "dropped delta" {
				lines = lines[1:]
			} else {
				test.mutate(lines)
			}
			data := jsonLines(t, lines)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("rewrite deltas: %v", err)
			}
			updateRoomReplayParticipantArtifact(t, manifest, "alpha", "deltas", data)
			writeRoomReplayAudioManifest(t, bundle, manifest)

			_, err := LoadRoomReplayAudioBundle(bundle)
			if err == nil || !errors.Is(err, ErrRoomReplayDeltaReconstruction) {
				t.Fatalf("%s error = %v, want typed reconstruction failure", test.name, err)
			}
			var reconstruction *RoomReplayDeltaReconstructionError
			if !errors.As(err, &reconstruction) {
				t.Fatalf("%s error = %v, want first-divergence details", test.name, err)
			}
			if reconstruction.ParticipantID != "alpha" || reconstruction.StreamID != "alpha:output" || reconstruction.DeltaID == "" {
				t.Fatalf("reconstruction = %+v, want stable participant/stream/delta identity", reconstruction)
			}
			if !strings.Contains(err.Error(), "first divergent byte") || !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "reconstructed") {
				t.Fatalf("reconstruction error = %v, want numeric discrepancy", err)
			}
		})
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

	_, err := LoadRoomReplayAudioBundle(bundle)
	assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "unique delta identity")
}

func TestLoadRoomReplayAudioBundleEnforcesAnnotationIdentityAndToleranceBounds(t *testing.T) {
	t.Run("unknown overlap participant", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		manifest["annotations"] = []any{map[string]any{"kind": "overlap", "id": "bad-overlap", "start_ms": 10, "end_ms": 20, "participants": []any{"alpha", "missing"}}}
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := LoadRoomReplayAudioBundle(bundle)
		assertRoomReplayAudioError(t, err, ErrInvalidRoomReplayBundle, "bad-overlap")
		if !strings.Contains(err.Error(), "missing") {
			t.Fatalf("annotation error = %v, want absent participant identity", err)
		}
	})

	t.Run("profile may tighten", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		manifest["tolerances"] = map[string]any{"name": "tight", "max_barge_in_latency": "250ms", "max_loudness_difference_db": 3}
		writeRoomReplayAudioManifest(t, bundle, manifest)
		got, err := LoadRoomReplayAudioBundle(bundle)
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
		_, err := LoadRoomReplayAudioBundle(bundle)
		if err == nil || !errors.Is(err, ErrRoomReplayToleranceProfile) || !strings.Contains(err.Error(), "max_barge_in_latency") {
			t.Fatalf("loosened profile error = %v, want explicit profile rejection", err)
		}
	})

	t.Run("stream samples cannot exceed room duration", func(t *testing.T) {
		bundle, manifest, _ := writeRoomReplayAudioBundle(t)
		clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		// Keep the declared stream interval inside the room while making the
		// four-sample payload (about 167us at 24kHz) exceed the room by 67us.
		manifest["timing"].(map[string]any)["ended_at"] = clock.Add(100 * time.Microsecond).Format(time.RFC3339Nano)
		participants := manifest["participants"].(map[string]any)
		for _, participantID := range []string{"alpha", "beta"} {
			streams := participants[participantID].(map[string]any)["streams"].(map[string]any)
			streams["wav"].(map[string]any)["timeline_end_ms"] = "100us"
		}
		timeline := []byte(fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"alpha"}`+"\n"+`{"sequence":1,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"beta"}`+"\n", clock.UnixMilli(), clock.UnixMilli()))
		if err := os.WriteFile(filepath.Join(bundle, "room-timeline.jsonl"), timeline, 0o600); err != nil {
			t.Fatalf("rewrite zero-duration timeline: %v", err)
		}
		updateRoomReplayArtifact(t, manifest, "room_timeline", timeline)
		writeRoomReplayAudioManifest(t, bundle, manifest)
		_, err := LoadRoomReplayAudioBundle(bundle)
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
	participants := manifest["participants"].(map[string]any)
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
	artifacts := manifest["artifacts"].(map[string]any)
	value := artifacts[role].(map[string]any)
	value["size"] = len(data)
	digest := sha256.Sum256(data)
	value["sha256"] = hex.EncodeToString(digest[:])
}

func updateRoomReplayParticipantArtifact(t *testing.T, manifest map[string]any, participantID, role string, data []byte) {
	t.Helper()
	participants := manifest["participants"].(map[string]any)
	participant := participants[participantID].(map[string]any)
	artifacts := participant["artifacts"].(map[string]any)
	value := artifacts[role].(map[string]any)
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
