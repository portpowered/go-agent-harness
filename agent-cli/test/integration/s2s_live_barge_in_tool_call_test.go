package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func toolBargeInResponseID(ordinal int) string {
	switch ordinal {
	case 1:
		return toolBargeInResponseOne
	case 2:
		return toolBargeInResponseTwo
	case 3:
		return toolBargeInResponseThree
	default:
		return fmt.Sprintf("response-tool-barge-in-%d", ordinal)
	}
}

func toolBargeInContract() probe.BargeInContract {
	return probe.BargeInContract{
		Inputs: []probe.BargeInInputExpectation{
			{ID: "input-1", TurnID: "turn-1"},
			{ID: "input-2", TurnID: "turn-2"},
		},
		Responses: []probe.BargeInResponseExpectation{
			{
				ID: toolBargeInResponseOne, InputID: "input-1", TurnID: "turn-1",
				Disposition: probe.BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true,
			},
			{
				ID: toolBargeInResponseTwo, InputID: "input-1", TurnID: "turn-1",
				Disposition: probe.BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true, RequireContinuation: true,
			},
			{
				ID: toolBargeInResponseThree, InputID: "input-2", TurnID: "turn-2",
				Disposition: probe.BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true, RequireContinuation: true,
			},
		},
		Tools: []probe.BargeInToolExpectation{
			{
				ID: toolBargeInCallID, ResponseID: toolBargeInResponseOne, TurnID: "turn-1",
				Disposition: probe.BargeInDispositionDelivered, ForbidResultAfterCancel: true,
			},
		},
		RequireSessionTerminal: true,
	}
}

type toolBargeInResponseIdentity struct {
	stable   string
	inputID  string
	turnID   string
	ordinal  int
	terminal bool
}

type toolBargeInToolIdentity struct {
	responseID string
	turnID     string
}

type toolBargeInInputIdentity struct {
	id               string
	committed        bool
	userTurnPending  bool
	userTurnObserved bool
}

// normalizeToolBargeInCapture translates raw OpenAI records at the adapter
// boundary. The ledger sees only stable identities; provider response and call
// IDs are used for lookup and never appear in oracle diagnostics.
type toolBargeInCaptureAdapter struct {
	ledger *probe.BargeInLedger

	nextSequence       int
	inputOrdinal       int
	inputs             []toolBargeInInputIdentity
	lastCommittedInput string
	responseOrdinal    int
	providerResponses  map[string]toolBargeInResponseIdentity
	responseByProvider map[string]string
	tools              map[string]toolBargeInToolIdentity
}

func normalizeToolBargeInCapture(capture gwtesting.SessionCapture) *probe.BargeInLedger {
	adapter := toolBargeInCaptureAdapter{
		ledger:             probe.NewBargeInLedger(),
		providerResponses:  make(map[string]toolBargeInResponseIdentity),
		responseByProvider: make(map[string]string),
		tools:              make(map[string]toolBargeInToolIdentity),
	}
	for _, record := range capture.Records {
		adapter.observe(record)
	}
	adapter.ledger.Observe(probe.BargeInEvent{
		Sequence:    adapter.nextEventSequence(),
		Kind:        probe.BargeInEventSessionTerminal,
		Disposition: probe.BargeInDispositionClean,
		Clean:       true,
	})
	return adapter.ledger
}

func (a *toolBargeInCaptureAdapter) nextEventSequence() int {
	a.nextSequence++
	return a.nextSequence
}

func (a *toolBargeInCaptureAdapter) activeInputID() string {
	if len(a.inputs) == 0 || a.inputs[len(a.inputs)-1].committed {
		a.inputOrdinal++
		a.inputs = append(a.inputs, toolBargeInInputIdentity{
			id: plainSpeechInputID(a.inputOrdinal),
		})
	}
	return a.inputs[len(a.inputs)-1].id
}

func (a *toolBargeInCaptureAdapter) latestUncommittedInputIndex() int {
	for index := len(a.inputs) - 1; index >= 0; index-- {
		if !a.inputs[index].committed {
			return index
		}
	}
	return -1
}

func (a *toolBargeInCaptureAdapter) nextUserTurnInputIndex() int {
	for index := range a.inputs {
		if !a.inputs[index].userTurnObserved && !a.inputs[index].userTurnPending {
			return index
		}
	}
	for index := range a.inputs {
		if a.inputs[index].userTurnPending && !a.inputs[index].userTurnObserved {
			return index
		}
	}
	return -1
}

