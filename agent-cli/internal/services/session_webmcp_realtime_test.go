package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	webMCPRealtimeListCallID    = "call_webmcp_list"
	webMCPRealtimeInvokeCallOne = "call_webmcp_invoke_1"
	webMCPRealtimeInvokeCallTwo = "call_webmcp_invoke_2"
)

func TestOpenAIRealtimeWebMCPResultsCorrelateAndContinueOnce(t *testing.T) {
	broker, runtime, targetSession, _ := newWebMCPRealtimeFixture(t, 0, testkit.WithAutoResponse(json.RawMessage(`{"accepted":true}`)))
	defer func() { _ = broker.Close() }()

	toolSet := webmcpTools.NewBrokerToolSet(broker)
	wire := newWebMCPRealtimeWire()
	inferencer, err := buildOpenAIRealtimeSessionInferencerWithTools(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime"},
		"",
		webMCPRealtimeDialer{wire: wire},
		toolSet.Definitions(),
	)
	if err != nil {
		t.Fatalf("build realtime inferencer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- runAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			WaitForClose:    true,
			ToolExecutor:    toolSet.Executor(),
			ToolDefinitions: toolSet.Definitions(),
		})
	}()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run realtime WebMCP session: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("realtime WebMCP session did not finish: %v", ctx.Err())
	}
	if err := wire.protocolError(); err != nil {
		t.Fatalf("scripted provider protocol: %v", err)
	}

	writes := wire.writesSnapshot()
	wantTypes := []string{
		"session.update",
		"conversation.item.create", // list_tools result
		"response.create",          // one continuation for list_tools
		"conversation.item.create", // first invoke result
		"conversation.item.create", // second invoke result
		"response.create",          // one continuation for the two-call batch
	}
	gotTypes := make([]string, 0, len(writes))
	for _, write := range writes {
		gotTypes = append(gotTypes, write.Type)
	}
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("provider writes = %v, want %v; payloads=%s", gotTypes, wantTypes, wire.writePayloads())
	}
	for index := range wantTypes {
		if gotTypes[index] != wantTypes[index] {
			t.Fatalf("provider writes = %v, want %v (mismatch at %d)", gotTypes, wantTypes, index)
		}
	}

	outputs := make(map[string]string)
	for _, write := range writes {
		if write.Type != "conversation.item.create" {
			continue
		}
		var event struct {
			Item struct {
				Type    string          `json:"type"`
				CallID  string          `json:"call_id"`
				Output  string          `json:"output"`
				Content json.RawMessage `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal(write.Payload, &event); err != nil {
			t.Fatalf("decode provider tool result: %v", err)
		}
		if event.Item.Type != "function_call_output" {
			t.Fatalf("provider conversation item = %#v, want only function_call_output items", event.Item)
		}
		if len(event.Item.Content) != 0 {
			t.Fatalf("provider tool result carried rich content parts: %s", event.Item.Content)
		}
		outputs[event.Item.CallID] = event.Item.Output
	}
	if len(outputs) != 3 {
		t.Fatalf("provider tool result call IDs = %#v, want list plus two invoke results", outputs)
	}
	if _, ok := outputs[webMCPRealtimeListCallID]; !ok {
		t.Fatalf("list_tools result missing from provider output: %#v", outputs)
	}
	for _, callID := range []string{webMCPRealtimeInvokeCallOne, webMCPRealtimeInvokeCallTwo} {
		output, ok := outputs[callID]
		if !ok {
			t.Fatalf("invoke result %q missing from provider output: %#v", callID, outputs)
		}
		envelope, err := webmcp.UnmarshalToolResult([]byte(output))
		if err != nil {
			t.Fatalf("decode %s WebMCP result: %v", callID, err)
		}
		if !envelope.OK || envelope.Version != webmcp.ToolResultVersion {
			t.Fatalf("%s envelope = %#v, want successful %s", callID, envelope, webmcp.ToolResultVersion)
		}
		var result struct {
			Status string          `json:"status"`
			Output json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(envelope.Data, &result); err != nil {
			t.Fatalf("decode %s result data: %v", callID, err)
		}
		if result.Status != string(webmcp.InvocationCompleted) || string(result.Output) != `{"accepted":true}` {
			t.Fatalf("%s result data = %#v, want completed fixture output", callID, result)
		}
	}

	listEnvelope, err := webmcp.UnmarshalToolResult([]byte(outputs[webMCPRealtimeListCallID]))
	if err != nil {
		t.Fatalf("decode list_tools WebMCP result: %v", err)
	}
	var catalog struct {
		Tools []struct {
			Ref  webmcp.ToolRef `json:"ref"`
			Name string         `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listEnvelope.Data, &catalog); err != nil {
		t.Fatalf("decode list_tools data: %v", err)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].Name != "write_fixture" || catalog.Tools[0].Ref == "" {
		t.Fatalf("list_tools catalog = %#v, want one generation-bound fixture ref", catalog.Tools)
	}

	invocations := targetSession.Invocations()
	if len(invocations) != 2 {
		t.Fatalf("fixture invocations = %#v, want exactly two", invocations)
	}
	wantInputs := map[string]struct{}{`{"value":42}`: {}, `{"value":43}`: {}}
	for index, invocation := range invocations {
		if invocation.ToolName != "write_fixture" || invocation.FrameID != "frame-1" || !invocation.Terminal {
			t.Fatalf("fixture invocation %d = %#v, want correlated terminal write_fixture input", index, invocation)
		}
		if _, ok := wantInputs[string(invocation.Input)]; !ok {
			t.Fatalf("fixture invocation %d input = %s, want one of %v", index, invocation.Input, wantInputs)
		}
		delete(wantInputs, string(invocation.Input))
	}
	if len(wantInputs) != 0 {
		t.Fatalf("fixture invocations missing inputs = %v", wantInputs)
	}

	invokeOperations := make([]testkit.Operation, 0, 2)
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationInvoke {
			invokeOperations = append(invokeOperations, operation)
		}
	}
	if len(invokeOperations) != 2 {
		t.Fatalf("fixture invoke operations = %#v, want exactly two", invokeOperations)
	}
	seenOperationInputs := make(map[string]struct{}, len(invokeOperations))
	for index, operation := range invokeOperations {
		if operation.ToolName != "write_fixture" {
			t.Fatalf("fixture invoke operation %d = %#v, want page tool name", index, operation)
		}
		seenOperationInputs[string(operation.Input)] = struct{}{}
	}
	// The broker's concurrent admission order may vary, but both original
	// input_json objects must reach the fixture exactly once.
	if len(seenOperationInputs) != 2 {
		t.Fatalf("fixture invoke operation inputs = %#v, want both values", seenOperationInputs)
	}
	for _, input := range []string{`{"value":42}`, `{"value":43}`} {
		if _, ok := seenOperationInputs[input]; !ok {
			t.Fatalf("fixture invoke operations = %#v, missing input %s", invokeOperations, input)
		}
	}

	responseCreates := 0
	for _, write := range writes {
		if write.Type == "response.create" {
			responseCreates++
		}
	}
	if responseCreates != 2 {
		t.Fatalf("provider response.create count = %d, want exactly one per result batch", responseCreates)
	}
}

