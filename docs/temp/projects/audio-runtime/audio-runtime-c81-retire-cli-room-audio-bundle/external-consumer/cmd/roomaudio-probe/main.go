// Command roomaudio-probe exercises the public roomaudio service from a
// separate module. It deliberately constructs the admitted plan itself so
// the probe imports no CLI, internal package, room scheduler, or device code.
package main

import (
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
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio/wire"
)

const (
	probeSchema = "audio-runtime.c81.public-roomaudio-probe.v1"
	alpha       = "alpha"
	beta        = "beta"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "positive" && os.Args[1] != "corrupted") {
		fatal("usage: roomaudio-probe positive|corrupted")
	}
	root, err := os.MkdirTemp("", "audio-runtime-c81-roomaudio-")
	if err != nil {
		fatal("create probe fixture: %v", err)
	}
	defer os.RemoveAll(root)

	plan, err := writeFixture(root)
	if err != nil {
		fatal("write probe fixture: %v", err)
	}
	if os.Args[1] == "corrupted" {
		probeCorrupted(plan)
		return
	}

	bundle, err := wire.NewService().Load(plan)
	if err != nil {
		fatal("load public roomaudio bundle: %v", err)
	}
	if err := assertPositive(bundle); err != nil {
		fatal("public roomaudio oracle: %v", err)
	}
	encoded, err := json.Marshal(positiveReport(bundle))
	if err != nil {
		fatal("encode positive report: %v", err)
	}
	fmt.Println(string(encoded))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func writeFixture(root string) (roomaudio.RoomReplayPlan, error) {
	clock := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	values := map[string][]int16{
		alpha + ":wav":      {100, 200, 300, 400},
		alpha + ":sent":     {110, 210, 310, 410},
		alpha + ":received": {120, 220, 320, 420},
		beta + ":wav":       {500, 600, 700, 800},
		beta + ":sent":      {510, 610, 710, 810},
		beta + ":received":  {520, 620, 720, 820},
		"room:mix":          {900, 1000, 1100, 1200},
	}

	plan := roomaudio.RoomReplayPlan{
		BundlePath:    root,
		ManifestPath:  filepath.Join(root, roomaudio.RoomReplayBundleManifestPath),
		SchemaVersion: roomaudio.RoomReplayBundleSchemaVersion,
		Finalized:     true,
		ClockBase:     clock,
		StartedAt:     clock,
		EndedAt:       clock.Add(8 * time.Second),
		PCMFormat: roomaudio.RoomReplayPCMFormat{
			SampleRate: 1000, Channels: 1, SampleWidthBits: 16,
			ByteOrder: "little", Encoding: "signed_pcm16",
		},
		Timeline: []roomaudio.RoomReplayTimelineEvent{
			{Sequence: 0, OffsetMS: 0, UnixMS: clock.UnixMilli(), Type: "speech_start", ParticipantID: alpha},
			{Sequence: 1, OffsetMS: 1000, UnixMS: clock.Add(time.Second).UnixMilli(), Type: "speech_start", ParticipantID: beta},
		},
	}

	manifestParticipants := map[string]any{}
	for _, participantID := range []string{alpha, beta} {
		participant := roomaudio.RoomReplayParticipant{ID: participantID, Kind: roomaudio.ParticipantKind("agent")}
		artifactValues := map[string]any{}
		for _, item := range []struct {
			role string
			rel  string
			data []byte
		}{
			{roomaudio.RoomReplayAudioRoleWAV, "participants/" + participantID + "/agent.wav", wav16(1000, values[participantID+":wav"])},
			{roomaudio.RoomReplayAudioRoleDeltas, "participants/" + participantID + "/deltas.jsonl", deltaJSON(participantID, values[participantID+":wav"])},
			{roomaudio.RoomReplayAudioRoleSentPCM, "participants/" + participantID + "/sent.pcm", pcm16(values[participantID+":sent"])},
			{roomaudio.RoomReplayAudioRoleReceivedPCM, "participants/" + participantID + "/received.pcm", pcm16(values[participantID+":received"])},
			{roomaudio.RoomReplayAudioRoleEvents, "participants/" + participantID + "/events.jsonl", []byte("{\"event\":\"turn\"}\n")},
			{roomaudio.RoomReplayAudioRoleDiagnostics, "participants/" + participantID + "/diagnostics.jsonl", []byte("{\"event\":\"complete\"}\n")},
		} {
			artifact, err := writeArtifact(root, item.role, participantID+":"+item.role, item.rel, item.data)
			if err != nil {
				return roomaudio.RoomReplayPlan{}, err
			}
			participant.Artifacts = append(participant.Artifacts, artifact)
			artifactValues[item.role] = manifestArtifact(item.rel, item.data)
		}
		manifestParticipants[participantID] = map[string]any{
			"id":   participantID,
			"kind": "agent",
			"streams": map[string]any{
				"wav":      streamMetadata(participantID + ":output"),
				"sent":     streamMetadata(participantID + ":sent"),
				"received": streamMetadata(participantID + ":received"),
			},
			"artifacts": artifactValues,
		}
		plan.Participants = append(plan.Participants, participant)
	}

	roomMixData := wav16(1000, values["room:mix"])
	roomMix, err := writeArtifact(root, "room_mix", "room:mix", "room-mix.wav", roomMixData)
	if err != nil {
		return roomaudio.RoomReplayPlan{}, err
	}
	plan.Artifacts = []roomaudio.RoomReplayArtifact{roomMix}
	manifest := map[string]any{
		"schema_version": 2,
		"finalized":      true,
		"clock_base":     clock.Format(time.RFC3339Nano),
		"timing": map[string]any{
			"started_at": clock.Format(time.RFC3339Nano),
			"ended_at":   clock.Add(8 * time.Second).Format(time.RFC3339Nano),
		},
		"pcm_format": map[string]any{
			"sample_rate_hz": 1000, "channels": 1, "sample_width_bits": 16,
			"byte_order": "little", "encoding": "signed_pcm16",
		},
		"participants": manifestParticipants,
		"artifacts": map[string]any{
			"room_mix": manifestArtifact("room-mix.wav", roomMixData),
		},
		"annotations": []any{
			map[string]any{
				"kind": "overlap", "id": "alpha-beta-overlap", "start_ms": 1000, "end_ms": 3000,
				"a": alpha, "b": beta,
				"a_sent_stream_id": alpha + ":sent", "a_received_stream_id": alpha + ":received",
				"b_sent_stream_id": beta + ":sent", "b_received_stream_id": beta + ":received",
			},
			map[string]any{
				"kind": "barge_in", "id": "alpha-barge-beta", "start_ms": 2000, "end_ms": 2500,
				"interrupter": alpha, "interrupted": beta,
			},
			map[string]any{
				"kind": "loudness", "id": "alpha-beta-loudness", "start_ms": 1000, "end_ms": 3000,
				"left": alpha, "right": beta,
			},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return roomaudio.RoomReplayPlan{}, err
	}
	if err := os.WriteFile(plan.ManifestPath, append(data, '\n'), 0o600); err != nil {
		return roomaudio.RoomReplayPlan{}, err
	}
	return plan, nil
}

func streamMetadata(streamID string) map[string]any {
	return map[string]any{"stream_id": streamID, "timeline_start_ms": 0, "timeline_end_ms": 8000}
}

func writeArtifact(root, role, owner, relative string, data []byte) (roomaudio.RoomReplayArtifact, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return roomaudio.RoomReplayArtifact{}, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return roomaudio.RoomReplayArtifact{}, err
	}
	digest := sha256.Sum256(data)
	return roomaudio.RoomReplayArtifact{
		Name: role, Role: role, Owner: owner, Path: relative, AbsolutePath: path,
		Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func manifestArtifact(relative string, data []byte) map[string]any {
	digest := sha256.Sum256(data)
	return map[string]any{"path": relative, "size": len(data), "sha256": hex.EncodeToString(digest[:])}
}

func deltaJSON(participantID string, samples []int16) []byte {
	first := pcm16(samples[:len(samples)/2])
	second := pcm16(samples[len(samples)/2:])
	lines := []map[string]any{
		{"type": "AUDIO.DELTA", "sequence": 0, "delta_id": participantID + "-delta-0", "offset_ms": 0, "delta": base64.StdEncoding.EncodeToString(first)},
		{"type": "AUDIO.DELTA", "sequence": 1, "delta_id": participantID + "-delta-1", "offset_ms": 0, "delta": base64.StdEncoding.EncodeToString(second)},
	}
	var result []byte
	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			fatal("encode delta fixture: %v", err)
		}
		result = append(result, data...)
		result = append(result, '\n')
	}
	return result
}

