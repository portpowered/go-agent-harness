package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	sampleRate = 1000
	duration   = 4 * sampleRate

	participantA = "agent-a"
	participantB = "agent-b"
)

type turn struct {
	ID    string
	Start int
	End   int
}

type artifactRef struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type deltaRecord struct {
	Type          string `json:"type"`
	Sequence      int    `json:"sequence"`
	DeltaID       string `json:"delta_id"`
	OffsetMS      int    `json:"offset_ms"`
	StreamID      string `json:"stream_id"`
	ParticipantID string `json:"participant_id"`
	TurnID        string `json:"turn_id,omitempty"`
	Delta         string `json:"delta"`
}

func main() {
	output := flag.String("output", "agent-cli/internal/services/testdata/room-audio/clean-turn-taking", "fixture directory to refresh")
	flag.Parse()
	if err := generate(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(output string) error {
	if output == "" {
		return fmt.Errorf("fixture output directory is empty")
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create fixture directory: %w", err)
	}

	aTurns := []turn{{ID: "agent-a-turn-1", Start: 200, End: 1000}, {ID: "agent-a-turn-2", Start: 2200, End: 3000}}
	bTurns := []turn{{ID: "agent-b-turn-1", Start: 1200, End: 2000}, {ID: "agent-b-turn-2", Start: 3200, End: 3800}}
	aSent := syntheticSpeech(duration, aTurns, 11)
	bSent := syntheticSpeech(duration, bTurns, 29)
	aReceived := delayedCopy(bSent, 20)
	bReceived := delayedCopy(aSent, 20)
	roomMix := mix(aSent, bSent)

	files := make(map[string][]byte)
	participantRefs := make(map[string]any)
	for _, fixture := range []struct {
		id            string
		turns         []turn
		sent          []int16
		received      []int16
		receivedTurns []turn
	}{
		{id: participantA, turns: aTurns, sent: aSent, received: aReceived, receivedTurns: shiftTurns(bTurns, 20)},
		{id: participantB, turns: bTurns, sent: bSent, received: bReceived, receivedTurns: shiftTurns(aTurns, 20)},
	} {
		directory := filepath.ToSlash(filepath.Join("participants", fixture.id))
		wavPath := filepath.ToSlash(filepath.Join(directory, "agent.wav"))
		sentPath := filepath.ToSlash(filepath.Join(directory, "sent.pcm"))
		receivedPath := filepath.ToSlash(filepath.Join(directory, "received.pcm"))
		deltasPath := filepath.ToSlash(filepath.Join(directory, "deltas.jsonl"))
		eventsPath := filepath.ToSlash(filepath.Join(directory, "events.jsonl"))
		diagnosticsPath := filepath.ToSlash(filepath.Join(directory, "diagnostics.jsonl"))
		capturePath := filepath.ToSlash(filepath.Join(directory, "capture.session.json"))

		files[wavPath] = wavBytes(fixture.sent)
		files[sentPath] = pcmBytes(fixture.sent)
		files[receivedPath] = pcmBytes(fixture.received)
		files[deltasPath] = deltaJSONL(fixture.id, fixture.turns, fixture.sent)
		files[eventsPath] = eventJSONL(fixture.id, fixture.turns)
		files[diagnosticsPath] = diagnosticJSONL(fixture.id, fixture.turns)
		files[capturePath] = captureJSON(fixture.id)

		participantRefs[fixture.id] = map[string]any{
			"id":              fixture.id,
			"kind":            "agent",
			"provider":        "offline",
			"model":           "clean-turn-taking-v1",
			"voice":           "synthetic-noise",
			"opening_prompt":  "Begin the deterministic clean turn-taking fixture.",
			"system_prompt":   "Use the privacy-safe synthetic fixture transcript; do not contact a provider.",
			"completed_turns": len(fixture.turns),
			"capture":         artifactRefFor(capturePath, files[capturePath]),
			"artifacts": map[string]artifactRef{
				"wav":          artifactRefFor(wavPath, files[wavPath]),
				"sent_pcm":     artifactRefFor(sentPath, files[sentPath]),
				"received_pcm": artifactRefFor(receivedPath, files[receivedPath]),
				"deltas":       artifactRefFor(deltasPath, files[deltasPath]),
				"events":       artifactRefFor(eventsPath, files[eventsPath]),
				"diagnostics":  artifactRefFor(diagnosticsPath, files[diagnosticsPath]),
			},
			"streams": map[string]any{
				"wav": map[string]any{
					"stream_id":         fixture.id + ":output",
					"timeline_start_ms": 0,
					"timeline_end_ms":   4000,
					"expected_speech":   speechAnnotations(fixture.turns),
					"chunk_boundaries":  chunkBoundaries(fixture.id),
				},
				"sent": map[string]any{
					"stream_id":         fixture.id + ":sent",
					"timeline_start_ms": 0,
					"timeline_end_ms":   4000,
					"expected_speech":   speechAnnotations(fixture.turns),
				},
				"received": map[string]any{
					"stream_id":         fixture.id + ":received",
					"timeline_start_ms": 0,
					"timeline_end_ms":   4000,
					"expected_speech":   speechAnnotations(fixture.receivedTurns),
				},
			},
		}
	}

	clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	timelinePath := "room-timeline.jsonl"
	roomMixPath := "room-mix.wav"
	files[timelinePath] = timelineJSONL(clock, append(append([]turn(nil), aTurns...), bTurns...))
	files[roomMixPath] = wavBytes(roomMix)

	manifest := map[string]any{
		"schema_version": 2,
		"finalized":      true,
		"clock_base":     clock.Format(time.RFC3339Nano),
		"timing": map[string]any{
			"started_at": clock.Format(time.RFC3339Nano),
			"ended_at":   clock.Add(4 * time.Second).Format(time.RFC3339Nano),
			"elapsed":    "4s",
		},
		"pcm_format": map[string]any{
			"sample_rate_hz":    sampleRate,
			"channels":          1,
			"sample_width_bits": 16,
			"byte_order":        "little",
			"encoding":          "signed_pcm16",
		},
		"participants": participantRefs,
		"artifacts": map[string]artifactRef{
			"room_timeline": artifactRefFor(timelinePath, files[timelinePath]),
			"room_mix":      artifactRefFor(roomMixPath, files[roomMixPath]),
		},
		"annotations": map[string]any{
			"loudness": []any{
				map[string]any{
					"kind":     "loudness",
					"id":       "clean-turn-balance",
					"start_ms": 200,
					"end_ms":   3800,
					"left":     participantA,
					"right":    participantB,
				},
			},
		},
		"provenance": map[string]any{
			"fixture_provenance": "synthetic",
			"source_run":         "offline deterministic generator; no provider, microphone, credentials, or private recording",
			"consent_review":     "not applicable; all samples are generated from a fixed seeded pseudo-speech signal",
			"sanitization":       "no real speech entered the fixture; participant identities and prompts are synthetic",
			"transformations": []string{
				"generated mono signed little-endian PCM16 at 1000 Hz",
				"kept four seconds with two non-empty turns per participant",
				"delayed peer received streams by 20 ms",
				"split each output stream into four timestamped delta chunks",
			},
			"regeneration_command": "go run ./agent-cli/internal/services/testdata/room-audio/generate.go --output agent-cli/internal/services/testdata/room-audio/clean-turn-taking",
			"hashes":               "sha256 and byte sizes for every replay artifact are embedded in this manifest",
		},
		"tolerances": map[string]any{
			"name":                           "clean-turn-taking-v1",
			"frame_duration":                 "20ms",
			"silence_floor_dbfs":             -50,
			"max_natural_pause":              "750ms",
			"boundary_delta":                 6000,
			"boundary_quiet_dbfs":            -24,
			"clip_sample_threshold":          32700,
			"edge_sample_threshold":          1000,
			"final_frame_max_rms_dbfs":       -40,
			"correlation_lag_window":         map[string]any{"min": "-100ms", "max": "100ms"},
			"correlation_silence_floor_dbfs": -50,
			"min_peer_correlation":           0.55,
			"max_self_correlation":           0.30,
			"barge_in_speech_threshold_dbfs": -40,
			"max_barge_in_latency":           "500ms",
			"max_loudness_difference_db":     6,
			"max_drift_absolute":             "20ms",
			"max_drift_fraction":             0.001,
		},
	}

	for relativePath, data := range files {
		path := filepath.Join(output, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", relativePath, err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", relativePath, err)
		}
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	manifestPath := filepath.Join(output, "run-manifest.json")
	if err := os.WriteFile(manifestPath, append(manifestData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func syntheticSpeech(sampleCount int, turns []turn, seed uint32) []int16 {
	samples := make([]int16, sampleCount)
	for _, currentTurn := range turns {
		state := seed + uint32(currentTurn.Start)
		for index := currentTurn.Start; index < currentTurn.End; index++ {
			state = state*1664525 + 1013904223
			value := int32((state>>8)%16001) - 8000
			if distance := index - currentTurn.Start; distance < 20 {
				value = value * int32(distance+1) / 20
			}
			if distance := currentTurn.End - index; distance <= 20 {
				value = value * int32(distance) / 20
			}
			samples[index] = int16(value)
		}
		seed += 7919
	}
	return samples
}

func delayedCopy(source []int16, delay int) []int16 {
	result := make([]int16, len(source))
	copy(result[delay:], source[:len(source)-delay])
	return result
}

func shiftTurns(turns []turn, samples int) []turn {
	result := make([]turn, len(turns))
	for index, currentTurn := range turns {
		result[index] = turn{ID: currentTurn.ID, Start: currentTurn.Start + samples, End: currentTurn.End + samples}
	}
	return result
}

func mix(streams ...[]int16) []int16 {
	result := make([]int16, duration)
	for index := range result {
		var value int32
		for _, stream := range streams {
			value += int32(stream[index])
		}
		if value > 32767 {
			value = 32767
		}
		if value < -32768 {
			value = -32768
		}
		result[index] = int16(value)
	}
	return result
}

func pcmBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*2:], uint16(sample))
	}
	return data
}

func wavBytes(samples []int16) []byte {
	pcm := pcmBytes(samples)
	data := make([]byte, 44+len(pcm))
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(36+len(pcm)))
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 1)
	binary.LittleEndian.PutUint32(data[24:28], sampleRate)
	binary.LittleEndian.PutUint32(data[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], uint32(len(pcm)))
	copy(data[44:], pcm)
	return data
}

