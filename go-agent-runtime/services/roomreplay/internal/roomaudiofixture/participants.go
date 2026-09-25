package roomaudiofixture

import "path/filepath"

// participantAudio is one participant's synthetic audio: the turns it speaks,
// the samples it sends, and the peer audio it receives after routing delay.
type participantAudio struct {
	id            string
	turns         []turn
	sent          []int16
	received      []int16
	receivedTurns []turn
}

// participantSidecars holds the per-participant JSON sidecar payloads.
type participantSidecars struct {
	deltas      []byte
	events      []byte
	diagnostics []byte
	capture     []byte
}

// participantIdentity holds the descriptive fields of a participant reference.
type participantIdentity struct {
	model         string
	openingPrompt string
	systemPrompt  string
}

type participantPaths struct {
	wav, sent, received, deltas, events, diagnostics, capture string
}

func pathsFor(id string) participantPaths {
	directory := filepath.ToSlash(filepath.Join("participants", id))
	join := func(name string) string { return filepath.ToSlash(filepath.Join(directory, name)) }
	return participantPaths{
		wav:         join("agent.wav"),
		sent:        join("sent.pcm"),
		received:    join("received.pcm"),
		deltas:      join("deltas.jsonl"),
		events:      join("events.jsonl"),
		diagnostics: join("diagnostics.jsonl"),
		capture:     join("capture.session.json"),
	}
}

// pairedParticipants returns both participants, each receiving the other's
// sent audio delayed by routeDelay samples.
func pairedParticipants(aTurns, bTurns []turn, aSent, bSent []int16, routeDelay int) []participantAudio {
	return []participantAudio{
		{id: participantA, turns: aTurns, sent: aSent, received: delayedCopy(bSent, routeDelay), receivedTurns: shiftTurns(bTurns, routeDelay)},
		{id: participantB, turns: bTurns, sent: bSent, received: delayedCopy(aSent, routeDelay), receivedTurns: shiftTurns(aTurns, routeDelay)},
	}
}

// addParticipant writes one participant's audio and sidecars into files and
// returns its manifest reference. chunks are the output stream's chunk
// boundaries and endMS is the shared stream timeline end.
func addParticipant(files map[string][]byte, audio participantAudio, sidecars participantSidecars, identity participantIdentity, chunks []map[string]any, endMS int) map[string]any {
	paths := pathsFor(audio.id)
	files[paths.wav] = wavBytes(audio.sent)
	files[paths.sent] = pcmBytes(audio.sent)
	files[paths.received] = pcmBytes(audio.received)
	files[paths.deltas] = sidecars.deltas
	files[paths.events] = sidecars.events
	files[paths.diagnostics] = sidecars.diagnostics
	files[paths.capture] = sidecars.capture

	return map[string]any{
		"id":              audio.id,
		"kind":            "agent",
		"provider":        "offline",
		"model":           identity.model,
		"voice":           "synthetic-noise",
		"opening_prompt":  identity.openingPrompt,
		"system_prompt":   identity.systemPrompt,
		"completed_turns": len(audio.turns),
		"capture":         artifactRefFor(paths.capture, files[paths.capture]),
		"artifacts": map[string]artifactRef{
			"wav":          artifactRefFor(paths.wav, files[paths.wav]),
			"sent_pcm":     artifactRefFor(paths.sent, files[paths.sent]),
			"received_pcm": artifactRefFor(paths.received, files[paths.received]),
			"deltas":       artifactRefFor(paths.deltas, files[paths.deltas]),
			"events":       artifactRefFor(paths.events, files[paths.events]),
			"diagnostics":  artifactRefFor(paths.diagnostics, files[paths.diagnostics]),
		},
		"streams": participantStreams(audio, chunks, endMS),
	}
}

func participantStreams(audio participantAudio, chunks []map[string]any, endMS int) map[string]any {
	return map[string]any{
		"wav": map[string]any{
			"stream_id":         audio.id + ":output",
			"timeline_start_ms": 0,
			"timeline_end_ms":   endMS,
			"expected_speech":   speechAnnotations(audio.turns),
			"chunk_boundaries":  chunks,
		},
		"sent": map[string]any{
			"stream_id":         audio.id + ":sent",
			"timeline_start_ms": 0,
			"timeline_end_ms":   endMS,
			"expected_speech":   speechAnnotations(audio.turns),
		},
		"received": map[string]any{
			"stream_id":         audio.id + ":received",
			"timeline_start_ms": 0,
			"timeline_end_ms":   endMS,
			"expected_speech":   speechAnnotations(audio.receivedTurns),
		},
	}
}

func allTurns(aTurns, bTurns []turn) []turn {
	return append(append([]turn(nil), aTurns...), bTurns...)
}