func probeCorrupted(plan roomaudio.RoomReplayPlan) {
	for participantIndex := range plan.Participants {
		if plan.Participants[participantIndex].ID != alpha {
			continue
		}
		for artifactIndex := range plan.Participants[participantIndex].Artifacts {
			artifact := &plan.Participants[participantIndex].Artifacts[artifactIndex]
			if artifact.Role != roomaudio.RoomReplayAudioRoleDeltas {
				continue
			}
			data := deltaJSON(alpha, []int16{999, 200, 300, 400})
			if err := os.WriteFile(artifact.AbsolutePath, data, 0o600); err != nil {
				fatal("corrupt delta fixture: %v", err)
			}
			digest := sha256.Sum256(data)
			artifact.Size = int64(len(data))
			artifact.SHA256 = hex.EncodeToString(digest[:])
		}
	}
	bundle, err := wire.NewService().Load(plan)
	var reconstruction *roomaudio.RoomReplayDeltaReconstructionError
	if err == nil || !errors.Is(err, roomaudio.ErrRoomReplayDeltaReconstruction) || !errors.As(err, &reconstruction) || !strings.Contains(err.Error(), "first divergent byte") {
		fatal("corrupted bundle was not rejected with typed reconstruction detail: %v", err)
	}
	_ = bundle
	report := map[string]any{
		"schema":                probeSchema,
		"workflow":              "public roomaudio/wire",
		"rejected":              true,
		"typed_reconstruction":  true,
		"participant":           reconstruction.ParticipantID,
		"stream":                reconstruction.StreamID,
		"delta_id":              reconstruction.DeltaID,
		"first_divergent_byte":  reconstruction.ByteOffset,
		"expected_sample_count": reconstruction.ExpectedSampleCount,
		"actual_sample_count":   reconstruction.ActualSampleCount,
		"clean_shutdown":        true,
	}
	data, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		fatal("encode corrupted report: %v", marshalErr)
	}
	fmt.Println(string(data))
}

