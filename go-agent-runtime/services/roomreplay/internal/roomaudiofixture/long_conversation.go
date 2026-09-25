package roomaudiofixture

import (
	"fmt"
	"sort"
	"time"
)

// Long conversation fixture: four evenly spaced two-second turns per
// participant, alternating every three seconds.
const (
	longConversationFirstStartA = 200
	longConversationFirstStartB = 3200
	longConversationTurnSpacing = 6000
	longConversationTurnLength  = 2000
	longConversationSeedA       = 311
	longConversationSeedB       = 719
	longConversationElapsed     = 24 * time.Second
	// Each turn contributes started, ended, and completed timeline entries.
	longConversationEntriesPerTurn = 3
	// Two terminal entries per participant plus the run terminal entry.
	longConversationTerminalEntries = 5
)

// longConversationTurns returns participant's ordered turn schedule.
func longConversationTurns(participant string, firstStart int) []turn {
	turns := make([]turn, 0, longConversationTurnsPerParticipant)
	for index := range longConversationTurnsPerParticipant {
		start := firstStart + index*longConversationTurnSpacing
		turns = append(turns, turn{ID: fmt.Sprintf("%s-turn-%d", participant, index+1), Start: start, End: start + longConversationTurnLength})
	}
	return turns
}

// generateLongConversationTermination writes the committed post-fix
// termination golden. It is intentionally synthetic and deterministic: the
// eight ordered turns model the multi-second cadence of the production
// regression while the manifest and sidecars preserve the room-owned
// max-turn terminal projection that the runtime must produce.
func generateLongConversationTermination(output string) error {
	if err := prepareOutput(output); err != nil {
		return err
	}
	aTurns := longConversationTurns(participantA, longConversationFirstStartA)
	bTurns := longConversationTurns(participantB, longConversationFirstStartB)
	aSent := syntheticSpeech(longConversationDuration, aTurns, longConversationSeedA)
	bSent := syntheticSpeech(longConversationDuration, bTurns, longConversationSeedB)
	deltaBoundaries := fixedDeltaBoundaries(longConversationDuration, sampleRate)
	terminalFields := longConversationTerminalFields()
	clock := time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC)
	identity := participantIdentity{
		model:         "room-bound-long-conversation-v1",
		openingPrompt: "Begin the deterministic post-fix long conversation fixture.",
		systemPrompt:  "Use the privacy-safe synthetic long-conversation fixture; do not contact a provider.",
	}

	files := make(map[string][]byte)
	participants := make(map[string]any, 2)
	for _, audio := range pairedParticipants(aTurns, bTurns, aSent, bSent, longConversationRouteDelay) {
		sidecars := participantSidecars{
			deltas:      deltaJSONLWithBoundaries(audio.id, audio.turns, audio.sent, deltaBoundaries),
			events:      longConversationEventJSONL(audio.id, audio.turns, terminalFields),
			diagnostics: longConversationDiagnosticJSONL(audio.id, audio.turns, terminalFields, clock),
			capture:     captureJSONForModel(audio.id, identity.model),
		}
		ref := addParticipant(files, audio, sidecars, identity, chunkBoundariesFor(audio.id, deltaBoundaries), longConversationDuration)
		addLongConversationTerminalFields(ref, terminalFields)
		participants[audio.id] = ref
	}

	artifacts := roomArtifacts(files, longConversationTimelineJSONL(clock, allTurns(aTurns, bTurns), terminalFields), wavBytes(mix(aSent, bSent)))
	manifest := baseManifest(clock, longConversationElapsed, participants, artifacts)
	addLongConversationManifestFields(manifest)
	return writeFixture(output, files, manifest)
}

func addLongConversationTerminalFields(ref map[string]any, terminalFields map[string]string) {
	ref["termination_reason"] = "ended"
	ref["reason"] = "ended"
	ref["connected"] = true
	for _, key := range []string{"termination_trigger", "termination_disposition", "classification", "terminal_reason", "terminal_provenance", "output_state"} {
		ref[key] = terminalFields[key]
	}
}

func addLongConversationManifestFields(manifest map[string]any) {
	manifest["fixture_id"] = "long-conversation-termination-v1"
	manifest["termination_reason"] = "max_turns_reached"
	manifest["reason"] = "max_turns_reached"
	manifest["bounds"] = map[string]any{"max_turns": longConversationTurnsPerParticipant}
	manifest["turn_counts"] = map[string]int{participantA: longConversationTurnsPerParticipant, participantB: longConversationTurnsPerParticipant}
	manifest["room_mix"] = "room-mix.wav"
	manifest["room_timeline"] = "room-timeline.jsonl"
	manifest["audio_format"] = map[string]any{"sample_rate": sampleRate, "channels": 1, "encoding": "pcm_s16le"}
	manifest["annotations"] = map[string]any{
		"loudness": []any{loudnessAnnotation("long-conversation-balance", longConversationFirstStartA, longConversationBoundOffset)},
	}
	manifest["provenance"] = provenance(map[string]any{
		"fixture_provenance": "synthetic_post_fix_termination",
		"source_run":         "offline deterministic scripted-provider replay; no provider, microphone, credentials, or private recording",
		"transformations": []string{
			"generated mono signed little-endian PCM16 at 1000 Hz",
			"kept 24 seconds with eight ordered turns, four per participant",
			"preserved one-second delta boundaries across each multi-second scripted turn",
			"recorded max_turns=4 after the final provider-authored response",
			"retained room-owned terminal fields in participant sidecars and timeline",
		},
		"regeneration_command": regenerationCommand(" --shape "+ShapeLongConversationTermination, ShapeLongConversationTermination),
	})
	manifest["tolerances"] = tolerances("long-conversation-termination-v1")
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

type longConversationTimelineEntry struct {
	offset        int
	participantID string
	event         string
	turnID        string
	fields        map[string]string
}

func longConversationTimelineJSONL(clock time.Time, turns []turn, terminalFields map[string]string) []byte {
	entries := longConversationTimelineEntries(turns, terminalFields)
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

func longConversationTimelineEntries(turns []turn, terminalFields map[string]string) []longConversationTimelineEntry {
	entries := make([]longConversationTimelineEntry, 0, len(turns)*longConversationEntriesPerTurn+longConversationTerminalEntries)
	for _, currentTurn := range turns {
		participantID := participantB
		if len(currentTurn.ID) >= len(participantA) && currentTurn.ID[:len(participantA)] == participantA {
			participantID = participantA
		}
		entries = append(entries,
			longConversationTimelineEntry{offset: currentTurn.Start, participantID: participantID, event: "turn_started", turnID: currentTurn.ID},
			longConversationTimelineEntry{offset: currentTurn.End, participantID: participantID, event: "turn_ended", turnID: currentTurn.ID},
			longConversationTimelineEntry{offset: currentTurn.End, participantID: participantID, event: "turn_completed", turnID: currentTurn.ID},
		)
	}
	for _, participantID := range []string{participantA, participantB} {
		entries = append(entries,
			longConversationTimelineEntry{offset: longConversationBoundOffset, participantID: participantID, event: "room_bound_shutdown", fields: terminalFields},
			longConversationTimelineEntry{offset: longConversationBoundOffset, participantID: participantID, event: "participant_terminated", fields: terminalFields},
		)
	}
	entries = append(entries, longConversationTimelineEntry{offset: longConversationDuration, event: "run_terminated", fields: map[string]string{"reason": "max_turns_reached"}})
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].offset < entries[right].offset })
	return entries
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
