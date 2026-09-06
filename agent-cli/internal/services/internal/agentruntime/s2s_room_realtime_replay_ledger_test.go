package agentruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type roomRealtimeLedgerResponse struct {
	input []byte
	id    string
	text  string
	audio []byte
}

func roomRealtimeLedgerCapture(t *testing.T, participantID, model string, responses ...roomRealtimeLedgerResponse) gwtesting.SessionCapture {
	t.Helper()
	sequence := 1
	records := make([]gwtesting.CapturedSessionEvent, 0, 2+len(responses)*5)
	add := func(direction gwtesting.SessionEventDirection, eventType string, payload []byte) {
		records = append(records, roomRealtimeReplayEvent(sequence, direction, eventType, payload))
		sequence++
	}
	add(gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, participantID+" system"))
	add(gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
		"type": "session.created", "session": map[string]any{"id": "sess-" + participantID + "-ledger", "model": model},
	}))
	for _, response := range responses {
		input := append([]byte(nil), response.input...)
		add(gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(input),
		}))
		add(gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": response.id},
		}))
		switch {
		case len(response.audio) > 0:
			audio := base64.StdEncoding.EncodeToString(response.audio)
			add(gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_audio.delta", "response_id": response.id, "delta": audio,
			}))
			add(gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_audio.done", "response_id": response.id,
			}))
		case response.text != "":
			add(gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_text.delta", "response_id": response.id, "delta": response.text,
			}))
			add(gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_text.done", "response_id": response.id,
			}))
		}
		done := map[string]any{
			"type": "response.done", "response": map[string]any{"id": response.id, "status": "completed"},
		}
		if len(response.audio) == 0 && response.text == "" {
			done["output"] = []any{}
		}
		add(gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, done))
	}
	return roomRealtimeReplayCapture(model, records...)
}

type roomRealtimeLedgerFanout struct {
	sourceID string
	targetID string
	pcm      []byte
}