func assertPositive(bundle roomaudio.RoomReplayAudioBundle) error {
	if len(bundle.Participants) != 2 || bundle.Participants[0].ID != alpha || bundle.Participants[1].ID != beta {
		return fmt.Errorf("participants = %+v", bundle.Participants)
	}
	alphaParticipant, ok := bundle.Participant(alpha)
	if !ok || alphaParticipant.WAV.StreamID != alpha+":output" || alphaParticipant.Sent.StreamID != alpha+":sent" || alphaParticipant.Received.StreamID != alpha+":received" {
		return fmt.Errorf("alpha stream identities are not stable")
	}
	if got, want := alphaParticipant.WAV.Samples, []int16{100, 200, 300, 400}; !equalInt16(got, want) {
		return fmt.Errorf("alpha WAV samples = %v, want %v", got, want)
	}
	if got, want := bundle.RoomMix.Samples, []int16{900, 1000, 1100, 1200}; !equalInt16(got, want) {
		return fmt.Errorf("room mix samples = %v, want %v", got, want)
	}
	if len(alphaParticipant.WAV.Deltas) != 2 || alphaParticipant.WAV.Deltas[0].ID != "alpha-delta-0" || alphaParticipant.WAV.Deltas[1].ID != "alpha-delta-1" {
		return fmt.Errorf("delta order = %+v", alphaParticipant.WAV.Deltas)
	}
	if len(bundle.Plan.Timeline) != 2 || bundle.Plan.Timeline[0].OffsetMS != 0 || bundle.Plan.Timeline[1].ParticipantID != beta {
		return fmt.Errorf("timeline = %+v", bundle.Plan.Timeline)
	}
	if len(bundle.Overlaps) != 1 || len(bundle.BargeIns) != 1 || len(bundle.Loudness) != 1 {
		return fmt.Errorf("annotation counts overlap=%d barge=%d loudness=%d", len(bundle.Overlaps), len(bundle.BargeIns), len(bundle.Loudness))
	}
	view, ok := bundle.Participant(alpha)
	if !ok {
		return fmt.Errorf("alpha clone is missing")
	}
	view.WAV.PCM[0] ^= 0xff
	view.WAV.Samples[0] = -1
	analysis := bundle.AnalysisInput()
	analysis.Streams[0].Samples[0] = -2
	if bundle.Participants[0].WAV.PCM[0] != pcm16([]int16{100, 200, 300, 400})[0] || bundle.Participants[0].WAV.Samples[0] != 100 {
		return fmt.Errorf("returned audio views share mutable state")
	}
	if len(analysis.Streams) != 7 || len(analysis.Overlaps) != 1 || len(analysis.BargeIns) != 1 || len(analysis.Loudness) != 1 {
		return fmt.Errorf("analysis input lost streams or annotations")
	}
	return nil
}

