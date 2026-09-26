package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// newTurnStartPlainSpeechAudioReader makes the two sides of the turn-start
// race explicit. The second utterance is released after response 1 has been
// created but before its first output; the third utterance is released only
// after response 2 has reached MESSAGE.END.
func newTurnStartPlainSpeechAudioReader(trace *plainSpeechTrace) *plainSpeechAudioReader {
	return &plainSpeechAudioReader{
		segments: []plainSpeechAudioSegment{
			{frame: plainSpeechFrame(1), endOfTurn: true},
			{frame: plainSpeechFrame(2), gate: func(ctx context.Context) error {
				return trace.waitForCreated(ctx, 1)
			}, endOfTurn: true},
			{frame: plainSpeechFrame(3), gate: func(ctx context.Context) error {
				return trace.waitForDone(ctx, 2)
			}},
		},
	}
}

func runTurnStartPlainSpeechCLI(t *testing.T) plainSpeechRun {
	t.Helper()
	trace := newPlainSpeechTrace()
	server := newTurnStartPlainSpeechServer()
	t.Cleanup(server.shutdown)
	recorder := newPlainSpeechRecordingDialer(server)

	agentCLI, err := newPlainSpeechSessionCLI(recorder)
	if err != nil {
		t.Fatalf("initialize turn-start CLI: %v", err)
	}
	agentCLI.SetSessionStreamObserver(trace.observe)
	root := agentCLI.Generate()
	root.SetIn(newTurnStartPlainSpeechAudioReader(trace))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(t.TempDir(), "config"),
		"session",
		"--record-dir", filepath.Join(t.TempDir(), "recording"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--audio-in", "-",
		"--max-duration", plainSpeechRunTimeout.String(),
	})

	ctx, cancel := diagnosticDeadline(t, plainSpeechRunTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		timer := time.NewTimer(plainSpeechCommandJoinWait)
		select {
		case runErr = <-done:
			timer.Stop()
		case <-timer.C:
			runErr = fmt.Errorf("turn-start CLI command await timed out at %s: %w", plainSpeechRunTimeout, probe.ErrBargeInWait)
		}
	}
	return plainSpeechRun{capture: recorder.Capture(), trace: trace, server: server, err: runErr}
}

// These small composition helpers keep the turn-start scenario on the same
// shipped CLI path as the plain-speech proof without duplicating its provider
// setup. The concrete types remain in the integration package so the test
// cannot accidentally bypass command wiring.
func newPlainSpeechRecordingDialer(server *plainSpeechServer) *gwtesting.RecordingWebSocketDialer {
	return gwtesting.NewRecordingWebSocketDialer(server, "openai", "gpt-realtime")
}

func newPlainSpeechSessionCLI(recorder *gwtesting.RecordingWebSocketDialer) (*cli.AgentCLI, error) {
	return wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortTransportDialer, recorder),
		wire.NewPortSwap(wire.PortToolExecutor, &mockToolExecutor{}),
		wire.NewPortSwap(wire.PortInferencer, &mockInferencer{response: "stateless inferencer should not be called"}),
	)
}

func turnStartPlainSpeechContract() probe.BargeInContract {
	return probe.BargeInContract{
		Inputs: []probe.BargeInInputExpectation{
			{ID: "input-1", TurnID: "turn-1"},
			{ID: "input-2", TurnID: "turn-2"},
			{ID: "input-3", TurnID: "turn-3"},
		},
		Responses: []probe.BargeInResponseExpectation{
			{
				ID: "response-1", InputID: "input-1", TurnID: "turn-1",
				Disposition:   probe.BargeInDispositionCancelled,
				RequireCancel: true, ForbidOutput: true,
			},
			{
				ID: "response-2", InputID: "input-2", TurnID: "turn-2",
				Disposition:  probe.BargeInDispositionCompleted,
				ForbidCancel: true, RequireOutput: true, RequireContinuation: true,
			},
			{
				ID: "response-3", InputID: "input-3", TurnID: "turn-3",
				Disposition:  probe.BargeInDispositionCompleted,
				ForbidCancel: true, RequireOutput: true, RequireContinuation: true,
			},
		},
		RequireSessionTerminal: true,
	}
}

func validateTurnStartPlainSpeechCapture(capture gwtesting.SessionCapture) error {
	return normalizePlainSpeechCapture(capture, "").Validate(turnStartPlainSpeechContract())
}

func turnStartResponseRecordIndex(capture gwtesting.SessionCapture, eventType, responseID string, occurrence int) int {
	return plainSpeechRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Type == eventType && plainSpeechRecordResponseID(record) == responseID
	}, occurrence)
}