func TestRunRoomWithResult_PreservesExactThreeParticipantTurnLedgers(t *testing.T) {
	const (
		sourceID = "source"
		alphaID  = "alpha"
		betaID   = "beta"
		model    = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	sourcePCM := []byte{0x34, 0x12, 0x78, 0x56}
	captures := map[string]gwtesting.SessionCapture{
		sourceID: roomRealtimeLedgerCapture(t, sourceID, model,
			roomRealtimeLedgerResponse{input: silence, id: "resp-source-audio", audio: sourcePCM},
			roomRealtimeLedgerResponse{input: silence, id: "resp-source-final", text: "source final response"},
		),
		alphaID: roomRealtimeLedgerCapture(t, alphaID, model,
			roomRealtimeLedgerResponse{input: sourcePCM, id: "resp-alpha-1", text: "alpha response one"},
			roomRealtimeLedgerResponse{input: silence, id: "resp-alpha-2", text: "alpha response two"},
			roomRealtimeLedgerResponse{input: silence, id: "resp-alpha-3", text: "alpha response three"},
		),
		betaID: roomRealtimeLedgerCapture(t, betaID, model,
			// This response is a complete provider lifecycle with no output.
			roomRealtimeLedgerResponse{input: sourcePCM, id: "resp-beta-empty"},
			roomRealtimeLedgerResponse{input: silence, id: "resp-beta-1", text: "beta response one"},
			roomRealtimeLedgerResponse{input: silence, id: "resp-beta-2", text: "beta response two"},
			roomRealtimeLedgerResponse{input: silence, id: "resp-beta-3", text: "beta response three"},
			roomRealtimeLedgerResponse{input: silence, id: "resp-beta-4", text: "beta response four"},
		),
	}
	harness := newRoomRealtimeReplayHarness(t, captures)

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_LEDGER_SOURCE_KEY": "room-ledger-source-key",
		"ROOM_LEDGER_ALPHA_KEY":  "room-ledger-alpha-key",
		"ROOM_LEDGER_BETA_KEY":   "room-ledger-beta-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 2},
		Participants: []room.Participant{
			{ID: sourceID, SystemPrompt: sourceID + " system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_LEDGER_SOURCE_KEY", Tools: []string{}},
			{ID: alphaID, SystemPrompt: alphaID + " system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_LEDGER_ALPHA_KEY", Tools: []string{}},
			{ID: betaID, SystemPrompt: betaID + " system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_LEDGER_BETA_KEY", Tools: []string{}},
		},
	}

	cadenceReady := make(chan *roomRealtimeReplayCadence, len(manifest.Participants))
	mixerConfig := room.PCM16MixerConfig{
		Format:            room.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		InputQueueFrames:  8,
		OutputQueueFrames: 8,
		CadenceFactory: func(time.Duration) room.PCM16Cadence {
			cadence := newRoomRealtimeReplayCadence()
			cadenceReady <- cadence
			return cadence
		},
	}
	opened := make(chan string, len(manifest.Participants))
	responseEnds := make(chan string, 16)
	fanouts := make(chan roomRealtimeLedgerFanout, 4)
	sourceAudio := make(chan []byte, 1)
	allowSourceFanout := make(chan struct{})
	diagnosticTurns := make(chan string, 16)
	roomCtx, cancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	defer cancel()

	opts := RoomRunOptions{
		Manifest:  manifest,
		ConfigDir: configDir, ModelCatalog: testModelCatalog(),
		BaseURL:     "wss://room-replay.invalid/v1/realtime",
		MixerConfig: mixerConfig,
		CredentialLookup: func(name string) (string, bool) {
			value, ok := credentials[name]
			return value, ok
		},
		WebSocketDialerFactory: harness.DialerFactory,
		onParticipantSessionOpen: func(participantID string) {
			opened <- participantID
		},
		onParticipantStream: func(participantID string, msg messages.StreamMessage) {
			if msg.Type == messages.StreamTypeMessageEnd {
				responseEnds <- participantID
			}
		},
		onParticipantAudioFanned: func(sourceID, targetID string, pcm []byte) {
			fanouts <- roomRealtimeLedgerFanout{sourceID: sourceID, targetID: targetID, pcm: append([]byte(nil), pcm...)}
		},
		OnAudioOutput: func(participantID string, pcm []byte) error {
			if participantID == sourceID {
				sourceAudio <- append([]byte(nil), pcm...)
				<-allowSourceFanout
			}
			return nil
		},
		OnDiagnostic: func(participantID string, record SessionDiagnosticRecord) {
			if record.Event == SessionDiagnosticEventTurn {
				diagnosticTurns <- participantID
			}
		},
	}

	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(roomCtx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	var sourceCadence, alphaCadence, betaCadence *roomRealtimeReplayCadence
	for _, cadence := range []**roomRealtimeReplayCadence{&sourceCadence, &alphaCadence, &betaCadence} {
		select {
		case *cadence = <-cadenceReady:
		case <-roomCtx.Done():
			t.Fatalf("ledger mixer cadence was not created: %v", roomCtx.Err())
		}
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("ledger room sessions did not open: %v", roomCtx.Err())
		}
	}

	// Source audio is fanned to both peers before either peer cadence is
	// released. The two real mixer writes are observed and must carry the same
	// byte-exact PCM that the source provider emitted.
	sourceCadence.Advance()
	assertRoomRealtimeReplayAppend(t, harness.participant(sourceID), silence)
	awaitRoomRealtimeLedgerAudio(t, sourceAudio, sourcePCM)
	close(allowSourceFanout)
	seenFanouts := map[string][]byte{}
	fanoutTimer := time.NewTimer(roomRealtimeReplayTestTimeout)
	for len(seenFanouts) < 2 {
		select {
		case fanout := <-fanouts:
			if fanout.sourceID != sourceID {
				t.Fatalf("unexpected ledger fanout source = %q, want %q", fanout.sourceID, sourceID)
			}
			seenFanouts[fanout.targetID] = append([]byte(nil), fanout.pcm...)
		case <-fanoutTimer.C:
			t.Fatalf("ledger did not fan out source audio to both peers: %v", seenFanouts)
		}
	}
	fanoutTimer.Stop()
	for _, targetID := range []string{alphaID, betaID} {
		if got, ok := seenFanouts[targetID]; !ok || !bytes.Equal(got, sourcePCM) {
			t.Fatalf("ledger fanout %s -> %s = %v, want %v", sourceID, targetID, got, sourcePCM)
		}
	}
	awaitRoomRealtimeLedgerResponseEnd(t, responseEnds, sourceID)

	// Alpha reaches three non-empty responses while beta is still below the
	// room's max-turn target, so its extra turn cannot cause early termination.
	alphaCadence.Advance()
	assertRoomRealtimeReplayAppend(t, harness.participant(alphaID), sourcePCM)
	awaitRoomRealtimeLedgerResponseEnd(t, responseEnds, alphaID)
	for _, want := range [][]byte{silence, silence} {
		alphaCadence.Advance()
		assertRoomRealtimeReplayAppend(t, harness.participant(alphaID), want)
		awaitRoomRealtimeLedgerResponseEnd(t, responseEnds, alphaID)
	}

	// Beta's first provider response is empty and must not advance its ledger.
	betaCadence.Advance()
	assertRoomRealtimeReplayAppend(t, harness.participant(betaID), sourcePCM)
	awaitRoomRealtimeLedgerResponseEnd(t, responseEnds, betaID)
	// Four contentful responses follow the empty boundary.
	for range []int{0, 1, 2, 3} {
		betaCadence.Advance()
		assertRoomRealtimeReplayAppend(t, harness.participant(betaID), silence)
		awaitRoomRealtimeLedgerResponseEnd(t, responseEnds, betaID)
	}

	// Source is the final participant to reach max_turns=2. The declared room
	// boundary, rather than an exhausted transport, ends the run.
	sourceCadence.Advance()
	assertRoomRealtimeReplayAppend(t, harness.participant(sourceID), silence)
	awaitRoomRealtimeLedgerResponseEnd(t, responseEnds, sourceID)

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-roomCtx.Done():
		t.Fatalf("ledger room did not reach max-turn boundary: %v", roomCtx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("three-participant ledger room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("ledger room termination = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}

	wantTurns := map[string]int{sourceID: 2, alphaID: 3, betaID: 4}
	diagnosticCounts := map[string]int{}
	for range 9 {
		select {
		case participantID := <-diagnosticTurns:
			diagnosticCounts[participantID]++
		case <-roomCtx.Done():
			t.Fatalf("ledger diagnostics did not report all admitted turns: %v", roomCtx.Err())
		}
	}
	for participantID, want := range wantTurns {
		participantResult, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("ledger result missing participant %q", participantID)
		}
		if !participantResult.Connected || participantResult.TurnsCompleted != want || participantResult.TerminationReason != ParticipantTerminationEnded {
			t.Fatalf("ledger participant %q result = %+v, want %d admitted turns and clean end", participantID, participantResult, want)
		}
		if diagnosticCounts[participantID] != want {
			t.Fatalf("ledger participant %q diagnostics = %d, want %d", participantID, diagnosticCounts[participantID], want)
		}
		capture := captures[participantID]
		if got := roomRealtimeLedgerNonEmptyResponseCount(capture); got != want {
			t.Fatalf("ledger participant %q scripted non-empty responses = %d, want %d", participantID, got, want)
		}
		if err := harness.participant(participantID).dialer.Err(); err != nil {
			t.Fatalf("ledger participant %q strict wire: %v", participantID, err)
		}
		assertRoomRealtimeLedgerWireConsumed(t, participantID, capture, harness.participant(participantID).outboundSnapshot())
	}

	if got := roomRealtimeLedgerResponseTypeCount(captures[betaID], "response.done"); got != 5 {
		t.Fatalf("beta provider response boundaries = %d, want five including one empty response", got)
	}
	if got := roomRealtimeLedgerNonEmptyResponseCount(captures[betaID]); got != 4 {
		t.Fatalf("beta non-empty provider responses = %d, want four after one empty response", got)
	}
}

