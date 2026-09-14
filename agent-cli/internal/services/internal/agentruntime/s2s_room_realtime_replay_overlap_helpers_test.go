package agentruntime

import "testing"

func runRoomSpeechOverlapContentful(t *testing.T) {
	scenario := newRoomSpeechOverlapScenario(t, []byte{0x20, 0x03, 0xe0, 0xfc})
	runRoomSpeechOverlapFrames(t, scenario)
	runRoomSpeechOverlapOutcome(t, scenario)
	runRoomSpeechOverlapDiagnostics(t, scenario)
	runRoomSpeechOverlapTargetWrites(t, scenario)
	runRoomSpeechOverlapSpeakerWrites(t, scenario)
}

func runRoomSpeechOverlapFrames(t *testing.T, scenario *roomSpeechOverlapScenario) {
	scenario.targetCadence.Advance()
	assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.silence)
	awaitRoomSpeechOverlapInput(t, scenario.targetInput, scenario.silence)
	awaitRoomSpeechOverlapAudio(t, scenario.targetAudio, scenario.targetOutput)
	awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "target", "speaker", scenario.targetOutput)

	scenario.peerCadence.Advance()
	assertRoomSpeechOverlapAppend(t, scenario.harness.participant("speaker"), scenario.targetOutput)
	awaitRoomSpeechOverlapAudio(t, scenario.peerAudio, scenario.peerOutput)
	awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)

	scenario.targetCadence.Advance()
	awaitRoomSpeechOverlapInput(t, scenario.targetInput, scenario.expectedSpeech)
	awaitRoomSpeechOverlapAudio(t, scenario.targetAudio, scenario.secondTargetOutput)
	awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "target", "speaker", scenario.secondTargetOutput)

	scenario.peerCadence.Advance()
	assertRoomSpeechOverlapAppend(t, scenario.harness.participant("speaker"), scenario.secondTargetOutput)
	awaitRoomSpeechOverlapAudio(t, scenario.peerAudio, scenario.peerOutput)
	awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)

	scenario.targetCadence.Advance()
	assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.expectedSpeech)
	awaitRoomSpeechOverlapTargetEnd(t, scenario.targetEnds)
	awaitRoomSpeechOverlapMessageEnd(t, scenario.speakerEnds, "speaker")
}

func runRoomSpeechOverlapOutcome(t *testing.T, scenario *roomSpeechOverlapScenario) {
	outcome := awaitRoomSpeechOverlapRun(t, scenario)
	if outcome.err != nil {
		t.Fatalf("speech-overlap room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("speech-overlap room termination = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}
	for _, participantID := range []string{"speaker", "target"} {
		participantResult, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("speech-overlap result missing participant %q", participantID)
		}
		if !participantResult.Connected || participantResult.TurnsCompleted != 1 {
			t.Fatalf("speech-overlap participant %q result = %+v, want one normal completed turn", participantID, participantResult)
		}
		if err := scenario.harness.participant(participantID).dialer.Err(); err != nil {
			t.Fatalf("speech-overlap participant %q strict wire: %v", participantID, err)
		}
	}
}

func runRoomSpeechOverlapDiagnostics(t *testing.T, scenario *roomSpeechOverlapScenario) {
	diagnosticCounts := map[string]int{}
	for range []int{0, 1} {
		select {
		case participantID := <-scenario.diagnostic:
			diagnosticCounts[participantID]++
		case <-scenario.ctx.Done():
			t.Fatalf("speech-overlap diagnostics did not report both normal turns: %v", scenario.ctx.Err())
		}
	}
	if diagnosticCounts["speaker"] != 1 || diagnosticCounts["target"] != 1 {
		t.Fatalf("speech-overlap diagnostic turns = %v, want one completed turn per participant", diagnosticCounts)
	}
}

func runRoomSpeechOverlapTargetWrites(t *testing.T, scenario *roomSpeechOverlapScenario) {
	targetWrites := scenario.harness.participant("target").outboundSnapshot()
	wantTypes := []string{"session.update", "input_audio_buffer.append", "input_audio_buffer.append", "input_audio_buffer.append"}
	gotTypes := make([]string, 0, len(targetWrites))
	for _, write := range targetWrites {
		gotTypes = append(gotTypes, write.Type)
	}
	if !sameRoomReplayStrings(gotTypes, wantTypes) {
		t.Fatalf("target overlap outbound types = %v, want %v", gotTypes, wantTypes)
	}
	wantAppends := [][]byte{scenario.silence, scenario.expectedSpeech, scenario.expectedSpeech}
	appendWriteIndexes := []int{1, 2, 3}
	for index, wantPCM := range wantAppends {
		assertRoomSpeechOverlapWireAppendPayload(t, targetWrites[appendWriteIndexes[index]], wantPCM)
	}
	appendCount := 0
	for _, write := range targetWrites {
		if write.Type == "input_audio_buffer.append" {
			appendCount++
		}
	}
	if got := appendCount; got != len(wantAppends) {
		t.Fatalf("target overlap append count = %d, want %d", got, len(wantAppends))
	}
	if got := countRoomReplayWireType(targetWrites, "response.cancel"); got != 0 {
		t.Fatalf("target peer overlap response.cancel count = %d, want zero", got)
	}
	if got := scenario.harness.participant("target").inboundTypes(); !sameRoomReplayStrings(got, []string{
		"session.created", "response.created", "response.output_audio.delta", "response.output_audio.delta",
		"response.output_audio.done", "response.done",
	}) {
		t.Fatalf("target overlap inbound provider events = %v", got)
	}
}

func runRoomSpeechOverlapSpeakerWrites(t *testing.T, scenario *roomSpeechOverlapScenario) {
	speakerWrites := scenario.harness.participant("speaker").outboundSnapshot()
	if got := len(speakerWrites); got != 3 {
		t.Fatalf("speaker overlap outbound count = %d, want session.update plus two appends", got)
	}
	for index, wantPCM := range [][]byte{scenario.targetOutput, scenario.secondTargetOutput} {
		assertRoomSpeechOverlapWireAppendPayload(t, speakerWrites[index+1], wantPCM)
	}
}