func turnStartClientRecordIndex(capture gwtesting.SessionCapture, eventType string, occurrence int) int {
	return plainSpeechRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == eventType
	}, occurrence)
}

func turnStartPayloadWithResponseID(payload []byte, id string) []byte {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return payload
	}
	if response, ok := value["response"].(map[string]any); ok {
		response["id"] = id
	} else {
		value["response_id"] = id
	}
	mutated, err := json.Marshal(value)
	if err != nil {
		return payload
	}
	return mutated
}

func moveTurnStartRecordAfter(capture *gwtesting.SessionCapture, match, after func(gwtesting.CapturedSessionEvent) bool) bool {
	movedIndex := plainSpeechRecordIndex(*capture, match, 0)
	if movedIndex < 0 {
		return false
	}
	moved := capture.Records[movedIndex]
	capture.Records = append(capture.Records[:movedIndex], capture.Records[movedIndex+1:]...)
	renumberPlainSpeechCapture(capture)
	afterIndex := plainSpeechRecordIndex(*capture, after, 0)
	if afterIndex < 0 {
		return false
	}
	insertPlainSpeechRecordAfter(capture, afterIndex, moved)
	return true
}

func TestS2SLiveBargeInTurnStartCollisionMatrix(t *testing.T) {
	run := runTurnStartPlainSpeechCLI(t)
	if run.err != nil {
		dialCount, responses, protocolErrs := run.server.snapshot()
		t.Fatalf("turn-start CLI returned %v; dial_count=%d responses=%v protocol_errors=%v stream=%v", run.err, dialCount, responses, protocolErrs, run.trace.snapshot())
	}
	if err := validateTurnStartPlainSpeechCapture(run.capture); err != nil {
		t.Fatalf("turn-start identity-aware ledger failed: %v; stream=%v", err, run.trace.snapshot())
	}

	firstCreated := turnStartResponseRecordIndex(run.capture, rtEventResponseCreated, rtPlainResponseID, 0)
	firstOutput := turnStartResponseRecordIndex(run.capture, rtEventOutputAudioDelta, rtPlainResponseID, 0)
	firstCancel := turnStartClientRecordIndex(run.capture, rtEventResponseCancel, 0)
	secondAppend := turnStartClientRecordIndex(run.capture, rtEventInputAudioAppend, 1)
	firstTerminal := turnStartResponseRecordIndex(run.capture, rtEventResponseDone, rtPlainResponseID, 0)
	secondTerminal := turnStartResponseRecordIndex(run.capture, rtEventResponseDone, "response-plain-2", 0)
	thirdAppend := turnStartClientRecordIndex(run.capture, rtEventInputAudioAppend, 2)
	thirdCreated := turnStartResponseRecordIndex(run.capture, rtEventResponseCreated, "response-plain-3", 0)
	secondCancel := turnStartClientRecordIndex(run.capture, rtEventResponseCancel, 1)
	if firstCreated < 0 || firstCancel < 0 || secondAppend < 0 || firstTerminal < 0 || secondTerminal < 0 || thirdAppend < 0 || thirdCreated < 0 {
		t.Fatalf("turn-start boundaries are incomplete: created=%d first_output=%d cancel=%d second_append=%d first_terminal=%d second_terminal=%d third_append=%d third_created=%d records=%v", firstCreated, firstOutput, firstCancel, secondAppend, firstTerminal, secondTerminal, thirdAppend, thirdCreated, run.capture.Records)
	}
	if firstOutput >= 0 {
		t.Fatalf("response 1 leaked first output before its turn-start cancellation: output=%d records=%v", firstOutput, run.capture.Records)
	}
	if secondCancel >= 0 {
		t.Fatalf("completion-winning response 2 was cancelled at sequence %d; records=%v", secondCancel, run.capture.Records)
	}
	if !strictlyIncreasing(firstCreated, firstCancel, secondAppend, firstTerminal) {
		t.Fatalf("input-winning turn-start order = created:%d cancel:%d append:%d terminal:%d", firstCreated, firstCancel, secondAppend, firstTerminal)
	}
	if !strictlyIncreasing(secondTerminal, thirdAppend, thirdCreated) {
		t.Fatalf("completion-winning turn-start order = second_terminal:%d third_append:%d third_created:%d", secondTerminal, thirdAppend, thirdCreated)
	}

	dialCount, responses, protocolErrs := run.server.snapshot()
	if dialCount != 1 || len(responses) != 3 || len(protocolErrs) != 0 {
		t.Fatalf("turn-start provider observations = dials:%d responses:%d protocol_errors:%v; want one session, three responses, and no protocol errors", dialCount, len(responses), protocolErrs)
	}
	for index, response := range responses {
		wantCancel := 0
		if index == 0 {
			wantCancel = 1
		}
		if response.CancelCount != wantCancel || !response.TerminalSent {
			t.Fatalf("provider response %q = cancel:%d terminal:%t, want cancel:%d and terminal", response.ID, response.CancelCount, response.TerminalSent, wantCancel)
		}
	}
}

