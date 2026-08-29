package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	case "input_audio_buffer.append":
		if record.Direction != gwtesting.DirectionClientToServer {
			return
		}
		inputID := a.activeInputID()
		decoded, _ := base64.StdEncoding.DecodeString(plainSpeechJSONField(payload, "audio"))
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:      a.nextEventSequence(),
			Kind:          probe.BargeInEventInputAppend,
			InputID:       inputID,
			TurnID:        plainSpeechTurnID(inputID),
			AppendGroupID: inputID,
			Bytes:         len(decoded),
			NonEmpty:      len(decoded) > 0,
		})
	case "input_audio_buffer.commit":
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
		if record.Direction != gwtesting.DirectionServerToClient || plainSpeechJSONField(payload, "item.role") != "user" {
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
	case "response.created":
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
	case "response.output_audio.delta":
		if record.Direction != gwtesting.DirectionServerToClient {
			return
		}
		providerID := plainSpeechJSONField(payload, "response_id", "response.id")
		stableID := a.responseByProvider[providerID]
		decoded, _ := base64.StdEncoding.DecodeString(plainSpeechJSONField(payload, "delta"))
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseOutput,
			ResponseID: stableID,
			Bytes:      len(decoded),
			NonEmpty:   len(decoded) > 0,
		})
	case "response.output_item.added":
		if record.Direction != gwtesting.DirectionServerToClient || plainSpeechJSONField(payload, "item.type") != "function_call" {
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
	case "response.cancel":
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
	case "response.done":
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
	case "conversation.item.create":
		if record.Direction != gwtesting.DirectionClientToServer || plainSpeechJSONField(payload, "item.type") != "function_call_output" {
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
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == "conversation.item.create" && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == "function_call_output"
	}, occurrence)
}

func toolBargeInResponseDoneWithStatus(capture *gwtesting.SessionCapture, responseID, status string) bool {
	index := toolBargeInResponseRecordIndex(*capture, "response.done", responseID)
	if index < 0 {
		return false
	}
	value := map[string]any{}
	if json.Unmarshal(plainSpeechRecordPayload(capture.Records[index]), &value) != nil {
		return false
	}
	response, _ := value["response"].(map[string]any)
	if response == nil {
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
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.function_call_arguments.done" && plainSpeechRecordResponseID(record) == toolBargeInResponseOne
	}, 0)
	if callIndex < 0 {
		return false
	}
	cancel := gwtesting.CapturedSessionEvent{
		Direction:   gwtesting.DirectionClientToServer,
		Type:        "response.cancel",
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
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.output_item.added" && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == "function_call"
	}, 0)
	overlapAppendIndex := toolBargeInClientRecordIndex(run.capture, "input_audio_buffer.append", 1)
	firstTerminalIndex := toolBargeInResponseRecordIndex(run.capture, "response.done", toolBargeInResponseOne)
	if toolCallIndex < 0 || overlapAppendIndex < 0 || firstTerminalIndex < 0 || !(toolCallIndex < firstTerminalIndex && firstTerminalIndex < overlapAppendIndex) {
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
					return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.done" && plainSpeechRecordResponseID(record) == toolBargeInResponseThree
				})
			},
			want: `response "response-tool-barge-in-3" has unresolved terminal disposition`,
		},
		{
			name: "lost result",
			mutate: func(capture *gwtesting.SessionCapture) {
				removePlainSpeechRecords(capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionClientToServer && record.Type == "conversation.item.create" && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == "function_call_output"
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
				item, _ := value["item"].(map[string]any)
				item["call_id"] = "call-orphaned"
				encoded, _ := json.Marshal(value)
				capture.Records[index].Payload = encoded
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
				toolBargeInResponseDoneWithStatus(capture, toolBargeInResponseOne, "cancelled")
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

func TestS2SLiveBargeInOutstandingToolCallWaitIsBounded(t *testing.T) {
	ledger := probe.NewBargeInLedger()
	ledger.Observe(probe.BargeInEvent{
		Sequence: 1, Kind: probe.BargeInEventInputAppend,
		InputID: "input-tool-wait", TurnID: "turn-tool-wait", AppendGroupID: "input-tool-wait",
		Bytes: 2, NonEmpty: true,
	})
	start := time.Now()
	err := ledger.WaitFor(context.Background(), "named tool continuation", make(chan struct{}), 20*time.Millisecond)
	if err == nil {
		t.Fatal("missing named-tool continuation gate unexpectedly passed")
	}
	var waitErr *probe.BargeInWaitError
	if !errors.As(err, &waitErr) || !errors.Is(err, probe.ErrBargeInWait) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want bounded barge-in wait with deadline identity", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("named-tool continuation gate took %s, want a bounded return", elapsed)
	}
	for _, want := range []string{"named tool continuation", "1:input.append", "input-tool-wait:commit", "session:terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("wait error = %v, want diagnostic %q", err, want)
		}
	}
}