func TestWebMCPAmbiguitySessionForwardsOneResultAndAsksOneQuestion(t *testing.T) {
	const (
		callID   = "call_ambiguous_context"
		question = "Which page should I use: Orders (https://orders.example.test) or Billing (https://billing.example.test)?"
	)
	ambiguity := webMCPAmbiguitySessionResult(t)
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(out, question, "",
		scriptedTurn{events: toolCallEvents(callID, webmcp.GetContextToolName, `{}`)},
	)
	inferencer.followUpEvents = []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(question)},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1, 2, 3})},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	calls := 0
	executor := sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		calls++
		if call.ID != callID || call.Name != webmcp.GetContextToolName || call.Arguments != `{}` {
			return messages.ToolCallResponse{}, fmt.Errorf("unexpected ambiguity call: %#v", call)
		}
		return messages.ToolCallResponse{Content: ambiguity}, nil
	})

	err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:     2 * time.Second,
		WaitForClose:    true,
		ToolExecutor:    executor,
		ToolDefinitions: []messages.ToolDefinition{{Name: webmcp.GetContextToolName}},
		observer:        observer,
	})
	if err != nil {
		t.Fatalf("run ambiguous WebMCP session: %v\noutput:\n%s", err, out.String())
	}
	if calls != 1 {
		t.Fatalf("ambiguous WebMCP executor calls = %d, want exactly one", calls)
	}
	if strings.Count(out.String(), question) != 1 {
		t.Fatalf("assistant disambiguation question count = %d, want one\noutput:\n%s", strings.Count(out.String(), question), out.String())
	}
	observer.toolStateMu.Lock()
	outputAudioBytes := observer.responseOutputAudioBytes
	observer.toolStateMu.Unlock()
	if outputAudioBytes == 0 {
		t.Fatal("assistant disambiguation response had no spoken audio")
	}

	session := inferencer.sessionSnapshot()
	if session == nil {
		t.Fatal("scripted inferencer did not retain its session")
	}
	var resultEnds, responseCreates int
	var resultPayload string
	for _, msg := range session.sentSnapshot() {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			resultEnds++
			result, ok := msg.Value.(*messages.ToolCallEndValue)
			if !ok || result == nil {
				t.Fatalf("tool result value = %T, want *messages.ToolCallEndValue", msg.Value)
			}
			if result.ToolCallID != callID || result.Name != webmcp.GetContextToolName {
				t.Fatalf("tool result correlation = (%q, %q), want (%q, %q)", result.ToolCallID, result.Name, callID, webmcp.GetContextToolName)
			}
			resultPayload = result.Arguments
		case messages.StreamTypeResponseCreate:
			responseCreates++
		}
	}
	if resultEnds != 1 || responseCreates != 1 {
		t.Fatalf("ambiguity provider boundaries = result:%d response.create:%d, want one of each", resultEnds, responseCreates)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(resultPayload))
	if err != nil {
		t.Fatalf("decode provider ambiguity result: %v", err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorAmbiguousTab) || !envelope.Error.Retryable {
		t.Fatalf("provider ambiguity envelope = %#v, want retryable ambiguous_tab failure", envelope)
	}
	var details struct {
		CandidateChoices []struct {
			TargetID string `json:"target_id"`
			Title    string `json:"title"`
			Origin   string `json:"origin"`
		} `json:"candidate_choices"`
		Recovery struct {
			Action      string `json:"action"`
			RetryAfter  string `json:"retry_after"`
			Instruction string `json:"instruction"`
		} `json:"recovery"`
	}
	detailsJSON, err := json.Marshal(envelope.Error.Details)
	if err != nil {
		t.Fatalf("marshal ambiguity details: %v", err)
	}
	if err := json.Unmarshal(detailsJSON, &details); err != nil {
		t.Fatalf("decode ambiguity details: %v", err)
	}
	if len(details.CandidateChoices) != 2 {
		t.Fatalf("candidate choices = %#v, want both pages", details.CandidateChoices)
	}
	for _, choice := range details.CandidateChoices {
		if choice.TargetID == "" || choice.Title == "" || choice.Origin == "" {
			t.Fatalf("candidate choice = %#v, want target ID, title, and origin", choice)
		}
		if !strings.Contains(question, choice.Title) || !strings.Contains(question, choice.Origin) {
			t.Fatalf("question %q omitted candidate label %#v", question, choice)
		}
	}
	if details.Recovery.Action != "ask_customer" || details.Recovery.RetryAfter != "customer_input" || !strings.Contains(details.Recovery.Instruction, "do not repeat") {
		t.Fatalf("ambiguity recovery = %#v, want bounded ask-customer guidance", details.Recovery)
	}
}

