package integration

// Story 003 controls for the depth-5 tool-call conversation. These tests keep
// the shipped session composition and OpenAI websocket replay boundary intact,
// changing one recorded behavior at a time so a fluent answer or a clean
// transport shutdown cannot hide a broken causal link.

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	conversationWrongToolName      = "lookup_weather"
	conversationWrongToolArgs      = `{"city":"Paris"}`
	conversationOtherCallID        = "call_weather_other"
	conversationContradictoryReply = "The weather in Lisbon is 99 degrees with stormy skies."
)

// conversationFixtureInputs returns the committed input corpus and a voiced
// reply window. The reply audio is deliberately kept identical across controls
// so grounding controls change transcript content only.
func conversationFixtureInputs(t *testing.T) (wavPath string, reply []int16) {
	t.Helper()
	wavPath = toolSingleCallWAVPath(t)
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	return wavPath, loudestWindowSamplesIntegration(t, samples, toolSingleCallReplySamples)
}

// buildConversationControlFixture starts from the passing depth-5 capture and
// applies exactly one control mutation. The resulting capture is validated by
// the strict replay dialer before the session command sees it.
func buildConversationControlFixture(t *testing.T, mutate func(*gwtesting.SessionCapture)) (wavPath, wirePath string) {
	t.Helper()
	wavPath, reply := conversationFixtureInputs(t)
	basePath := buildToolResultConversationFixture(t, wavPath, reply, toolResultPositive, true)
	capture, err := gwtesting.LoadSessionCapture(basePath)
	if err != nil {
		t.Fatalf("load base conversation capture: %v", err)
	}
	mutate(&capture)
	for index := range capture.Records {
		capture.Records[index].Sequence = index + 1
	}
	wirePath = t.TempDir() + "/tool-result-conversation-control.session.json"
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal conversation control fixture: %v", err)
	}
	if err := os.WriteFile(wirePath, data, 0o600); err != nil {
		t.Fatalf("write conversation control fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(wirePath); err != nil {
		t.Fatalf("conversation control fixture rejected by replay dialer: %v", err)
	}
	return wavPath, wirePath
}

func conversationPayloadMap(t *testing.T, record *gwtesting.CapturedSessionEvent) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatalf("decode %s control payload: %v", record.Type, err)
	}
	return payload
}

func conversationItemMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	item, ok := payload["item"].(map[string]any)
	if !ok {
		t.Fatalf("conversation control payload has no object item: %#v", payload)
	}
	return item
}

func marshalConversationPayload(t *testing.T, record *gwtesting.CapturedSessionEvent, payload map[string]any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s control payload: %v", record.Type, err)
	}
	record.Payload = data
}

func mutateConversationCallIdentity(t *testing.T, capture *gwtesting.SessionCapture, name, args string) {
	t.Helper()
	seenAdded, seenArguments := false, false
	for index := range capture.Records {
		record := &capture.Records[index]
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.output_item.added":
			payload := conversationPayloadMap(t, record)
			item := conversationItemMap(t, payload)
			item["name"] = name
			marshalConversationPayload(t, record, payload)
			seenAdded = true
		case "response.function_call_arguments.done":
			payload := conversationPayloadMap(t, record)
			payload["name"] = name
			if args != "" {
				payload["arguments"] = args
			}
			marshalConversationPayload(t, record, payload)
			seenArguments = true
		}
	}
	if !seenAdded || !seenArguments {
		t.Fatalf("conversation call identity mutation saw added=%t arguments=%t; want both call records", seenAdded, seenArguments)
	}
}

func duplicateConversationCall(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	var added *gwtesting.CapturedSessionEvent
	var arguments *gwtesting.CapturedSessionEvent
	for index := range capture.Records {
		record := capture.Records[index]
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.output_item.added":
			copy := record
			added = &copy
		case "response.function_call_arguments.done":
			copy := record
			arguments = &copy
		}
	}
	if added == nil || arguments == nil {
		t.Fatalf("conversation duplicate-call mutation could not find the original call records")
	}
	var records []gwtesting.CapturedSessionEvent
	for _, record := range capture.Records {
		records = append(records, record)
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.function_call_arguments.done" {
			records = append(records, *added, *arguments)
		}
	}
	capture.Records = records
}