func (a *toolBargeInCaptureAdapter) emitUserTurn(index int) {
	if index < 0 || index >= len(a.inputs) || a.inputs[index].userTurnObserved {
		return
	}
	inputID := a.inputs[index].id
	a.inputs[index].userTurnObserved = true
	a.inputs[index].userTurnPending = false
	a.ledger.Observe(probe.BargeInEvent{
		Sequence: a.nextEventSequence(),
		Kind:     probe.BargeInEventUserTurn,
		InputID:  inputID,
		TurnID:   plainSpeechTurnID(inputID),
	})
}

func (a *toolBargeInCaptureAdapter) flushPendingUserTurns() {
	for index := range a.inputs {
		if a.inputs[index].committed && a.inputs[index].userTurnPending {
			a.emitUserTurn(index)
		}
	}
}

func (a *toolBargeInCaptureAdapter) observe(record gwtesting.CapturedSessionEvent) {
	payload := plainSpeechRecordPayload(record)
	switch record.Type {
	case rtEventInputAudioAppend:
		if record.Direction != gwtesting.DirectionClientToServer {
			return
		}
		inputID := a.activeInputID()
		decoded := decodeObservedAudio(plainSpeechJSONField(payload, "audio"))
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:      a.nextEventSequence(),
			Kind:          probe.BargeInEventInputAppend,
			InputID:       inputID,
			TurnID:        plainSpeechTurnID(inputID),
			AppendGroupID: inputID,
			Bytes:         len(decoded),
			NonEmpty:      len(decoded) > 0,
		})
	case rtEventInputAudioCommit:
		if record.Direction != gwtesting.DirectionClientToServer {
			return
		}
		inputIndex := a.latestUncommittedInputIndex()
		inputID := ""
		if inputIndex >= 0 {
			inputID = a.inputs[inputIndex].id
			a.inputs[inputIndex].committed = true
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence: a.nextEventSequence(),
			Kind:     probe.BargeInEventInputCommit,
			InputID:  inputID,
			TurnID:   plainSpeechTurnID(inputID),
		})
		a.lastCommittedInput = inputID
		a.flushPendingUserTurns()
	case "conversation.item.created":
		a.observeUserTurnAck(record, payload)
	case rtEventResponseCreated:
		a.observeResponseCreated(record, payload)
	case rtEventOutputAudioDelta:
		if record.Direction != gwtesting.DirectionServerToClient {
			return
		}
		providerID := plainSpeechJSONField(payload, "response_id", "response.id")
		stableID := a.responseByProvider[providerID]
		decoded := decodeObservedAudio(plainSpeechJSONField(payload, "delta"))
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseOutput,
			ResponseID: stableID,
			Bytes:      len(decoded),
			NonEmpty:   len(decoded) > 0,
		})
	case rtEventOutputItemAdded:
		a.observeToolCall(record, payload)
	case rtEventResponseCancel:
		if record.Direction != gwtesting.DirectionClientToServer {
			return
		}
		identity := a.activeResponse()
		interruptingInput := ""
		if identity.ordinal > 0 {
			interruptingInput = plainSpeechInputID(identity.ordinal + 1)
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseCancel,
			InputID:    interruptingInput,
			TurnID:     plainSpeechTurnID(interruptingInput),
			ResponseID: identity.stable,
		})
	case rtEventResponseDone:
		if record.Direction != gwtesting.DirectionServerToClient {
			return
		}
		providerID := plainSpeechJSONField(payload, "response.id", "response_id")
		identity := a.providerResponses[providerID]
		stableID := a.responseByProvider[providerID]
		status := plainSpeechJSONField(payload, "response.status", "status")
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:    a.nextEventSequence(),
			Kind:        probe.BargeInEventResponseTerminal,
			ResponseID:  stableID,
			Disposition: plainSpeechDisposition(status),
			Reason:      status,
		})
		identity.terminal = true
		a.providerResponses[providerID] = identity
	case rtEventConversationItemCreate:
		a.observeToolResult(record, payload)
	}
}

func (a *toolBargeInCaptureAdapter) activeResponse() toolBargeInResponseIdentity {
	for ordinal := a.responseOrdinal; ordinal > 0; ordinal-- {
		for _, identity := range a.providerResponses {
			if identity.ordinal == ordinal && !identity.terminal {
				return identity
			}
		}
	}
	return toolBargeInResponseIdentity{}
}

func validateToolBargeInCapture(capture gwtesting.SessionCapture) error {
	return normalizeToolBargeInCapture(capture).Validate(toolBargeInContract())
}

func toolBargeInRecordIndex(capture gwtesting.SessionCapture, match func(gwtesting.CapturedSessionEvent) bool, occurrence int) int {
	return plainSpeechRecordIndex(capture, match, occurrence)
}

func toolBargeInClientRecordIndex(capture gwtesting.SessionCapture, eventType string, occurrence int) int {
	return toolBargeInRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == eventType
	}, occurrence)
}