func TestWebMCPAmbiguitySessionRejectsSilentContinuation(t *testing.T) {
	ambiguity := webMCPAmbiguitySessionResult(t)
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(out, "", "",
		scriptedTurn{events: toolCallEvents("call_silent_ambiguity", webmcp.GetContextToolName, `{}`)},
	)
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:  2 * time.Second,
		WaitForClose: true,
		ToolExecutor: sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
			return messages.ToolCallResponse{Content: ambiguity}, nil
		}),
		ToolDefinitions: []messages.ToolDefinition{{Name: webmcp.GetContextToolName}},
		observer:        observer,
	})
	if !errors.Is(err, ErrSessionAudioResponseIncomplete) {
		t.Fatalf("silent ambiguity continuation error = %v, want ErrSessionAudioResponseIncomplete", err)
	}
	if !errors.Is(err, ErrSessionToolContinuationIncomplete) {
		t.Fatalf("silent ambiguity continuation error = %v, want ErrSessionToolContinuationIncomplete", err)
	}
}

func TestWebMCPAmbiguitySessionUsesExactChoiceBeforePageWork(t *testing.T) {
	const (
		ambiguityCallID = "call_choice_ambiguity"
		selectCallID    = "call_choice_select"
		pageCallID      = "call_choice_page"
		question        = "Which page should I use: Orders (https://orders.example.test) or Billing (https://billing.example.test)?"
	)
	ambiguity := webMCPAmbiguitySessionResult(t)
	out := newSignalingBuffer()
	inferencer := newWebMCPChoiceInferencer(out, question)
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	var (
		mu          sync.Mutex
		calls       []messages.ToolCall
		selected    bool
		pageTargets []string
	)
	executor := sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		switch call.ID {
		case ambiguityCallID:
			if call.Name != webmcp.GetContextToolName || call.Arguments != `{}` {
				return messages.ToolCallResponse{}, fmt.Errorf("unexpected ambiguity call: %#v", call)
			}
			return messages.ToolCallResponse{Content: ambiguity}, nil
		case selectCallID:
			if call.Name != webmcp.SelectTabToolName {
				return messages.ToolCallResponse{}, fmt.Errorf("unexpected selection tool: %#v", call)
			}
			var selection struct {
				BrowserID string `json:"browser_id"`
				TargetID  string `json:"target_id"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &selection); err != nil {
				return messages.ToolCallResponse{}, fmt.Errorf("decode exact selection: %w", err)
			}
			if selection.BrowserID != "browser-session" || selection.TargetID != "target-orders" {
				return messages.ToolCallResponse{}, fmt.Errorf("selection = %#v, want exact Orders candidate", selection)
			}
			mu.Lock()
			selected = true
			mu.Unlock()
			return webMCPChoiceSuccess(t, map[string]any{
				"browser_id": selection.BrowserID,
				"target_id":  selection.TargetID,
			}), nil
		case pageCallID:
			if call.Name != "orders_action" {
				return messages.ToolCallResponse{}, fmt.Errorf("unexpected page tool: %#v", call)
			}
			mu.Lock()
			pageTargets = append(pageTargets, "target-orders")
			wasSelected := selected
			mu.Unlock()
			if !wasSelected {
				return messages.ToolCallResponse{}, errors.New("page work arrived before exact customer choice")
			}
			return webMCPChoiceSuccess(t, map[string]any{"target_id": "target-orders", "status": "completed"}), nil
		default:
			return messages.ToolCallResponse{}, fmt.Errorf("unexpected tool call: %#v", call)
		}
	})

	err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:     2 * time.Second,
		WaitForClose:    true,
		ToolExecutor:    executor,
		ToolDefinitions: []messages.ToolDefinition{{Name: webmcp.GetContextToolName}, {Name: webmcp.SelectTabToolName}, {Name: "orders_action"}},
		observer:        observer,
	})
	if err != nil {
		t.Fatalf("run choice WebMCP session: %v\noutput:\n%s", err, out.String())
	}
	if strings.Count(out.String(), question) != 1 {
		t.Fatalf("choice question count = %d, want one\noutput:\n%s", strings.Count(out.String(), question), out.String())
	}
	mu.Lock()
	gotCalls := append([]messages.ToolCall(nil), calls...)
	gotPageTargets := append([]string(nil), pageTargets...)
	wasSelected := selected
	mu.Unlock()
	if !wasSelected || len(gotPageTargets) != 1 || gotPageTargets[0] != "target-orders" {
		t.Fatalf("choice state selected=%t page_targets=%v, want one chosen Orders page action", wasSelected, gotPageTargets)
	}
	if len(gotCalls) != 3 || gotCalls[0].ID != ambiguityCallID || gotCalls[1].ID != selectCallID || gotCalls[2].ID != pageCallID {
		t.Fatalf("choice tool calls = %#v, want ambiguity then exact selection then chosen page", gotCalls)
	}

	sent := inferencer.sessionSnapshot().sentSnapshot()
	resultEnds, responseCreates := 0, 0
	for _, msg := range sent {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			resultEnds++
		case messages.StreamTypeResponseCreate:
			responseCreates++
		}
	}
	if resultEnds != 3 || responseCreates != 3 {
		t.Fatalf("choice provider boundaries = result:%d response.create:%d, want one correlated continuation per completed call", resultEnds, responseCreates)
	}
}

type webMCPChoiceInferencer struct {
	out      *signalingBuffer
	question string

	sessionMu sync.Mutex
	session   *roundTripSession
}

func newWebMCPChoiceInferencer(out *signalingBuffer, question string) *webMCPChoiceInferencer {
	return &webMCPChoiceInferencer{out: out, question: question}
}

func (i *webMCPChoiceInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := newRoundTripSession()
	i.sessionMu.Lock()
	i.session = session
	i.sessionMu.Unlock()
	go func() {
		if !session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("choice-session", "session"),
		}) {
			return
		}
		if !session.recv.Write(ctx, toolCallEvents("call_choice_ambiguity", webmcp.GetContextToolName, `{}`)[0]) {
			return
		}
		for _, event := range toolCallEvents("call_choice_ambiguity", webmcp.GetContextToolName, `{}`)[1:] {
			if !session.recv.Write(ctx, event) {
				return
			}
		}
		if !session.waitForSent(ctx, messages.StreamTypeResponseCreate) {
			return
		}

		questionEvents := []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(i.question)},
			{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{4, 5, 6})},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		}
		for _, event := range questionEvents {
			if !session.recv.Write(ctx, event) {
				return
			}
		}
		if i.out != nil && !i.out.waitForOutput(i.question, 5*time.Second) {
			return
		}
		for _, event := range []messages.StreamMessage{
			{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleUser, Value: messages.NewTranscriptStartValue()},
			{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("Orders")},
			{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("Orders")},
		} {
			if !session.recv.Write(ctx, event) {
				return
			}
		}
		for _, event := range toolCallEvents("call_choice_select", webmcp.SelectTabToolName, `{"browser_id":"browser-session","target_id":"target-orders"}`) {
			if !session.recv.Write(ctx, event) {
				return
			}
		}
		if !session.waitForSent(ctx, messages.StreamTypeResponseCreate) {
			return
		}
		for _, event := range toolCallEvents("call_choice_page", "orders_action", `{"move":"R"}`) {
			if !session.recv.Write(ctx, event) {
				return
			}
		}
		if !session.waitForSent(ctx, messages.StreamTypeResponseCreate) {
			return
		}
		for _, event := range []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("Done on Orders.")},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("choice-session", "choice complete")},
		} {
			if !session.recv.Write(ctx, event) {
				return
			}
		}
	}()
	return session, nil
}

func (i *webMCPChoiceInferencer) sessionSnapshot() *roundTripSession {
	i.sessionMu.Lock()
	defer i.sessionMu.Unlock()
	return i.session
}

func webMCPChoiceSuccess(t *testing.T, data any) messages.ToolCallResponse {
	t.Helper()
	encoded, err := webmcp.EncodeToolResult(data, nil)
	if err != nil {
		t.Fatalf("encode choice success: %v", err)
	}
	return messages.ToolCallResponse{Content: string(encoded)}
}

func webMCPAmbiguitySessionResult(t *testing.T) string {
	t.Helper()
	resultError := webmcp.ResultErrorFor(
		webmcp.NewClassifiedError(webmcp.ErrorAmbiguousTab, "multiple eligible pages matched", map[string]any{
			"browser_id":           "browser-session",
			"candidate_target_ids": []string{"target-orders", "target-billing"},
			"candidate_choices": []map[string]any{
				{"browser_id": "browser-session", "target_id": "target-orders", "title": "Orders", "origin": "https://orders.example.test"},
				{"browser_id": "browser-session", "target_id": "target-billing", "title": "Billing", "origin": "https://billing.example.test"},
			},
		}),
		webmcp.ErrorInvocationFailed,
		nil,
	)
	encoded, err := webmcp.EncodeToolResult(nil, &resultError)
	if err != nil {
		t.Fatalf("encode ambiguity session result: %v", err)
	}
	return string(encoded)
}

func TestWebMCPMutationTimeoutIsTerminalAndNotRetried(t *testing.T) {
	const timeout = 5 * time.Second
	broker, runtime, targetSession, clock := newWebMCPRealtimeFixture(t, timeout)
	defer func() { _ = broker.Close() }()
	targetSession.BlockInvocations()

	toolSet := webmcpTools.NewBrokerToolSet(broker)
	catalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list fixture tools: %v", err)
	}
	if len(catalog.Tools) != 1 {
		t.Fatalf("fixture catalog = %#v, want one tool", catalog.Tools)
	}
	arguments := `{"tool_ref":"` + string(catalog.Tools[0].Ref) + `","input_json":"{\"value\":99}","reason":"write fixture"}`
	resultCh := make(chan struct {
		response messages.ToolCallResponse
		err      error
	}, 1)
	go func() {
		response, invokeErr := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
			ID:        "call_timeout",
			Name:      webmcp.InvokeToolName,
			Arguments: arguments,
		})
		resultCh <- struct {
			response messages.ToolCallResponse
			err      error
		}{response: response, err: invokeErr}
	}()

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	invocation, err := targetSession.WaitForInvocation(waitCtx)
	if err != nil {
		t.Fatalf("observe blocked fixture invocation: %v", err)
	}
	clock.Advance(timeout)

	var outcome struct {
		response messages.ToolCallResponse
		err      error
	}
	select {
	case outcome = <-resultCh:
	case <-waitCtx.Done():
		t.Fatalf("timed out waiting for terminal WebMCP result: %v", waitCtx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("invoke executor: %v", outcome.err)
	}
	if outcome.response.ToolCallID != "call_timeout" || outcome.response.Name != webmcp.InvokeToolName || len(outcome.response.ContentParts) != 0 {
		t.Fatalf("timeout response correlation/content = %#v", outcome.response)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(outcome.response.Content))
	if err != nil {
		t.Fatalf("decode timeout envelope: %v", err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationTimedOut) || envelope.Error.Retryable {
		t.Fatalf("timeout envelope = %#v, want non-retryable invocation_timed_out", envelope)
	}
	if envelope.Error.Details["side_effect_unknown"] != true {
		t.Fatalf("timeout details = %#v, want side_effect_unknown=true", envelope.Error.Details)
	}

	invocations := targetSession.Invocations()
	if len(invocations) != 1 || invocations[0].ID != invocation.ID {
		t.Fatalf("fixture invocations after timeout = %#v, want one original invocation", invocations)
	}
	invokeCount, cancelCount := 0, 0
	for _, operation := range runtime.Operations() {
		switch operation.Kind {
		case testkit.OperationInvoke:
			invokeCount++
		case testkit.OperationCancel:
			cancelCount++
		}
	}
	if invokeCount != 1 || cancelCount != 1 {
		t.Fatalf("fixture operation counts after unknown mutation timeout = invoke:%d cancel:%d, want one invoke and one bounded cancel", invokeCount, cancelCount)
	}
}

func newWebMCPRealtimeFixture(t *testing.T, invocationTimeout time.Duration, sessionOptions ...testkit.ScriptedTargetSessionOption) (*webmcp.StatefulBroker, *testkit.ScriptedBrowserRuntime, *testkit.ScriptedTargetSession, *testkit.FakeClock) {
	t.Helper()
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-fixture", Product: "scripted", Loopback: true}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-fixture",
		Type:      "page",
		Title:     "Fixture",
		URL:       "https://fixture.test/",
		Origin:    "https://fixture.test",
	}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				target,
				append([]testkit.ScriptedTargetSessionOption{
					testkit.WithInitialCatalog(webmcp.ToolDescriptor{
						Name:        "write_fixture",
						Description: "Write a value to the deterministic fixture.",
						FrameID:     "frame-1",
						InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"required":["value"],"additionalProperties":false}`),
						Annotations: webmcp.ToolAnnotations{ReadOnly: boolPointerForWebMCPRealtime(false)},
					}),
				}, sessionOptions...)...,
			)},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           runtime,
		Discoverer:        webMCPRealtimeDiscoverer{candidate: candidate},
		IDs:               ids,
		Clock:             clock,
		InvocationTimeout: invocationTimeout,
	})
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}); err != nil {
		t.Fatalf("select scripted WebMCP target: %v", err)
	}
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open scripted WebMCP browser: %v", err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession(target.ID)
	if session == nil {
		t.Fatal("scripted WebMCP target session is nil")
	}
	return broker, runtime, session, clock
}

