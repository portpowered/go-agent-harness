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
	"sort"
	"time"
)

const (
	sampleRate                          = 1000
	duration                            = 4 * sampleRate
	overlapDuration                     = 8 * sampleRate
	longConversationDuration            = 24 * sampleRate
	longConversationRouteDelay          = 20
	longConversationBoundOffset         = 23200
	longConversationTurnsPerParticipant = 4

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
	shape := flag.String("shape", "clean-turn-taking", "fixture shape to generate")
	flag.Parse()
	var err error
	switch *shape {
	case "clean-turn-taking":
		err = generate(*output)
	case "deliberate-overlap":
		err = generateDeliberateOverlap(*output)
	case "long-conversation-termination":
		err = generateLongConversationTermination(*output)
	default:
		err = fmt.Errorf("unknown room audio fixture shape %q", *shape)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// generateLongConversationTermination writes the committed post-fix
// termination golden. It is intentionally synthetic and deterministic: the
// eight ordered turns model the multi-second cadence of the production
// regression while the manifest and sidecars preserve the room-owned
// max-turn terminal projection that the runtime must produce.
func generateLongConversationTermination(output string) error {
	if output == "" {
		return fmt.Errorf("fixture output directory is empty")
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create fixture directory: %w", err)
	}

	aTurns := []turn{
		{ID: "agent-a-turn-1", Start: 200, End: 2200},
		{ID: "agent-a-turn-2", Start: 6200, End: 8200},
		{ID: "agent-a-turn-3", Start: 12200, End: 14200},
		{ID: "agent-a-turn-4", Start: 18200, End: 20200},
	}
	bTurns := []turn{
		{ID: "agent-b-turn-1", Start: 3200, End: 5200},
		{ID: "agent-b-turn-2", Start: 9200, End: 11200},
		{ID: "agent-b-turn-3", Start: 15200, End: 17200},
		{ID: "agent-b-turn-4", Start: 21200, End: 23200},
	}
	aSent := syntheticSpeech(longConversationDuration, aTurns, 311)
	bSent := syntheticSpeech(longConversationDuration, bTurns, 719)
	aReceived := delayedCopy(bSent, longConversationRouteDelay)
	bReceived := delayedCopy(aSent, longConversationRouteDelay)
	deltaBoundaries := fixedDeltaBoundaries(longConversationDuration, sampleRate)
	terminalFields := longConversationTerminalFields()
	clock := time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC)

	files := make(map[string][]byte)
	participantRefs := make(map[string]any, 2)
	for _, fixture := range []struct {
		id            string
		turns         []turn
		sent          []int16
		received      []int16
		receivedTurns []turn
	}{
		{id: participantA, turns: aTurns, sent: aSent, received: aReceived, receivedTurns: shiftTurns(bTurns, longConversationRouteDelay)},
		{id: participantB, turns: bTurns, sent: bSent, received: bReceived, receivedTurns: shiftTurns(aTurns, longConversationRouteDelay)},
	} {
		directory := filepath.ToSlash(filepath.Join("participants", fixture.id))
		wavPath := filepath.ToSlash(filepath.Join(directory, "agent.wav"))
		sentPath := filepath.ToSlash(filepath.Join(directory, "sent.pcm"))
		receivedPath := filepath.ToSlash(filepath.Join(directory, "received.pcm"))
		deltasPath := filepath.ToSlash(filepath.Join(directory, "deltas.jsonl"))
		eventsPath := filepath.ToSlash(filepath.Join(directory, "events.jsonl"))
		diagnosticsPath := filepath.ToSlash(filepath.Join(directory, "diagnostics.jsonl"))
		capturePath := filepath.ToSlash(filepath.Join(directory, "capture.session.json"))
		model := "room-bound-long-conversation-v1"

		files[wavPath] = wavBytes(fixture.sent)
		files[sentPath] = pcmBytes(fixture.sent)
		files[receivedPath] = pcmBytes(fixture.received)
		files[deltasPath] = deltaJSONLWithBoundaries(fixture.id, fixture.turns, fixture.sent, deltaBoundaries)
		files[eventsPath] = longConversationEventJSONL(fixture.id, fixture.turns, terminalFields)
		files[diagnosticsPath] = longConversationDiagnosticJSONL(fixture.id, fixture.turns, terminalFields, clock)
		files[capturePath] = captureJSONForModel(fixture.id, model)

		participantRefs[fixture.id] = map[string]any{
			"id":                      fixture.id,
			"kind":                    "agent",
			"provider":                "offline",
			"model":                   model,
			"voice":                   "synthetic-noise",
			"opening_prompt":          "Begin the deterministic post-fix long conversation fixture.",
			"system_prompt":           "Use the privacy-safe synthetic long-conversation fixture; do not contact a provider.",
			"completed_turns":         longConversationTurnsPerParticipant,
			"termination_reason":      "ended",
			"reason":                  "ended",
			"termination_trigger":     terminalFields["termination_trigger"],
			"termination_disposition": terminalFields["termination_disposition"],
			"classification":          terminalFields["classification"],
			"terminal_reason":         terminalFields["terminal_reason"],
			"terminal_provenance":     terminalFields["terminal_provenance"],
			"output_state":            terminalFields["output_state"],
			"connected":               true,
			"capture":                 artifactRefFor(capturePath, files[capturePath]),
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
					"timeline_end_ms":   longConversationDuration,
					"expected_speech":   speechAnnotations(fixture.turns),
					"chunk_boundaries":  chunkBoundariesFor(fixture.id, deltaBoundaries),
				},
				"sent": map[string]any{
					"stream_id":         fixture.id + ":sent",
					"timeline_start_ms": 0,
					"timeline_end_ms":   longConversationDuration,
					"expected_speech":   speechAnnotations(fixture.turns),
				},
				"received": map[string]any{
					"stream_id":         fixture.id + ":received",
					"timeline_start_ms": 0,
					"timeline_end_ms":   longConversationDuration,
					"expected_speech":   speechAnnotations(fixture.receivedTurns),
				},
			},
		}
	}

	allTurns := append(append([]turn(nil), aTurns...), bTurns...)
	timelinePath := "room-timeline.jsonl"
	roomMixPath := "room-mix.wav"
	files[timelinePath] = longConversationTimelineJSONL(clock, allTurns, terminalFields)
	files[roomMixPath] = wavBytes(mix(aSent, bSent))

	manifest := map[string]any{
		"schema_version":     2,
		"fixture_id":         "long-conversation-termination-v1",
		"finalized":          true,
		"clock_base":         clock.Format(time.RFC3339Nano),
		"termination_reason": "max_turns_reached",
		"reason":             "max_turns_reached",
		"bounds":             map[string]any{"max_turns": longConversationTurnsPerParticipant},
		"turn_counts":        map[string]int{participantA: longConversationTurnsPerParticipant, participantB: longConversationTurnsPerParticipant},
		"room_mix":           roomMixPath,
		"room_timeline":      timelinePath,
		"audio_format":       map[string]any{"sample_rate": sampleRate, "channels": 1, "encoding": "pcm_s16le"},
		"timing": map[string]any{
			"started_at": clock.Format(time.RFC3339Nano),
			"ended_at":   clock.Add(24 * time.Second).Format(time.RFC3339Nano),
			"elapsed":    "24s",
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
			"loudness": []any{map[string]any{
				"kind":     "loudness",
				"id":       "long-conversation-balance",
				"start_ms": 200,
				"end_ms":   longConversationBoundOffset,
				"left":     participantA,
				"right":    participantB,
			}},
		},
		"provenance": map[string]any{
			"fixture_provenance": "synthetic_post_fix_termination",
			"source_run":         "offline deterministic scripted-provider replay; no provider, microphone, credentials, or private recording",
			"consent_review":     "not applicable; all samples are generated from a fixed seeded pseudo-speech signal",
			"sanitization":       "no real speech entered the fixture; participant identities and prompts are synthetic",
			"transformations": []string{
				"generated mono signed little-endian PCM16 at 1000 Hz",
				"kept 24 seconds with eight ordered turns, four per participant",
				"preserved one-second delta boundaries across each multi-second scripted turn",
				"recorded max_turns=4 after the final provider-authored response",
				"retained room-owned terminal fields in participant sidecars and timeline",
			},
			"regeneration_command": "go run ./agent-cli/internal/services/testdata/room-audio/generate.go --shape long-conversation-termination --output agent-cli/internal/services/testdata/room-audio/long-conversation-termination",
			"hashes":               "sha256 and byte sizes for every replay artifact are embedded in this manifest",
		},
		"tolerances": map[string]any{
			"name":                           "long-conversation-termination-v1",
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

func longConversationTerminalFields() map[string]string {
	return map[string]string{
		"termination_trigger":     "max_turns_reached",
		"termination_disposition": "completed",
		"classification":          "",
		"terminal_reason":         "provider_authored_completion",
		"terminal_provenance":     "provider",
		"output_state":            "complete",
		"reason":                  "ended",
	}
}

func longConversationEventJSONL(id string, turns []turn, terminalFields map[string]string) []byte {
	values := make([]map[string]any, 0, len(turns)*2+1)
	for _, currentTurn := range turns {
		values = append(values,
			map[string]any{"event": "turn_started", "participant_id": id, "turn_id": currentTurn.ID, "timestamp_ms": currentTurn.Start},
			map[string]any{"event": "turn_ended", "participant_id": id, "turn_id": currentTurn.ID, "timestamp_ms": currentTurn.End},
		)
	}
	values = append(values, map[string]any{
		"event":          "participant_terminated",
		"participant_id": id,
		"timestamp_ms":   longConversationBoundOffset,
		"fields":         terminalFields,
	})
	return jsonLines(values)
}

func longConversationDiagnosticJSONL(id string, turns []turn, terminalFields map[string]string, clock time.Time) []byte {
	values := make([]map[string]any, 0, len(turns)+1)
	for _, currentTurn := range turns {
		values = append(values, map[string]any{
			"event":          "turn",
			"participant_id": id,
			"turn_id":        currentTurn.ID,
			"start_ms":       currentTurn.Start,
			"end_ms":         currentTurn.End,
			"status":         "complete",
			"t_offset_ms":    currentTurn.End,
			"t_unix_ms":      clock.UnixMilli() + int64(currentTurn.End),
		})
	}
	values = append(values, map[string]any{
		"event":       "room_bound_shutdown",
		"fields":      terminalFields,
		"t_offset_ms": longConversationBoundOffset,
		"t_unix_ms":   clock.UnixMilli() + int64(longConversationBoundOffset),
	})
	return jsonLines(values)
}

func longConversationTimelineJSONL(clock time.Time, turns []turn, terminalFields map[string]string) []byte {
	type timelineEntry struct {
		offset        int
		participantID string
		event         string
		turnID        string
		fields        map[string]string
	}
	entries := make([]timelineEntry, 0, len(turns)*3+5)
	for _, currentTurn := range turns {
		participantID := participantB
		if len(currentTurn.ID) >= len(participantA) && currentTurn.ID[:len(participantA)] == participantA {
			participantID = participantA
		}
		entries = append(entries,
			timelineEntry{offset: currentTurn.Start, participantID: participantID, event: "turn_started", turnID: currentTurn.ID},
			timelineEntry{offset: currentTurn.End, participantID: participantID, event: "turn_ended", turnID: currentTurn.ID},
			timelineEntry{offset: currentTurn.End, participantID: participantID, event: "turn_completed", turnID: currentTurn.ID},
		)
	}
	for _, participantID := range []string{participantA, participantB} {
		entries = append(entries,
			timelineEntry{offset: longConversationBoundOffset, participantID: participantID, event: "room_bound_shutdown", fields: terminalFields},
			timelineEntry{offset: longConversationBoundOffset, participantID: participantID, event: "participant_terminated", fields: terminalFields},
		)
	}
	entries = append(entries, timelineEntry{offset: longConversationDuration, event: "run_terminated", fields: map[string]string{"reason": "max_turns_reached"}})
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].offset < entries[right].offset })

	values := make([]map[string]any, 0, len(entries))
	for sequence, entry := range entries {
		value := map[string]any{
			"sequence":            sequence,
			"monotonic_offset_ms": entry.offset,
			"unix_ms":             clock.UnixMilli() + int64(entry.offset),
			"type":                entry.event,
		}
		if entry.participantID != "" {
			value["participant_id"] = entry.participantID
		}
		if entry.turnID != "" {
			value["turn_id"] = entry.turnID
		}
		if len(entry.fields) > 0 {
			value["fields"] = entry.fields
		}
		values = append(values, value)
	}
	return jsonLines(values)
}