func mutateConversationTranscript(t *testing.T, capture *gwtesting.SessionCapture, transcript string) {
	t.Helper()
	seenDelta, seenDone := false, false
	for index := range capture.Records {
		record := &capture.Records[index]
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.output_audio_transcript.delta":
			payload := conversationPayloadMap(t, record)
			payload["delta"] = transcript
			marshalConversationPayload(t, record, payload)
			seenDelta = true
		case "response.output_audio_transcript.done":
			payload := conversationPayloadMap(t, record)
			payload["transcript"] = transcript
			marshalConversationPayload(t, record, payload)
			seenDone = true
		}
	}
	if !seenDelta || !seenDone {
		t.Fatalf("conversation transcript mutation saw delta=%t done=%t; want both transcript records", seenDelta, seenDone)
	}
}

func functionCallOutputRecord(t *testing.T, record *gwtesting.CapturedSessionEvent) bool {
	t.Helper()
	if record.Direction != gwtesting.DirectionClientToServer || record.Type != "conversation.item.create" {
		return false
	}
	payload := conversationPayloadMap(t, record)
	item := conversationItemMap(t, payload)
	return item["type"] == "function_call_output"
}

func mutateExpectedConversationResult(t *testing.T, capture *gwtesting.SessionCapture, callID, output string) {
	t.Helper()
	seen := false
	for index := range capture.Records {
		record := &capture.Records[index]
		if !functionCallOutputRecord(t, record) {
			continue
		}
		payload := conversationPayloadMap(t, record)
		item := conversationItemMap(t, payload)
		item["call_id"] = callID
		item["output"] = output
		marshalConversationPayload(t, record, payload)
		seen = true
	}
	if !seen {
		t.Fatalf("conversation result mutation found no expected function_call_output record")
	}
}

func removeExpectedConversationResult(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	removed := false
	for _, record := range capture.Records {
		if functionCallOutputRecord(t, &record) {
			removed = true
			continue
		}
		filtered = append(filtered, record)
	}
	if !removed {
		t.Fatalf("conversation missing-result mutation found no expected function_call_output record")
	}
	capture.Records = filtered
}

// removeConversationFollowUp leaves the first provider response and terminal
// close in place, but removes the authored answer that would otherwise race a
// missing result on the asynchronous websocket writer. The control therefore
// has one changed obligation: the provider never offers a follow-up answer
// without an accepted function_call_output.
func removeConversationFollowUp(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	firstResponseDone := false
	filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionServerToClient {
			filtered = append(filtered, record)
			continue
		}
		if record.Type == "response.done" {
			if firstResponseDone {
				continue
			}
			firstResponseDone = true
			filtered = append(filtered, record)
			continue
		}
		if firstResponseDone && record.Type != "session.closed" {
			continue
		}
		filtered = append(filtered, record)
	}
	if !firstResponseDone {
		t.Fatalf("conversation missing-result control could not find the first response.done boundary")
	}
	capture.Records = filtered
}

func duplicateExpectedConversationResult(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	var result *gwtesting.CapturedSessionEvent
	for index := range capture.Records {
		if functionCallOutputRecord(t, &capture.Records[index]) {
			copy := capture.Records[index]
			result = &copy
			break
		}
	}
	if result == nil {
		t.Fatalf("conversation duplicate-result mutation found no expected function_call_output record")
	}
	withDuplicate := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records)+1)
	for _, record := range capture.Records {
		withDuplicate = append(withDuplicate, record)
		if functionCallOutputRecord(t, &record) {
			withDuplicate = append(withDuplicate, *result)
		}
	}
	capture.Records = withDuplicate
}

func assertConversationOneCall(t *testing.T, executor *conversationResultExecutor) []messages.ToolCall {
	t.Helper()
	calls, returned := executor.snapshot()
	if len(calls) != 1 {
		t.Fatalf("control executor observed %d calls %v, want exactly one expected invocation", len(calls), calls)
	}
	if calls[0].ID != toolConversationCallID {
		t.Fatalf("control executor observed call ID %q, want originating ID %q", calls[0].ID, toolConversationCallID)
	}
	if len(returned) != 1 || returned[0] != toolResultPositive {
		t.Fatalf("control executor returned %q, want exactly the positive result %s once", returned, toolResultPositive)
	}
	return calls
}