func TestS2SLiveBargeInTurnStartOracleRejectsNamedMutations(t *testing.T) {
	run := runTurnStartPlainSpeechCLI(t)
	if run.err != nil {
		t.Fatalf("build positive turn-start capture for negative controls: %v", run.err)
	}
	cases := []struct {
		name   string
		mutate func(*gwtesting.SessionCapture) bool
		want   string
	}{
		{
			name: "missing cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				return removeTurnStartRecord(capture, turnStartClientRecordIndex(*capture, rtEventResponseCancel, 0))
			},
			want: `response "response-1" was marked cancelled without a cancellation event`,
		},
		{
			name: "late cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				return moveTurnStartRecordAfter(capture,
					func(record gwtesting.CapturedSessionEvent) bool {
						return record.Direction == gwtesting.DirectionClientToServer && record.Type == rtEventResponseCancel
					},
					func(record gwtesting.CapturedSessionEvent) bool {
						return record.Direction == gwtesting.DirectionServerToClient && record.Type == rtEventResponseDone && plainSpeechRecordResponseID(record) == rtPlainResponseID
					})
			},
			want: "cancellation references unknown response",
		},
		{
			name: "duplicate cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				index := turnStartClientRecordIndex(*capture, rtEventResponseCancel, 0)
				if index < 0 {
					return false
				}
				insertPlainSpeechRecordAfter(capture, index, capture.Records[index])
				return true
			},
			want: `response "response-1" received duplicate cancellation`,
		},
		{
			name:   "first output leakage",
			mutate: leakTurnStartReplacementOutput,
			want:   `response "response-1" emitted 1 non-empty output events although output is forbidden`,
		},
		{
			name: "completion assigned to wrong turn",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				index := turnStartResponseRecordIndex(*capture, rtEventResponseDone, "response-plain-2", 0)
				if index < 0 {
					return false
				}
				capture.Records[index].Payload = turnStartPayloadWithResponseID(plainSpeechRecordPayload(capture.Records[index]), rtPlainResponseID)
				capture.Records[index].Data = nil
				return true
			},
			want: `response "response-1" received duplicate terminal disposition`,
		},
		{
			name: "dropped replacement",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				before := len(capture.Records)
				removePlainSpeechRecords(capture, func(record gwtesting.CapturedSessionEvent) bool {
					return plainSpeechRecordResponseID(record) == "response-plain-2"
				})
				return len(capture.Records) != before
			},
			want: `missing response "response-3"`,
		},
		{
			name: "clean unresolved close",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				return removeTurnStartRecord(capture, turnStartResponseRecordIndex(*capture, rtEventResponseDone, "response-plain-3", 0))
			},
			want: `response "response-3" has unresolved terminal disposition`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := clonePlainSpeechCapture(run.capture)
			if !testCase.mutate(&capture) {
				t.Fatal("negative control did not find its target event")
			}
			err := validateTurnStartPlainSpeechCapture(capture)
			if err == nil {
				t.Fatal("negative control unexpectedly passed the turn-start ledger")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("negative-control error = %v, want detail %q", err, testCase.want)
			}
		})
	}
}

func (a *plainSpeechCaptureAdapter) observeResponseCreated(payload []byte) {
	a.responseOrdinal++
	providerID := plainSpeechJSONField(payload, "response.id", "response_id")
	stableID := plainSpeechResponseID(a.responseOrdinal)
	owner := a.lastCommittedInput
	identity := plainSpeechResponseIdentity{
		stable:  stableID,
		inputID: owner,
		turnID:  plainSpeechTurnID(owner),
		ordinal: a.responseOrdinal,
	}
	a.providerResponses[providerID] = identity
	a.responseByProvider[providerID] = stableID
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:   a.nextEventSequence(),
		Kind:       probe.BargeInEventResponseCreated,
		InputID:    owner,
		TurnID:     identity.turnID,
		ResponseID: stableID,
	})
	if a.responseOrdinal > 1 && stableID != a.omitContinuationFor {
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventContinuation,
			InputID:    owner,
			TurnID:     identity.turnID,
			ResponseID: stableID,
		})
	}
}