func fixedDeltaBoundaries(sampleCount, step int) []int {
	if sampleCount <= 0 || step <= 0 {
		return []int{0, sampleCount}
	}
	boundaries := []int{0}
	for boundary := step; boundary < sampleCount; boundary += step {
		boundaries = append(boundaries, boundary)
	}
	return append(boundaries, sampleCount)
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

func generateDeliberateOverlap(output string) error {
	if output == "" {
		return fmt.Errorf("fixture output directory is empty")
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create fixture directory: %w", err)
	}

	const routeDelay = 20
	aTurns := []turn{{ID: "agent-a-overlap-turn", Start: 1000, End: 6500}}
	bTurns := []turn{{ID: "agent-b-overlap-turn", Start: 1000, End: 6500}}
	aSent := syntheticSpeech(overlapDuration, aTurns, 101)
	bSent := syntheticSpeech(overlapDuration, bTurns, 202)
	// Keep these large jumps inside loud neighboring windows so the boundary
	// analyzer reports them as observable speech impulses, not clicks.
	setLoudBoundaryImpulse(aSent, 2200)
	setLoudBoundaryImpulse(bSent, 3200)
	aReceived := delayedCopy(bSent, routeDelay)
	bReceived := delayedCopy(aSent, routeDelay)

	files := make(map[string][]byte)
	participantRefs := make(map[string]any)
	for _, fixture := range []struct {
		id            string
		turns         []turn
		sent          []int16
		received      []int16
		receivedTurns []turn
	}{
		{id: participantA, turns: aTurns, sent: aSent, received: aReceived, receivedTurns: shiftTurns(bTurns, routeDelay)},
		{id: participantB, turns: bTurns, sent: bSent, received: bReceived, receivedTurns: shiftTurns(aTurns, routeDelay)},
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
		files[capturePath] = captureJSONForModel(fixture.id, "deliberate-overlap-v1")

		participantRefs[fixture.id] = map[string]any{
			"id":                      fixture.id,
			"kind":                    "agent",
			"provider":                "offline",
			"model":                   "deliberate-overlap-v1",
			"voice":                   "synthetic-noise",
			"opening_prompt":          "Begin the deterministic deliberate overlap fixture.",
			"system_prompt":           "Use the privacy-safe synthetic overlap fixture; do not contact a provider.",
			"completed_turns":         len(fixture.turns),
			"received_audio_contract": "provider-bound room delivery in participants/<id>/received.pcm",
			"capture":                 artifactRefFor(capturePath, files[capturePath]),
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
					"timeline_end_ms":   8000,
					"expected_speech":   speechAnnotations(fixture.turns),
					"chunk_boundaries":  chunkBoundaries(fixture.id),
				},
				"sent": map[string]any{
					"stream_id":         fixture.id + ":sent",
					"timeline_start_ms": 0,
					"timeline_end_ms":   8000,
					"expected_speech":   speechAnnotations(fixture.turns),
				},
				"received": map[string]any{
					"stream_id":         fixture.id + ":received",
					"timeline_start_ms": 0,
					"timeline_end_ms":   8000,
					"expected_speech":   speechAnnotations(fixture.receivedTurns),
				},
			},
		}
	}

	clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	timelinePath := "room-timeline.jsonl"
	roomMixPath := "room-mix.wav"
	files[timelinePath] = timelineJSONL(clock, append(append([]turn(nil), aTurns...), bTurns...))
	files[roomMixPath] = wavBytes(mix(aSent, bSent))

	manifest := map[string]any{
		"schema_version": 2,
		"finalized":      true,
		"clock_base":     clock.Format(time.RFC3339Nano),
		"timing": map[string]any{
			"started_at": clock.Format(time.RFC3339Nano),
			"ended_at":   clock.Add(8 * time.Second).Format(time.RFC3339Nano),
			"elapsed":    "8s",
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
			"overlaps": []any{
				map[string]any{
					"kind":                 "deliberate_overlap",
					"id":                   "deliberate-overlap-interval",
					"start_ms":             1000,
					"end_ms":               6500,
					"a":                    participantA,
					"b":                    participantB,
					"a_sent_stream_id":     participantA + ":sent",
					"a_received_stream_id": participantA + ":received",
					"b_sent_stream_id":     participantB + ":sent",
					"b_received_stream_id": participantB + ":received",
				},
			},
			"loudness": []any{
				map[string]any{
					"kind":     "loudness",
					"id":       "deliberate-overlap-balance",
					"start_ms": 1000,
					"end_ms":   6500,
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
				"kept eight seconds with a 5.5 second simultaneous-speech interval",
				"delayed each provider-bound peer received stream by 20 ms",
				"preserved stable sent/received identities and absolute millisecond alignment",
				"split each output stream into four timestamped delta chunks",
			},
			"received_audio_dependency_revision": "s2s-room-participants-deaf-while-speaking@1ed45336; s2s-room-recording-completeness@649f0cd6",
			"regeneration_command":               "go run ./agent-cli/internal/services/testdata/room-audio/generate.go --shape deliberate-overlap --output agent-cli/internal/services/testdata/room-audio/deliberate-overlap",
			"hashes":                             "sha256 and byte sizes for every replay artifact are embedded in this manifest",
		},
		"tolerances": map[string]any{
			"name":                           "deliberate-overlap-v1",
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
	sampleCount := duration
	for _, stream := range streams {
		if len(stream) > sampleCount {
			sampleCount = len(stream)
		}
	}
	result := make([]int16, sampleCount)
	for index := range result {
		var value int32
		for _, stream := range streams {
			if index >= len(stream) {
				continue
			}
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

func setLoudBoundaryImpulse(samples []int16, boundary int) {
	if boundary <= 0 || boundary >= len(samples) {
		return
	}
	samples[boundary-1] = -7000
	samples[boundary] = 7000
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
	return deltaJSONLWithBoundaries(id, turns, samples, []int{0, 1200, 2200, 3200, len(samples)})
}

func deltaJSONLWithBoundaries(id string, turns []turn, samples []int16, boundaries []int) []byte {
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
	type timelineEntry struct {
		turn          turn
		participantID string
		typeName      string
		offset        int
	}
	entries := make([]timelineEntry, 0, len(turns)*2)
	for _, currentTurn := range turns {
		participantID := participantA
		if len(currentTurn.ID) <= len(participantA) || currentTurn.ID[:len(participantA)] != participantA {
			participantID = participantB
		}
		entries = append(entries,
			timelineEntry{turn: currentTurn, participantID: participantID, typeName: "turn_started", offset: currentTurn.Start},
			timelineEntry{turn: currentTurn, participantID: participantID, typeName: "turn_ended", offset: currentTurn.End},
		)
	}
	sort.SliceStable(entries, func(left, right int) bool {
		return entries[left].offset < entries[right].offset
	})
	values := make([]map[string]any, 0, len(entries))
	sequence := 0
	for _, entry := range entries {
		values = append(values, map[string]any{
			"sequence":            sequence,
			"monotonic_offset_ms": entry.offset,
			"unix_ms":             clock.UnixMilli() + int64(entry.offset),
			"type":                entry.typeName,
			"participant_id":      entry.participantID,
			"turn_id":             entry.turn.ID,
		})
		sequence++
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
	return chunkBoundariesFor(id, []int{0, 1200, 2200, 3200, duration})
}

func chunkBoundariesFor(id string, boundaries []int) []map[string]any {
	result := make([]map[string]any, 0, len(boundaries)-2)
	for index := 1; index < len(boundaries)-1; index++ {
		result = append(result, map[string]any{
			"id":           fmt.Sprintf("%s-chunk-%d", id, index),
			"sample_index": boundaries[index],
		})
	}
	return result
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
	return captureJSONForModel(id, "clean-turn-taking-v1")
}

func captureJSONForModel(id, model string) []byte {
	payload, err := json.Marshal(map[string]any{"type": "session.created", "session": map[string]any{"id": id + "-offline-session"}})
	if err != nil {
		panic(err)
	}
	type providerMetadata struct {
		Name  string `json:"name,omitempty"`
		Model string `json:"model,omitempty"`
	}
	type sessionMetadata struct {
		ID                string `json:"id,omitempty"`
		StartedAtUTC      string `json:"started_at_utc,omitempty"`
		FixtureProvenance string `json:"fixture_provenance,omitempty"`
	}
	type record struct {
		Sequence    int             `json:"sequence"`
		Direction   string          `json:"direction"`
		Timestamp   int64           `json:"timestamp_ms"`
		Type        string          `json:"type"`
		PayloadType string          `json:"payload_type"`
		Payload     json.RawMessage `json:"payload,omitempty"`
	}
	type integrity struct {
		Algorithm string `json:"algorithm"`
		Coverage  string `json:"coverage"`
		Digest    string `json:"digest"`
	}
	type captureEnvelope struct {
		Version            int              `json:"version"`
		Provider           providerMetadata `json:"provider"`
		Session            sessionMetadata  `json:"session"`
		Records            []record         `json:"records"`
		Integrity          *integrity       `json:"integrity,omitempty"`
		EndsWithDisconnect bool             `json:"ends_with_disconnect,omitempty"`
	}
	base := captureEnvelope{
		Version:  2,
		Provider: providerMetadata{Name: "offline", Model: model},
		Session: sessionMetadata{
			ID:                id + "-offline-session",
			StartedAtUTC:      "2026-08-30T12:00:00Z",
			FixtureProvenance: "synthetic",
		},
		Records: []record{{
			Sequence:    1,
			Direction:   "server_to_client",
			Timestamp:   0,
			Type:        "session.created",
			PayloadType: "websocket_message",
			Payload:     json.RawMessage(payload),
		}},
	}
	coverage, err := json.Marshal(base)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(coverage)
	base.Integrity = &integrity{
		Algorithm: "sha256",
		Coverage:  "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)",
		Digest:    hex.EncodeToString(digest[:]),
	}
	data, err := json.MarshalIndent(base, "", "  ")
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