func positiveReport(bundle roomaudio.RoomReplayAudioBundle) map[string]any {
	participants := make([]string, 0, len(bundle.Participants))
	streamOrder := make([]string, 0, len(bundle.Participants)*3+1)
	sampleCounts := map[string]int{}
	deltaIDs := map[string][]string{}
	for _, participant := range bundle.Participants {
		participants = append(participants, participant.ID)
		for _, stream := range []roomaudio.RoomReplayAudioStream{participant.WAV, participant.Sent, participant.Received} {
			streamOrder = append(streamOrder, stream.StreamID)
			sampleCounts[stream.StreamID] = stream.SampleCount
			for _, delta := range stream.Deltas {
				deltaIDs[stream.StreamID] = append(deltaIDs[stream.StreamID], delta.ID)
			}
		}
	}
	streamOrder = append(streamOrder, bundle.RoomMix.StreamID)
	sampleCounts[bundle.RoomMix.StreamID] = bundle.RoomMix.SampleCount
	annotationIDs := make([]string, 0, len(bundle.Annotations))
	annotationKinds := make([]string, 0, len(bundle.Annotations))
	for _, annotation := range bundle.Annotations {
		annotationIDs = append(annotationIDs, annotation.ID)
		annotationKinds = append(annotationKinds, annotation.Kind)
	}
	return map[string]any{
		"schema":              probeSchema,
		"workflow":            "public roomaudio/wire",
		"participants":        participants,
		"stream_order":        streamOrder,
		"sample_counts":       sampleCounts,
		"alpha_wav_samples":   bundle.Participants[0].WAV.Samples,
		"room_mix_samples":    bundle.RoomMix.Samples,
		"alpha_delta_ids":     deltaIDs[alpha+":output"],
		"timeline_offsets_ms": []int64{bundle.Plan.Timeline[0].OffsetMS, bundle.Plan.Timeline[1].OffsetMS},
		"annotation_ids":      annotationIDs,
		"annotation_kinds":    annotationKinds,
		"overlap_count":       len(bundle.Overlaps),
		"barge_in_count":      len(bundle.BargeIns),
		"loudness_count":      len(bundle.Loudness),
		"immutable_views":     true,
		"clean_shutdown":      true,
	}
}

func equalInt16(left, right []int16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