// decodeObservedAudio decodes a captured base64 audio field. Malformed audio
// is observed as an empty frame, which the barge-in ledger rejects as a
// non-audible append or output.
func decodeObservedAudio(field string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(field)
	if err != nil {
		return nil
	}
	return decoded
}

// dropPlainSpeechRecord removes the occurrence-th record of eventType in the
// given direction, when present, and renumbers the capture.
func dropPlainSpeechRecord(capture *gwtesting.SessionCapture, direction gwtesting.SessionEventDirection, eventType string, occurrence int) {
	index := plainSpeechRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == direction && record.Type == eventType
	}, occurrence)
	if index >= 0 {
		capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
		renumberPlainSpeechCapture(capture)
	}
}

// misattributePlainSpeechTerminal rewrites the second response.done so it
// names the first response, producing a duplicate terminal disposition.
func misattributePlainSpeechTerminal(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	index := plainSpeechRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == rtEventResponseDone
	}, 1)
	if index < 0 {
		return
	}
	payload := map[string]any{}
	if json.Unmarshal(plainSpeechRecordPayload(capture.Records[index]), &payload) != nil {
		return
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		response = map[string]any{}
		payload["response"] = response
	}
	response["id"] = rtPlainResponseID
	capture.Records[index].Payload = mustJSON(t, payload)
}

// removeTurnStartRecord deletes the record at index and renumbers the
// capture; it reports false when the negative control found no target.
func removeTurnStartRecord(capture *gwtesting.SessionCapture, index int) bool {
	if index < 0 {
		return false
	}
	capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
	renumberPlainSpeechCapture(capture)
	return true
}

// leakTurnStartReplacementOutput relabels the replacement's first audio as
// output of the cancelled first response, directly after that response began.
func leakTurnStartReplacementOutput(capture *gwtesting.SessionCapture) bool {
	outputIndex := turnStartResponseRecordIndex(*capture, rtEventOutputAudioDelta, "response-plain-2", 0)
	createdIndex := turnStartResponseRecordIndex(*capture, rtEventResponseCreated, rtPlainResponseID, 0)
	if outputIndex < 0 || createdIndex < 0 {
		return false
	}
	leaked := capture.Records[outputIndex]
	leaked.Payload = turnStartPayloadWithResponseID(plainSpeechRecordPayload(leaked), rtPlainResponseID)
	leaked.Data = nil
	insertPlainSpeechRecordAfter(capture, createdIndex, leaked)
	return true
}

func (a *toolBargeInCaptureAdapter) observeUserTurnAck(record gwtesting.CapturedSessionEvent, payload []byte) {
	if record.Direction != gwtesting.DirectionServerToClient || plainSpeechJSONField(payload, "item.role") != rtRoleUser {
		return
	}
	inputIndex := a.nextUserTurnInputIndex()
	if inputIndex < 0 {
		// Keep an explicit malformed observation so a duplicate or orphan
		// acknowledgement fails the ledger instead of being silently ignored.
		a.ledger.Observe(probe.BargeInEvent{
			Sequence: a.nextEventSequence(),
			Kind:     probe.BargeInEventUserTurn,
		})
		return
	}
	if !a.inputs[inputIndex].committed {
		// A recording transport can observe the provider acknowledgement
		// before the successful client commit has been appended to the
		// capture. Retain the stable FIFO identity and emit the normalized
		// user-turn event immediately after its commit boundary.
		a.inputs[inputIndex].userTurnPending = true
		return
	}
	a.emitUserTurn(inputIndex)
}

func (a *toolBargeInCaptureAdapter) observeResponseCreated(record gwtesting.CapturedSessionEvent, payload []byte) {
	if record.Direction != gwtesting.DirectionServerToClient {
		return
	}
	a.responseOrdinal++
	providerID := plainSpeechJSONField(payload, "response.id", "response_id")
	stableID := toolBargeInResponseID(a.responseOrdinal)
	owner := a.lastCommittedInput
	identity := toolBargeInResponseIdentity{
		stable:  stableID,
		inputID: owner,
		turnID:  plainSpeechTurnID(owner),
		ordinal: a.responseOrdinal,
	}
	a.providerResponses[providerID] = identity
	a.responseByProvider[providerID] = stableID
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:   a.nextEventSequence(),
		Kind:       probe.BargeInEventResponseCreated,
		InputID:    owner,
		TurnID:     identity.turnID,
		ResponseID: stableID,
	})
	if a.responseOrdinal > 1 {
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventContinuation,
			InputID:    owner,
			TurnID:     identity.turnID,
			ResponseID: stableID,
		})
	}
}