func boolPointerForWebMCPRealtime(value bool) *bool { return &value }

type webMCPRealtimeDiscoverer struct {
	candidate webmcp.BrowserCandidate
}

func (d webMCPRealtimeDiscoverer) Discover(ctx context.Context, _ webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return []webmcp.BrowserCandidate{d.candidate}, nil
	}
}

type webMCPRealtimeDialer struct {
	wire *webMCPRealtimeWire
}

func (d webMCPRealtimeDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	return d.wire, nil
}

type webMCPRealtimeWireWrite struct {
	Type    string
	Payload json.RawMessage
}

type webMCPRealtimeWire struct {
	inbound chan []byte
	done    chan struct{}

	mu             sync.Mutex
	writes         []webMCPRealtimeWireWrite
	protocolErr    error
	listSeen       bool
	listRef        webmcp.ToolRef
	invokeResults  map[string]struct{}
	invokeTurnSent bool
	finalTurnSent  bool
}

func newWebMCPRealtimeWire() *webMCPRealtimeWire {
	return &webMCPRealtimeWire{
		inbound:       make(chan []byte, 64),
		done:          make(chan struct{}),
		invokeResults: make(map[string]struct{}),
	}
}

func (w *webMCPRealtimeWire) WriteMessage(_ int, payload []byte) error {
	select {
	case <-w.done:
		return io.ErrClosedPipe
	default:
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		w.fail(fmt.Errorf("decode client event: %w", err))
		return err
	}
	w.mu.Lock()
	w.writes = append(w.writes, webMCPRealtimeWireWrite{Type: envelope.Type, Payload: append(json.RawMessage(nil), payload...)})
	w.mu.Unlock()

	switch envelope.Type {
	case "session.update":
		w.sendInitialListTurn()
	case "conversation.item.create":
		w.acceptToolResult(payload)
	case "response.create":
		w.acceptContinuation()
	default:
		w.fail(fmt.Errorf("unexpected client event %q", envelope.Type))
	}
	return nil
}