func toolBargeInResponseRecordIndex(capture gwtesting.SessionCapture, eventType, responseID string) int {
	return toolBargeInRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == eventType && plainSpeechRecordResponseID(record) == responseID
	}, 0)
}

func toolBargeInResultRecordIndex(capture gwtesting.SessionCapture, occurrence int) int {
	return toolBargeInRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == rtEventConversationItemCreate && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == rtItemFunctionCallOutput
	}, occurrence)
}

func toolBargeInResponseDoneWithStatus(capture *gwtesting.SessionCapture, responseID, status string) bool {
	index := toolBargeInResponseRecordIndex(*capture, rtEventResponseDone, responseID)
	if index < 0 {
		return false
	}
	value := map[string]any{}
	if json.Unmarshal(plainSpeechRecordPayload(capture.Records[index]), &value) != nil {
		return false
	}
	response, ok := value["response"].(map[string]any)
	if !ok {
		response = map[string]any{}
		value["response"] = response
	}
	response["status"] = status
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	capture.Records[index].Payload = encoded
	return true
}

func toolBargeInInsertCancellationBeforeTerminal(capture *gwtesting.SessionCapture) bool {
	callIndex := toolBargeInRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == rtEventFunctionCallArgumentsDone && plainSpeechRecordResponseID(record) == toolBargeInResponseOne
	}, 0)
	if callIndex < 0 {
		return false
	}
	cancel := gwtesting.CapturedSessionEvent{
		Direction:   gwtesting.DirectionClientToServer,
		Type:        rtEventResponseCancel,
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"response.cancel"}`),
	}
	insertPlainSpeechRecordAfter(capture, callIndex, cancel)
	return true
}

func countToolBargeInAudio(events []toolBargeInStreamEvent, ordinal int) int {
	count := 0
	for _, event := range events {
		if event.Ordinal == ordinal && event.Type == messages.StreamTypeAudioDelta && event.Bytes > 0 {
			count++
		}
	}
	return count
}

func toolBargeInMilestoneOrder(milestones []string, names ...string) error {
	positions := make(map[string]int, len(milestones))
	for index, milestone := range milestones {
		if _, exists := positions[milestone]; !exists {
			positions[milestone] = index
		}
	}
	for index, name := range names {
		position, exists := positions[name]
		if !exists {
			return fmt.Errorf("provider milestone %q was not observed: %v", name, milestones)
		}
		if index > 0 {
			previous := positions[names[index-1]]
			if previous >= position {
				return fmt.Errorf("provider milestone order = %v, want %q before %q", milestones, names[index-1], name)
			}
		}
	}
	return nil
}

func TestS2SLiveBargeInOutstandingToolCallThroughCLI(t *testing.T) {
	run := runToolBargeInCLI(t)
	if run.err != nil {
		dialCount, responses, resultCount, overlap, pending, protocolErrs, milestones := run.server.snapshot()
		t.Fatalf("tool-call barge-in CLI returned %v; dials=%d responses=%v result_count=%d overlap=%t close_pending=%t protocol_errors=%v milestones=%v stream=%v", run.err, dialCount, responses, resultCount, overlap, pending, protocolErrs, milestones, run.trace.snapshot())
	}
	if err := validateToolBargeInCapture(run.capture); err != nil {
		t.Fatalf("tool-call barge-in identity-aware ledger failed: %v; stream=%v", err, run.trace.snapshot())
	}

	calls, returned := run.executor.snapshot()
	if len(calls) != 1 || calls[0].ID != toolBargeInCallID || calls[0].Name != toolBargeInToolName || calls[0].Arguments != toolBargeInToolArguments {
		t.Fatalf("tool calls = %+v, want exactly one named correlated call", calls)
	}
	if len(returned) != 1 || returned[0].ToolCallID != toolBargeInCallID || returned[0].Content != toolBargeInToolResult {
		t.Fatalf("tool results = %+v, want exactly one correlated sentinel result", returned)
	}

	dialCount, responses, resultCount, overlap, pending, protocolErrs, milestones := run.server.snapshot()
	if dialCount != 1 || len(responses) != 3 || resultCount != 1 || !overlap || pending || len(protocolErrs) != 0 {
		t.Fatalf("provider observations = dials:%d responses:%d result_count:%d overlap:%t close_pending:%t protocol_errors:%v; want one session, three terminal responses, one result, overlap, and no pending close", dialCount, len(responses), resultCount, overlap, pending, protocolErrs)
	}
	for _, response := range responses {
		if response.CancelCount != 0 || !response.TerminalSent {
			t.Fatalf("provider response %q = cancel:%d terminal:%t, want completion without cancellation", response.ID, response.CancelCount, response.TerminalSent)
		}
	}
	if err := toolBargeInMilestoneOrder(milestones, "tool_call_issued", "speech_overlap", "tool_result_received", "continuation_issued", "later_input_committed", "later_response_issued", "transport_close"); err != nil {
		t.Fatal(err)
	}
	if countToolBargeInAudio(run.trace.snapshot(), 2) == 0 || countToolBargeInAudio(run.trace.snapshot(), 3) == 0 {
		t.Fatalf("spoken continuation/later response audio = %v, want non-empty output for responses 2 and 3", run.trace.snapshot())
	}

	toolCallIndex := toolBargeInRecordIndex(run.capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == rtEventOutputItemAdded && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == rtItemFunctionCall
	}, 0)
	overlapAppendIndex := toolBargeInClientRecordIndex(run.capture, rtEventInputAudioAppend, 1)
	firstTerminalIndex := toolBargeInResponseRecordIndex(run.capture, rtEventResponseDone, toolBargeInResponseOne)
	if toolCallIndex < 0 || overlapAppendIndex < 0 || firstTerminalIndex < 0 || !strictlyIncreasing(toolCallIndex, firstTerminalIndex, overlapAppendIndex) {
		t.Fatalf("wire collision order = tool_call:%d first_terminal:%d overlap_append:%d; want tool call < owning terminal < non-empty interrupting speech", toolCallIndex, firstTerminalIndex, overlapAppendIndex)
	}
}

func TestS2SLiveBargeInOutstandingToolCallOracleRejectsMutations(t *testing.T) {
	run := runToolBargeInCLI(t)
	if run.err != nil {
		t.Fatalf("build positive tool-call capture: %v; stream=%v", run.err, run.trace.snapshot())
	}
	cases := []struct {
		name     string
		mutate   func(*gwtesting.SessionCapture)
		contract func() probe.BargeInContract
		want     string
	}{
		{
			name: "premature clean close",
			mutate: func(capture *gwtesting.SessionCapture) {
				removePlainSpeechRecords(capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionServerToClient && record.Type == rtEventResponseDone && plainSpeechRecordResponseID(record) == toolBargeInResponseThree
				})
			},
			want: `response "response-tool-barge-in-3" has unresolved terminal disposition`,
		},
		{
			name: "lost result",
			mutate: func(capture *gwtesting.SessionCapture) {
				removePlainSpeechRecords(capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionClientToServer && record.Type == rtEventConversationItemCreate && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == rtItemFunctionCallOutput
				})
			},
			want: `tool call "call-tool-barge-in" has unresolved result disposition`,
		},
		{
			name: "orphan result ID",
			mutate: func(capture *gwtesting.SessionCapture) {
				index := toolBargeInResultRecordIndex(*capture, 0)
				if index < 0 {
					return
				}
				value := map[string]any{}
				if json.Unmarshal(plainSpeechRecordPayload(capture.Records[index]), &value) != nil {
					return
				}
				item := mustAs[map[string]any](t, value["item"])
				item["call_id"] = "call-orphaned"
				capture.Records[index].Payload = mustJSON(t, value)
			},
			want: `tool result references unknown call "call-orphaned"`,
		},
		{
			name: "duplicate delivery",
			mutate: func(capture *gwtesting.SessionCapture) {
				index := toolBargeInResultRecordIndex(*capture, 0)
				if index >= 0 {
					insertPlainSpeechRecordAfter(capture, index, capture.Records[index])
				}
			},
			want: `tool call "call-tool-barge-in" received duplicate result disposition`,
		},
		{
			name: "post-cancel delivery",
			mutate: func(capture *gwtesting.SessionCapture) {
				if !toolBargeInInsertCancellationBeforeTerminal(capture) {
					return
				}
				toolBargeInResponseDoneWithStatus(capture, toolBargeInResponseOne, rtStatusCancelled)
			},
			contract: func() probe.BargeInContract {
				contract := toolBargeInContract()
				contract.Responses[0].Disposition = probe.BargeInDispositionCancelled
				contract.Responses[0].RequireCancel = true
				contract.Responses[0].ForbidCancel = false
				return contract
			},
			want: `tool result for "call-tool-barge-in" was delivered after response cancellation`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := clonePlainSpeechCapture(run.capture)
			if testCase.mutate != nil {
				testCase.mutate(&capture)
			}
			contract := toolBargeInContract()
			if testCase.contract != nil {
				contract = testCase.contract()
			}
			err := normalizeToolBargeInCapture(capture).Validate(contract)
			if err == nil {
				t.Fatal("mutation unexpectedly passed the identity-aware ledger")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("mutation error = %v, want detail %q", err, testCase.want)
			}
		})
	}
}