// observeInputAppendLocked validates one client audio append and returns the
// server events it triggers. The caller holds s.mu.
func (s *toolBargeInServer) observeInputAppendLocked(audio string) []string {
	var events []string
	decoded, err := base64.StdEncoding.DecodeString(audio)
	if audio == "" || err != nil || len(decoded) == 0 {
		s.protocolErrs = append(s.protocolErrs, "input_audio_buffer.append was not non-empty base64 audio")
		return events
	}
	if !s.turnHasAudio {
		s.turnHasAudio = true
		events = append(events, `{"type":"input_audio_buffer.speech_started"}`)
	}
	if s.toolOutstanding && s.toolResultCount == 0 {
		s.speechWhileToolOutstanding = true
		s.recordMilestoneLocked("speech_overlap")
		s.speechOverlapOnce.Do(func() { close(s.speechSignalCh) })
	}
	return events
}

// createResponseLocked starts the next scripted response and returns its
// server events. The caller holds s.mu.
func (s *toolBargeInServer) createResponseLocked() []string {
	var events []string
	ordinal := len(s.responses) + 1
	response := &toolBargeInServerResponse{ID: toolBargeInResponseID(ordinal)}
	s.responses = append(s.responses, response)
	s.active = response
	events = append(events, fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, response.ID))
	switch ordinal {
	case 1:
		events = append(events,
			plainSpeechAudioDelta(response.ID, 51),
			fmt.Sprintf(`{"type":"response.output_audio.done","response_id":%q}`, response.ID),
			fmt.Sprintf(`{"type":"response.output_item.added","response_id":%q,"item":{"id":"item-tool-call","type":"function_call","call_id":%q,"name":%q}}`, response.ID, toolBargeInCallID, toolBargeInToolName),
			fmt.Sprintf(`{"type":"response.function_call_arguments.done","response_id":%q,"call_id":%q,"name":%q,"arguments":%q}`, response.ID, toolBargeInCallID, toolBargeInToolName, toolBargeInToolArguments),
			fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed"}}`, response.ID),
		)
		response.TerminalSent = true
		s.active = nil
		s.toolOutstanding = true
		s.recordMilestoneLocked("tool_call_issued")
	case 2:
		if s.toolResultCount != 1 {
			s.protocolErrs = append(s.protocolErrs, "continuation response created without exactly one tool result")
		}
		s.recordMilestoneLocked("continuation_issued")
		events = append(events,
			plainSpeechAudioDelta(response.ID, 52),
			fmt.Sprintf(`{"type":"response.output_audio.done","response_id":%q}`, response.ID),
			fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed"}}`, response.ID),
		)
		response.TerminalSent = true
		s.active = nil
	case 3:
		s.recordMilestoneLocked("later_response_issued")
		events = append(events,
			plainSpeechAudioDelta(response.ID, 53),
			fmt.Sprintf(`{"type":"response.output_audio.done","response_id":%q}`, response.ID),
			fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed"}}`, response.ID),
		)
		response.TerminalSent = true
		s.active = nil
	default:
		s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("unexpected response ordinal %d", ordinal))
	}
	return events
}

func (a *toolBargeInCaptureAdapter) observeToolCall(record gwtesting.CapturedSessionEvent, payload []byte) {
	if record.Direction != gwtesting.DirectionServerToClient || plainSpeechJSONField(payload, "item.type") != rtItemFunctionCall {
		return
	}
	providerResponseID := plainSpeechJSONField(payload, "response_id", "response.id")
	stableResponseID := a.responseByProvider[providerResponseID]
	identity := a.providerResponses[providerResponseID]
	callID := plainSpeechJSONField(payload, "item.call_id", "item.id")
	a.tools[callID] = toolBargeInToolIdentity{responseID: stableResponseID, turnID: identity.turnID}
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:   a.nextEventSequence(),
		Kind:       probe.BargeInEventToolCall,
		ResponseID: stableResponseID,
		TurnID:     identity.turnID,
		ToolCallID: callID,
	})
}

func (a *toolBargeInCaptureAdapter) observeToolResult(record gwtesting.CapturedSessionEvent, payload []byte) {
	if record.Direction != gwtesting.DirectionClientToServer || plainSpeechJSONField(payload, "item.type") != rtItemFunctionCallOutput {
		return
	}
	callID := plainSpeechJSONField(payload, "item.call_id")
	tool := a.tools[callID]
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:    a.nextEventSequence(),
		Kind:        probe.BargeInEventToolResult,
		ResponseID:  tool.responseID,
		TurnID:      tool.turnID,
		ToolCallID:  callID,
		Disposition: probe.BargeInDispositionDelivered,
	})
}
