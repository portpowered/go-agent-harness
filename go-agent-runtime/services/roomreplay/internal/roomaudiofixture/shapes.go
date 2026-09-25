package roomaudiofixture

import "time"

// Clean turn-taking fixture: two alternating turns per participant.
const (
	cleanRouteDelay   = 20
	cleanSeedA        = 11
	cleanSeedB        = 29
	cleanFirstStartMS = 200
	cleanLastEndMS    = 3800
	cleanElapsed      = 4 * time.Second
)

// Deliberate overlap fixture: one 5.5 second simultaneous turn each.
const (
	overlapRouteDelay = 20
	overlapSeedA      = 101
	overlapSeedB      = 202
	overlapStartMS    = 1000
	overlapEndMS      = 6500
	overlapImpulseA   = 2200
	overlapImpulseB   = 3200
	overlapElapsed    = 8 * time.Second
)

// cleanTurns returns the clean fixture's turn schedule for both participants.
func cleanTurns() (aTurns, bTurns []turn) {
	const (
		aTurn1Start, aTurn1End = cleanFirstStartMS, 1000
		aTurn2Start, aTurn2End = 2200, 3000
		bTurn1Start, bTurn1End = 1200, 2000
		bTurn2Start, bTurn2End = 3200, cleanLastEndMS
	)
	aTurns = []turn{{ID: "agent-a-turn-1", Start: aTurn1Start, End: aTurn1End}, {ID: "agent-a-turn-2", Start: aTurn2Start, End: aTurn2End}}
	bTurns = []turn{{ID: "agent-b-turn-1", Start: bTurn1Start, End: bTurn1End}, {ID: "agent-b-turn-2", Start: bTurn2Start, End: bTurn2End}}
	return aTurns, bTurns
}

// generateCleanTurnTaking writes the four-second fixture in which the two
// participants alternate turns without overlap.
func generateCleanTurnTaking(output string) error {
	if err := prepareOutput(output); err != nil {
		return err
	}
	aTurns, bTurns := cleanTurns()
	aSent := syntheticSpeech(duration, aTurns, cleanSeedA)
	bSent := syntheticSpeech(duration, bTurns, cleanSeedB)
	identity := participantIdentity{
		model:         "clean-turn-taking-v1",
		openingPrompt: "Begin the deterministic clean turn-taking fixture.",
		systemPrompt:  "Use the privacy-safe synthetic fixture transcript; do not contact a provider.",
	}

	files := make(map[string][]byte)
	participants := make(map[string]any)
	for _, audio := range pairedParticipants(aTurns, bTurns, aSent, bSent, cleanRouteDelay) {
		sidecars := participantSidecars{
			deltas:      deltaJSONL(audio.id, audio.turns, audio.sent),
			events:      eventJSONL(audio.id, audio.turns),
			diagnostics: diagnosticJSONL(audio.id, audio.turns),
			capture:     captureJSONForModel(audio.id, identity.model),
		}
		participants[audio.id] = addParticipant(files, audio, sidecars, identity, chunkBoundaries(audio.id), duration)
	}

	clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	artifacts := roomArtifacts(files, timelineJSONL(clock, allTurns(aTurns, bTurns)), wavBytes(mix(aSent, bSent)))
	manifest := baseManifest(clock, cleanElapsed, participants, artifacts)
	manifest["annotations"] = map[string]any{
		"loudness": []any{loudnessAnnotation("clean-turn-balance", cleanFirstStartMS, cleanLastEndMS)},
	}
	manifest["provenance"] = provenance(map[string]any{
		"fixture_provenance": "synthetic",
		"source_run":         "offline deterministic generator; no provider, microphone, credentials, or private recording",
		"transformations": []string{
			"generated mono signed little-endian PCM16 at 1000 Hz",
			"kept four seconds with two non-empty turns per participant",
			"delayed peer received streams by 20 ms",
			"split each output stream into four timestamped delta chunks",
		},
		"regeneration_command": regenerationCommand("", ShapeCleanTurnTaking),
	})
	manifest["tolerances"] = tolerances("clean-turn-taking-v1")
	return writeFixture(output, files, manifest)
}