func awaitRoomRealtimeLedgerAudio(t *testing.T, audio <-chan []byte, want []byte) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case got := <-audio:
		if !bytes.Equal(got, want) {
			t.Fatalf("ledger source output PCM = %v, want %v", got, want)
		}
	case <-timer.C:
		t.Fatalf("ledger source did not emit output PCM %v", want)
	}
}

func awaitRoomRealtimeLedgerResponseEnd(t *testing.T, ends <-chan string, participantID string) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for {
		select {
		case got := <-ends:
			if got == participantID {
				return
			}
		case <-timer.C:
			t.Fatalf("ledger participant %q did not reach response.done", participantID)
		}
	}
}

func roomRealtimeLedgerNonEmptyResponseCount(capture gwtesting.SessionCapture) int {
	count := 0
	for _, event := range capture.Records {
		if event.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		if event.Type == "response.output_audio.delta" || event.Type == "response.output_text.delta" {
			count++
		}
	}
	return count
}

func roomRealtimeLedgerResponseTypeCount(capture gwtesting.SessionCapture, eventType string) int {
	count := 0
	for _, event := range capture.Records {
		if event.Direction == gwtesting.DirectionServerToClient && event.Type == eventType {
			count++
		}
	}
	return count
}

func assertRoomRealtimeLedgerWireConsumed(t *testing.T, participantID string, capture gwtesting.SessionCapture, writes []roomRealtimeReplayWireMessage) {
	t.Helper()
	want := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	for _, event := range capture.Records {
		if event.Direction == gwtesting.DirectionClientToServer {
			want = append(want, event)
		}
	}
	if len(writes) != len(want) {
		t.Fatalf("ledger participant %q outbound wire count = %d, want %d", participantID, len(writes), len(want))
	}
	for index, write := range writes {
		if write.Type != want[index].Type || !bytes.Equal(write.Payload, want[index].Payload) {
			t.Fatalf("ledger participant %q outbound step %d = %+v, want type=%q payload=%s", participantID, index+1, write, want[index].Type, want[index].Payload)
		}
	}
}