func (w *webMCPRealtimeWire) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-w.inbound:
		return 1, payload, nil
	case <-w.done:
		return 0, nil, io.EOF
	}
}

func (w *webMCPRealtimeWire) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.done:
	default:
		close(w.done)
	}
	return nil
}

func (w *webMCPRealtimeWire) enqueue(event map[string]any) {
	payload, err := json.Marshal(event)
	if err != nil {
		w.fail(fmt.Errorf("encode server event: %w", err))
		return
	}
	select {
	case w.inbound <- payload:
	case <-w.done:
	}
}

func (w *webMCPRealtimeWire) sendInitialListTurn() {
	w.enqueue(map[string]any{"type": "session.created", "session": map[string]any{"id": "sess_webmcp_fixture", "model": "gpt-realtime"}})
	w.sendToolTurn(webMCPRealtimeListCallID, webmcp.ListToolsToolName, `{"include_schemas":true}`)
	w.enqueue(map[string]any{"type": "response.done"})
}

func (w *webMCPRealtimeWire) sendToolTurn(callID, name, arguments string) {
	w.enqueue(map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": "response-" + callID},
	})
	w.enqueue(map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"type":      "function_call",
			"id":        "item-" + callID,
			"call_id":   callID,
			"name":      name,
			"arguments": "",
		},
	})
	w.enqueue(map[string]any{
		"type":      "response.function_call_arguments.done",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	})
}