func deltaJSONL(id string, turns []turn, samples []int16) []byte {
	boundaries := []int{0, 1200, 2200, 3200, len(samples)}
	var result bytes.Buffer
	for index := 0; index < len(boundaries)-1; index++ {
		start, end := boundaries[index], boundaries[index+1]
		record := deltaRecord{
			Type:          "AUDIO.DELTA",
			Sequence:      index,
			DeltaID:       fmt.Sprintf("%s-delta-%d", id, index),
			OffsetMS:      start,
			StreamID:      id + ":output",
			ParticipantID: id,
			TurnID:        turnAt(turns, start),
			Delta:         base64.StdEncoding.EncodeToString(pcmBytes(samples[start:end])),
		}
		data, err := json.Marshal(record)
		if err != nil {
			panic(err)
		}
		result.Write(data)
		result.WriteByte('\n')
	}
	return result.Bytes()
}

func eventJSONL(id string, turns []turn) []byte {
	values := make([]map[string]any, 0, len(turns)*2)
	for _, currentTurn := range turns {
		values = append(values,
			map[string]any{"event": "turn_started", "participant_id": id, "turn_id": currentTurn.ID, "timestamp_ms": currentTurn.Start},
			map[string]any{"event": "turn_ended", "participant_id": id, "turn_id": currentTurn.ID, "timestamp_ms": currentTurn.End},
		)
	}
	return jsonLines(values)
}