func assertConversationResultGateFailure(t *testing.T, control string, runErr error, elapsed time.Duration) {
	t.Helper()
	if runErr == nil {
		t.Fatalf("%s control completed cleanly; the invalid provider-result pairing was accepted", control)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("%s control took %s; invalid result pairing must fail within the explicit 4s control bound: %v", control, elapsed, runErr)
	}
	errText := runErr.Error()
	if !strings.Contains(errText, "replay mismatch") || !strings.Contains(errText, "conversation.item.create") {
		t.Fatalf("%s control failed with %q, want strict conversation.item.create replay mismatch", control, errText)
	}
	if strings.Contains(errText, "context deadline exceeded") && !strings.Contains(errText, "replay mismatch") {
		t.Fatalf("%s control reached a deadline instead of the result gate: %q", control, errText)
	}
}

func assertConversationMissingResultFailure(t *testing.T, runErr error, elapsed time.Duration) {
	t.Helper()
	if runErr == nil {
		t.Fatal("missing-result control completed cleanly; the unresolved result was silently discarded")
	}
	if elapsed > 4*time.Second {
		t.Fatalf("missing-result control took %s; unresolved-result close handling must remain within the explicit 4s bound: %v", elapsed, runErr)
	}
	errText := runErr.Error()
	if !strings.Contains(errText, "tool results were not delivered") &&
		(!strings.Contains(errText, "replay mismatch") || !strings.Contains(errText, "conversation.item.create")) {
		t.Fatalf("missing-result control failed with %q, want an unresolved-result diagnostic or strict result-gate mismatch for %q", errText, toolConversationCallID)
	}
	if strings.Contains(errText, "context deadline exceeded") && !strings.Contains(errText, "replay mismatch") {
		t.Fatalf("missing-result control reached a deadline instead of reporting the unresolved call: %q", errText)
	}
}