// generateDeliberateOverlap writes the eight-second fixture in which both
// participants speak simultaneously for 5.5 seconds.
func generateDeliberateOverlap(output string) error {
	if err := prepareOutput(output); err != nil {
		return err
	}
	aTurns := []turn{{ID: "agent-a-overlap-turn", Start: overlapStartMS, End: overlapEndMS}}
	bTurns := []turn{{ID: "agent-b-overlap-turn", Start: overlapStartMS, End: overlapEndMS}}
	aSent := syntheticSpeech(overlapDuration, aTurns, overlapSeedA)
	bSent := syntheticSpeech(overlapDuration, bTurns, overlapSeedB)
	// Keep these large jumps inside loud neighboring windows so the boundary
	// analyzer reports them as observable speech impulses, not clicks.
	setLoudBoundaryImpulse(aSent, overlapImpulseA)
	setLoudBoundaryImpulse(bSent, overlapImpulseB)
	identity := participantIdentity{
		model:         "deliberate-overlap-v1",
		openingPrompt: "Begin the deterministic deliberate overlap fixture.",
		systemPrompt:  "Use the privacy-safe synthetic overlap fixture; do not contact a provider.",
	}

	files := make(map[string][]byte)
	participants := make(map[string]any)
	for _, audio := range pairedParticipants(aTurns, bTurns, aSent, bSent, overlapRouteDelay) {
		sidecars := participantSidecars{
			deltas:      deltaJSONL(audio.id, audio.turns, audio.sent),
			events:      eventJSONL(audio.id, audio.turns),
			diagnostics: diagnosticJSONL(audio.id, audio.turns),
			capture:     captureJSONForModel(audio.id, identity.model),
		}
		ref := addParticipant(files, audio, sidecars, identity, chunkBoundaries(audio.id), overlapDuration)
		ref["received_audio_contract"] = "provider-bound room delivery in participants/<id>/received.pcm"
		participants[audio.id] = ref
	}

	clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	artifacts := roomArtifacts(files, timelineJSONL(clock, allTurns(aTurns, bTurns)), wavBytes(mix(aSent, bSent)))
	manifest := baseManifest(clock, overlapElapsed, participants, artifacts)
	manifest["annotations"] = map[string]any{
		"overlaps": []any{overlapAnnotation()},
		"loudness": []any{loudnessAnnotation("deliberate-overlap-balance", overlapStartMS, overlapEndMS)},
	}
	manifest["provenance"] = provenance(map[string]any{
		"fixture_provenance": "synthetic",
		"source_run":         "offline deterministic generator; no provider, microphone, credentials, or private recording",
		"transformations": []string{
			"generated mono signed little-endian PCM16 at 1000 Hz",
			"kept eight seconds with a 5.5 second simultaneous-speech interval",
			"delayed each provider-bound peer received stream by 20 ms",
			"preserved stable sent/received identities and absolute millisecond alignment",
			"split each output stream into four timestamped delta chunks",
		},
		"received_audio_dependency_revision": "s2s-room-participants-deaf-while-speaking@1ed45336; s2s-room-recording-completeness@649f0cd6",
		"regeneration_command":               regenerationCommand(" --shape "+ShapeDeliberateOverlap, ShapeDeliberateOverlap),
	})
	manifest["tolerances"] = tolerances("deliberate-overlap-v1")
	return writeFixture(output, files, manifest)
}

func overlapAnnotation() map[string]any {
	return map[string]any{
		"kind":                 "deliberate_overlap",
		"id":                   "deliberate-overlap-interval",
		"start_ms":             overlapStartMS,
		"end_ms":               overlapEndMS,
		"a":                    participantA,
		"b":                    participantB,
		"a_sent_stream_id":     participantA + ":sent",
		"a_received_stream_id": participantA + ":received",
		"b_sent_stream_id":     participantB + ":sent",
		"b_received_stream_id": participantB + ":received",
	}
}