func diagnosticJSONL(id string, turns []turn) []byte {
	values := make([]map[string]any, 0, len(turns))
	for _, currentTurn := range turns {
		values = append(values, map[string]any{
			"event":          "turn",
			"participant_id": id,
			"turn_id":        currentTurn.ID,
			"start_ms":       currentTurn.Start,
			"end_ms":         currentTurn.End,
			"status":         "complete",
		})
	}
	return jsonLines(values)
}

func timelineJSONL(clock time.Time, turns []turn) []byte {
	ordered := append([]turn(nil), turns...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].Start < ordered[j-1].Start; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	values := make([]map[string]any, 0, len(ordered)*2)
	sequence := 0
	for _, currentTurn := range ordered {
		participantID := participantA
		if len(currentTurn.ID) > len(participantA) && currentTurn.ID[:len(participantA)] == participantA {
			participantID = participantA
		} else {
			participantID = participantB
		}
		for _, event := range []struct {
			typeName string
			offset   int
		}{
			{typeName: "turn_started", offset: currentTurn.Start},
			{typeName: "turn_ended", offset: currentTurn.End},
		} {
			values = append(values, map[string]any{
				"sequence":            sequence,
				"monotonic_offset_ms": event.offset,
				"unix_ms":             clock.UnixMilli() + int64(event.offset),
				"type":                event.typeName,
				"participant_id":      participantID,
				"turn_id":             currentTurn.ID,
			})
			sequence++
		}
	}
	return jsonLines(values)
}

func speechAnnotations(turns []turn) []map[string]any {
	result := make([]map[string]any, 0, len(turns))
	for _, currentTurn := range turns {
		result = append(result, map[string]any{"label": currentTurn.ID, "start_ms": currentTurn.Start, "end_ms": currentTurn.End})
	}
	return result
}

func chunkBoundaries(id string) []map[string]any {
	return []map[string]any{
		{"id": id + "-chunk-1", "sample_index": 1200},
		{"id": id + "-chunk-2", "sample_index": 2200},
		{"id": id + "-chunk-3", "sample_index": 3200},
	}
}

func turnAt(turns []turn, sample int) string {
	for _, currentTurn := range turns {
		if sample >= currentTurn.Start && sample < currentTurn.End {
			return currentTurn.ID
		}
	}
	return ""
}

func captureJSON(id string) []byte {
	payload, err := json.Marshal(map[string]any{"type": "session.created", "session": map[string]any{"id": id + "-offline-session"}})
	if err != nil {
		panic(err)
	}
	capture := map[string]any{
		"version":  1,
		"provider": map[string]any{"name": "offline", "model": "clean-turn-taking-v1"},
		"session":  map[string]any{"id": id + "-offline-session", "started_at_utc": "2026-08-30T12:00:00Z", "fixture_provenance": "synthetic"},
		"records": []map[string]any{{
			"sequence":     1,
			"direction":    "server_to_client",
			"timestamp_ms": 0,
			"type":         "session.created",
			"payload_type": "websocket_message",
			"payload":      json.RawMessage(payload),
		}},
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

func artifactRefFor(path string, data []byte) artifactRef {
	digest := sha256.Sum256(data)
	return artifactRef{Path: path, Size: len(data), SHA256: hex.EncodeToString(digest[:])}
}

func jsonLines(values []map[string]any) []byte {
	var result bytes.Buffer
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		result.Write(data)
		result.WriteByte('\n')
	}
	return result.Bytes()
}