func (w *webMCPRealtimeWire) acceptToolResult(payload []byte) {
	var event struct {
		Item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		w.fail(fmt.Errorf("decode conversation item: %w", err))
		return
	}
	if event.Item.Type != "function_call_output" {
		w.fail(fmt.Errorf("conversation item type = %q, want function_call_output", event.Item.Type))
		return
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(event.Item.Output))
	if err != nil {
		w.fail(fmt.Errorf("decode %s result: %w", event.Item.CallID, err))
		return
	}
	switch event.Item.CallID {
	case webMCPRealtimeListCallID:
		if !envelope.OK {
			w.fail(fmt.Errorf("list_tools returned failure: %#v", envelope))
			return
		}
		var catalog struct {
			Tools []struct {
				Ref  webmcp.ToolRef `json:"ref"`
				Name string         `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(envelope.Data, &catalog); err != nil {
			w.fail(fmt.Errorf("decode list_tools catalog: %w", err))
			return
		}
		if len(catalog.Tools) != 1 || catalog.Tools[0].Ref == "" || catalog.Tools[0].Name != "write_fixture" {
			w.fail(fmt.Errorf("list_tools catalog = %#v, want one write_fixture ref", catalog.Tools))
			return
		}
		w.mu.Lock()
		if w.listSeen {
			w.mu.Unlock()
			w.fail(errors.New("list_tools result was delivered more than once"))
			return
		}
		w.listSeen = true
		w.listRef = catalog.Tools[0].Ref
		w.mu.Unlock()
	case webMCPRealtimeInvokeCallOne, webMCPRealtimeInvokeCallTwo:
		if !envelope.OK {
			w.fail(fmt.Errorf("%s returned failure: %#v", event.Item.CallID, envelope))
			return
		}
		var result struct {
			ToolRef webmcp.ToolRef  `json:"tool_ref"`
			Status  string          `json:"status"`
			Output  json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(envelope.Data, &result); err != nil {
			w.fail(fmt.Errorf("decode %s data: %w", event.Item.CallID, err))
			return
		}
		w.mu.Lock()
		ref := w.listRef
		w.mu.Unlock()
		if result.ToolRef != ref || result.Status != string(webmcp.InvocationCompleted) || string(result.Output) != `{"accepted":true}` {
			w.fail(fmt.Errorf("%s result = %#v, want completed output for %q", event.Item.CallID, result, ref))
			return
		}
		w.mu.Lock()
		if _, exists := w.invokeResults[event.Item.CallID]; exists {
			w.mu.Unlock()
			w.fail(fmt.Errorf("%s result was delivered more than once", event.Item.CallID))
			return
		}
		w.invokeResults[event.Item.CallID] = struct{}{}
		w.mu.Unlock()
	default:
		w.fail(fmt.Errorf("unexpected function_call_output call ID %q", event.Item.CallID))
	}
}

func (w *webMCPRealtimeWire) acceptContinuation() {
	w.mu.Lock()
	listSeen := w.listSeen
	listRef := w.listRef
	invokeCount := len(w.invokeResults)
	if listSeen && !w.invokeTurnSent {
		w.invokeTurnSent = true
		w.mu.Unlock()
		w.sendToolTurn(webMCPRealtimeInvokeCallOne, webmcp.InvokeToolName, webMCPRealtimeInvokeArguments(listRef, `{"value":42}`))
		w.sendToolTurn(webMCPRealtimeInvokeCallTwo, webmcp.InvokeToolName, webMCPRealtimeInvokeArguments(listRef, `{"value":43}`))
		w.enqueue(map[string]any{"type": "response.done"})
		return
	}
	if invokeCount == 2 && !w.finalTurnSent {
		w.finalTurnSent = true
		w.mu.Unlock()
		w.enqueue(map[string]any{"type": "response.created", "response": map[string]any{"id": "response-final"}})
		w.enqueue(map[string]any{"type": "response.output_text.delta", "delta": "fixture complete"})
		w.enqueue(map[string]any{"type": "response.output_text.done"})
		w.enqueue(map[string]any{"type": "response.done"})
		w.enqueue(map[string]any{"type": "session.closed", "session_id": "sess_webmcp_fixture", "reason": "fixture_complete"})
		return
	}
	w.mu.Unlock()
	w.fail(errors.New("unexpected or duplicate response.create"))
}

func webMCPRealtimeInvokeArguments(ref webmcp.ToolRef, input string) string {
	arguments, _ := json.Marshal(map[string]string{
		"input_json": input,
		"reason":     "write fixture",
		"tool_ref":   string(ref),
	})
	return string(arguments)
}

func (w *webMCPRealtimeWire) fail(err error) {
	if err == nil {
		return
	}
	w.mu.Lock()
	if w.protocolErr == nil {
		w.protocolErr = err
	}
	w.mu.Unlock()
}

func (w *webMCPRealtimeWire) protocolError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.protocolErr
}

func (w *webMCPRealtimeWire) writesSnapshot() []webMCPRealtimeWireWrite {
	w.mu.Lock()
	defer w.mu.Unlock()
	writes := make([]webMCPRealtimeWireWrite, len(w.writes))
	copy(writes, w.writes)
	return writes
}

func (w *webMCPRealtimeWire) writePayloads() string {
	writes := w.writesSnapshot()
	payloads := make([]string, 0, len(writes))
	for _, write := range writes {
		payloads = append(payloads, string(write.Payload))
	}
	return fmt.Sprintf("%q", payloads)
}

var _ transport.Dialer = webMCPRealtimeDialer{}
var _ transport.Conn = (*webMCPRealtimeWire)(nil)