// TestSessionToolCallConversationWrongToolNameIsRejected changes only the
// provider-issued tool name. The transport still accepts the paired result,
// so the failure is specifically the observed-vs-expected call identity.
func TestSessionToolCallConversationWrongToolNameIsRejected(t *testing.T) {
	wavPath, wirePath := buildConversationControlFixture(t, func(capture *gwtesting.SessionCapture) {
		mutateConversationCallIdentity(t, capture, conversationWrongToolName, "")
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	stdout, _, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	if runErr != nil {
		t.Fatalf("wrong-name control transport should complete so identity assertion isolates the call violation: %v\nstdout:\n%s", runErr, stdout)
	}
	calls := assertConversationOneCall(t, executor)
	identityErr := validateExactlyOneToolCall(calls)
	if identityErr == nil || !strings.Contains(identityErr.Error(), conversationWrongToolName) || !strings.Contains(identityErr.Error(), toolCallScenarioName) {
		t.Fatalf("wrong-name control produced identity error %v, want observed %q and expected %q", identityErr, conversationWrongToolName, toolCallScenarioName)
	}
	t.Logf("wrong-name control rejected as expected: %v", identityErr)
}

// TestSessionToolCallConversationWrongArgumentsAreRejected changes only the
// decoded provider arguments. The real executor still runs once and the
// paired result still crosses the provider gate, isolating argument identity.
func TestSessionToolCallConversationWrongArgumentsAreRejected(t *testing.T) {
	wavPath, wirePath := buildConversationControlFixture(t, func(capture *gwtesting.SessionCapture) {
		mutateConversationCallIdentity(t, capture, toolCallScenarioName, conversationWrongToolArgs)
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	stdout, _, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	if runErr != nil {
		t.Fatalf("wrong-arguments control transport should complete so argument assertion isolates the call violation: %v\nstdout:\n%s", runErr, stdout)
	}
	calls := assertConversationOneCall(t, executor)
	identityErr := validateExactlyOneToolCall(calls)
	if identityErr == nil || !strings.Contains(identityErr.Error(), "Paris") || !strings.Contains(identityErr.Error(), "Lisbon") {
		t.Fatalf("wrong-arguments control produced identity error %v, want observed Paris and expected Lisbon", identityErr)
	}
	t.Logf("wrong-arguments control rejected as expected: %v", identityErr)
}

// TestSessionToolCallConversationDuplicateCallIsRejected duplicates only the
// provider call records. The live executor must observe two calls, and the
// single-result replay gate rejects the second result before follow-up speech.
func TestSessionToolCallConversationDuplicateCallIsRejected(t *testing.T) {
	wavPath, wirePath := buildConversationControlFixture(t, func(capture *gwtesting.SessionCapture) {
		duplicateConversationCall(t, capture)
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	started := time.Now()
	stdout, _, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	assertConversationResultGateFailure(t, "duplicate-call", runErr, time.Since(started))
	calls, returned := executor.snapshot()
	if len(calls) != 2 {
		t.Fatalf("duplicate-call control executor observed %d calls %v, want exactly two duplicate invocations", len(calls), calls)
	}
	for index, call := range calls {
		if call.ID != toolConversationCallID || call.Name != toolCallScenarioName || call.Arguments != toolCallScenarioArguments {
			t.Fatalf("duplicate-call control invocation %d = %#v, want ID=%q name=%q args=%q", index, call, toolConversationCallID, toolCallScenarioName, toolCallScenarioArguments)
		}
	}
	if len(returned) != 2 {
		t.Fatalf("duplicate-call control returned %d results from its two executor invocations, want both attempted before the strict gate", len(returned))
	}
	if strings.Contains(stdout, "24 degrees") || strings.Contains(stdout, "clear skies") {
		t.Fatalf("duplicate-call control leaked follow-up transcript after the second result was rejected:\n%s", stdout)
	}
	t.Logf("duplicate-call control rejected as expected: %v", runErr)
}

// TestSessionToolCallConversationMissingResultIsRejectedAtGate removes only
// the expected provider result. The executor invocation remains valid, but the
// normal close boundary reports the still-unresolved call instead of allowing
// the missing result to be silently discarded.
func TestSessionToolCallConversationMissingResultIsRejectedAtGate(t *testing.T) {
	wavPath, wirePath := buildConversationControlFixture(t, func(capture *gwtesting.SessionCapture) {
		removeExpectedConversationResult(t, capture)
		removeConversationFollowUp(t, capture)
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	started := time.Now()
	stdout, _, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	assertConversationMissingResultFailure(t, runErr, time.Since(started))
	assertConversationOneCall(t, executor)
	if strings.Contains(stdout, "24 degrees") || strings.Contains(stdout, "clear skies") {
		t.Fatalf("missing-result control leaked follow-up transcript despite the absent result gate:\n%s", stdout)
	}
	t.Logf("missing-result control rejected as expected: %v", runErr)
}

// TestSessionToolCallConversationDuplicateResultIsRejectedWithBoundedLiveness
// inserts only a second expected provider result. Production emits one result;
// the replay remains at the duplicate outbound boundary and must fail within a
// short explicit bound instead of treating one result as two.
func TestSessionToolCallConversationDuplicateResultIsRejectedWithBoundedLiveness(t *testing.T) {
	wavPath, wirePath := buildConversationControlFixture(t, func(capture *gwtesting.SessionCapture) {
		duplicateExpectedConversationResult(t, capture)
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	started := time.Now()
	stdout, _, runErr := runToolResultConversationWithBounds(t, wavPath, wirePath, executor, 8*time.Second, 4*time.Second)
	if runErr == nil {
		t.Fatalf("duplicate-result control completed cleanly; one emitted result incorrectly satisfied two provider obligations")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("duplicate-result control took %s, want bounded failure within 5s: %v", elapsed, runErr)
	}
	if !strings.Contains(runErr.Error(), "replay") && !strings.Contains(runErr.Error(), "awaiting") {
		t.Fatalf("duplicate-result control failed with %q, want replay/incomplete diagnostic naming the blocked duplicate result", runErr)
	}
	if outputs := functionCallOutputsInExchange(t, wirePath); len(outputs) != 2 {
		t.Fatalf("duplicate-result control fixture contains %d provider result obligations, want exactly two", len(outputs))
	}
	assertConversationOneCall(t, executor)
	if strings.Contains(stdout, "24 degrees") || strings.Contains(stdout, "clear skies") {
		t.Fatalf("duplicate-result control leaked follow-up transcript before the duplicate result was delivered:\n%s", stdout)
	}
	t.Logf("duplicate-result control rejected as expected: %v", runErr)
}

func TestSessionToolCallConversationMismatchedResultCallIDIsRejectedAtGate(t *testing.T) {
	wavPath, wirePath := buildConversationControlFixture(t, func(capture *gwtesting.SessionCapture) {
		mutateExpectedConversationResult(t, capture, conversationOtherCallID, toolResultPositive)
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	started := time.Now()
	stdout, _, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	assertConversationResultGateFailure(t, "mismatched-result-call-id", runErr, time.Since(started))
	assertConversationOneCall(t, executor)
	if strings.Contains(stdout, "24 degrees") || strings.Contains(stdout, "clear skies") {
		t.Fatalf("mismatched-result-call-id control leaked follow-up transcript after pairing rejection:\n%s", stdout)
	}
	t.Logf("mismatched-result-call-id control rejected as expected: %v", runErr)
}

func TestSessionToolCallConversationEmptyResultCallIDIsRejectedAtGate(t *testing.T) {
	wavPath, wirePath := buildConversationControlFixture(t, func(capture *gwtesting.SessionCapture) {
		mutateExpectedConversationResult(t, capture, "", toolResultPositive)
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	started := time.Now()
	stdout, _, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	assertConversationResultGateFailure(t, "empty-result-call-id", runErr, time.Since(started))
	assertConversationOneCall(t, executor)
	if strings.Contains(stdout, "24 degrees") || strings.Contains(stdout, "clear skies") {
		t.Fatalf("empty-result-call-id control leaked follow-up transcript after pairing rejection:\n%s", stdout)
	}
	t.Logf("empty-result-call-id control rejected as expected: %v", runErr)
}

// TestSessionToolCallConversationContradictoryGroundingIsRejected keeps the
// correctly paired result and valid audible output, but changes only the
// fluent follow-up transcript. The shared result-unique reflection assertion
// must reject it, proving grounding is independently non-vacuous.
func TestSessionToolCallConversationContradictoryGroundingIsRejected(t *testing.T) {
	wavPath, wirePath := buildConversationControlFixture(t, func(capture *gwtesting.SessionCapture) {
		mutateConversationTranscript(t, capture, conversationContradictoryReply)
	})
	executor := &conversationResultExecutor{result: toolResultPositive}
	stdout, outputPath, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	if runErr != nil {
		t.Fatalf("grounding control transport failed before its contradictory transcript was delivered: %v\nstdout:\n%s", runErr, stdout)
	}
	assertConversationOneCall(t, executor)
	outputs := functionCallOutputsInExchange(t, wirePath)
	if len(outputs) != 1 || outputs[0].CallID != toolConversationCallID || outputs[0].Output != toolResultPositive {
		t.Fatalf("grounding control result pairing = %v, want exactly the positive result for %q", outputs, toolConversationCallID)
	}
	assertRecordedSpeech(t, outputPath, toolSingleCallReplySamples)
	if err := transcriptReflectionError(stdout); err == nil {
		t.Fatalf("grounding control passed the result-unique reflection assertion despite contradictory transcript %q", conversationContradictoryReply)
	} else {
		for _, marker := range []string{"24 degrees", "clear skies"} {
			if !strings.Contains(err.Error(), marker) {
				t.Fatalf("grounding-control error %q does not name missing result fact %q", err, marker)
			}
		}
		t.Logf("grounding control rejected as expected: %v", err)
	}
	if !strings.Contains(stdout, "99 degrees") || !strings.Contains(stdout, "stormy skies") {
		t.Fatalf("grounding control did not deliver its fluent contradictory transcript; stdout=%q", stdout)
	}
}
