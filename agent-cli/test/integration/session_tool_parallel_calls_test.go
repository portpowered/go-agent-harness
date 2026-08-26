package integration

// s2s v4b-tool-parallel-calls vertical: CLI-verified hermetic (T1) proof that
// concurrent tool calls in one provider turn of a real 'agent session' run all
// succeed and each result is paired back to its originating call by ToolCallID,
// following the s2s-v4a/v4c replay-transport lane conventions.
//
// Proven here through the public CLI surface over the record/replay transport:
//   - a synthetic capture whose first provider turn carries TWO named function
//     tool calls with distinct call IDs and distinct argument payloads
//     traverses the real agent session path,
//   - a barrier-based recording executor observes each call exactly once and
//     proves genuine overlap deterministically: every call blocks until ALL
//     calls are observed in-flight, so a sequential executor deadlocks the
//     bounded run instead of passing, and the completion order is forced to
//     the reverse of request order purely with channel gating (no sleeps),
//   - every executor response echoes its own call ID with its own content,
//   - the replayed exchange proves the executed results flowed back into the
//     model-facing conversation state: the follow-up
//     conversation.item.create/response.create pair that opens the second
//     provider turn can only be emitted after the tool batch completed and its
//     reconstructed tool-output messages triggered the next inference pass —
//     the replay transport itself enforces this ordering, because the fixture
//     blocks the second turn's inbound frames behind those exact outbound
//     events,
//   - resumed output speech is produced in that post-tool turn,
//   - a negative control swaps one result's ToolCallID at the executor
//     boundary and proves the shared pairing assertion fails deterministically.
//
// Why prompt-seeded instead of v4a's file-backed audio-in: the audio-in runner
// stops the session at the first terminal response frame, which cancels an
// in-flight tool batch mid-barrier (observed empirically: arrivals recorded,
// completions lost to cooperative cancellation). The prompt-seeded replay
// runtime enables WaitForClose whenever the fixture ends with session.closed,
// so the loop persists across the completed tool turn and the batch finishes
// with no cancellation pressure. Everything else — synthetic capture shape,
// strict outbound byte matching, bounded duration, recorded-speech assertion —
// follows the established lane conventions.
//
// Outbound delivery gap (documented, not silently weakened): on current main
// the OpenAI Realtime outbound translation maps only AUDIO.DELTA, MESSAGE.END,
// and TEXT.DELTA (see go-llm-gateway/pkg/providers/openai/session_events.go),
// and the duplex ModelRunner forwards only the latest user text to the
// session, so reconstructed tool-result messages trigger the follow-up model
// pass but are never serialized into the provider exchange as
// conversation.item.create function_call_output items. The exchange verifier
// below therefore asserts that ANY function_call_output events present in the
// replayed outbound exchange pair 1:1 with executed call IDs and their result
// contents (zero today), so the moment outbound translation lands this lane
// extends to full exchange-level pairing instead of silently ignoring it. The
// gap is reported in the PR description per the lane contract.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// The two provider-issued tool calls carried by the first assistant turn, in
// request order. Distinct names, call IDs, arguments, and result payloads so
// any cross-wiring is detectable.
const (
	parallelCallAlphaID   = "call_par_alpha"
	parallelCallAlphaName = "get_weather"
	parallelCallAlphaArgs = `{"city":"Lisbon"}`

	parallelCallBravoID   = "call_par_bravo"
	parallelCallBravoName = "get_time"
	parallelCallBravoArgs = `{"zone":"UTC"}`

	// parallelPrompt is the seeded user text. The duplex session re-sends the
	// latest user text verbatim to open every provider turn, so the fixture
	// encodes the identical payload for the initial turn and the post-tool
	// follow-up turn.
	parallelPrompt = "run both tools now"

	// parallelReplySamples sizes the scripted post-tool spoken reply window
	// carved from the committed corpus fixture.
	parallelReplySamples = 9600
)

// parallelResultContent holds the distinct result payload owned by each call.
var parallelResultContent = map[string]string{
	parallelCallAlphaID: `{"temperature_c":24,"condition":"clear","origin":"alpha"}`,
	parallelCallBravoID: `{"utc":"12:34","zone":"UTC","origin":"bravo"}`,
}

// parallelRequestOrder is the request order encoded by the fixture; the
// positive path forces completion order to be exactly its reverse.
var parallelRequestOrder = []string{parallelCallAlphaID, parallelCallBravoID}

// parallelUserItemCreatePayload reproduces byte-for-byte what the gateway
// serializes for the duplex session's user-turn conversation.item.create: the
// event data is marshaled from Go maps (sorted keys) and writeEvent re-marshals
// the payload with the event type injected, again over sorted map keys.
func parallelUserItemCreatePayload(text string) json.RawMessage {
	payload := map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": text},
			},
		},
	}
	data, _ := json.Marshal(payload)
	return data
}

// buildParallelToolCallsFixture writes a synthetic record/replay capture in
// two provider turns. Turn one issues the two named function tool calls and
// terminates; turn two can only be reached after the client emits the
// follow-up conversation.item.create/response.create pair, which production
// sends solely from the inference request triggered by the reconstructed tool
// outputs. The capture ends with session.closed, which arms the replay
// runtime's WaitForClose behavior.
func buildParallelToolCallsFixture(t *testing.T, replySamples []int16) string {
	t.Helper()
	baseCapture, err := gwtesting.LoadSessionCapture(filepath.Join("testdata", "openai_realtime_smoke.session.json"))
	if err != nil {
		t.Fatalf("load replay base fixture: %v", err)
	}
	records := []gwtesting.CapturedSessionEvent{baseCapture.Records[0], baseCapture.Records[1]}

	clientEvent := func(eventType string, payload json.RawMessage) {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionClientToServer,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     payload,
		})
	}
	clientEventRaw := func(eventType, payload string) {
		clientEvent(eventType, json.RawMessage(payload))
	}

	serverEvent := func(eventType string, payload string) {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionServerToClient,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(payload),
		})
	}

	// Initial provider turn opened by the seeded user text.
	clientEvent("conversation.item.create", parallelUserItemCreatePayload(parallelPrompt))
	clientEventRaw("response.create", `{"type":"response.create"}`)

	serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_tool_parallel_1"}}`)
	serverEvent("response.output_item.added",
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"`+parallelCallAlphaID+`","name":"`+parallelCallAlphaName+`"}}`)
	serverEvent("response.function_call_arguments.done",
		`{"type":"response.function_call_arguments.done","call_id":"`+parallelCallAlphaID+`","name":"`+parallelCallAlphaName+`","arguments":`+strconvQuote(parallelCallAlphaArgs)+`}`)
	serverEvent("response.output_item.added",
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"`+parallelCallBravoID+`","name":"`+parallelCallBravoName+`"}}`)
	serverEvent("response.function_call_arguments.done",
		`{"type":"response.function_call_arguments.done","call_id":"`+parallelCallBravoID+`","name":"`+parallelCallBravoName+`","arguments":`+strconvQuote(parallelCallBravoArgs)+`}`)
	serverEvent("response.done", `{"type":"response.done","response":{"id":"resp_tool_parallel_1","status":"completed"}}`)

	// Post-tool follow-up turn: reachable only after the executed results were
	// reconstructed and dispatched the next inference pass.
	clientEvent("conversation.item.create", parallelUserItemCreatePayload(parallelPrompt))
	clientEventRaw("response.create", `{"type":"response.create"}`)

	serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_tool_parallel_2"}}`)
	transcriptDelta, marshalErr := json.Marshal(map[string]string{
		"type":  "response.output_audio_transcript.delta",
		"delta": "Both tools finished; here is your answer.",
	})
	if marshalErr != nil {
		t.Fatalf("marshal transcript delta: %v", marshalErr)
	}
	serverEvent("response.output_audio_transcript.delta", string(transcriptDelta))
	serverEvent("response.output_audio_transcript.done", `{"type":"response.output_audio_transcript.done","transcript":"Both tools finished; here is your answer."}`)

	audioDelta, marshalErr := json.Marshal(map[string]string{
		"type":  "response.output_audio.delta",
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(replySamples)),
	})
	if marshalErr != nil {
		t.Fatalf("marshal audio delta: %v", marshalErr)
	}
	serverEvent("response.output_audio.delta", string(audioDelta))
	serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`)
	serverEvent("response.done", `{"type":"response.done","response":{"id":"resp_tool_parallel_2","status":"completed"}}`)

	baseCapture.Session.ID = "sess_tool_parallel_calls"
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_tool_parallel_calls","reason":"fixture_complete"}`),
	})
	wirePath := filepath.Join(t.TempDir(), "tool-parallel-calls.session.json")
	wireData, err := json.MarshalIndent(baseCapture, "", "  ")
	if err != nil {
		t.Fatalf("marshal wire fixture: %v", err)
	}
	if err := os.WriteFile(wirePath, wireData, 0o600); err != nil {
		t.Fatalf("write wire fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(wirePath); err != nil {
		t.Fatalf("replay fixture rejected by the session replayer dialer: %v", err)
	}
	return wirePath
}

// parallelToolExecutor is a messages.ToolExecutor that records every invocation
// and gates execution deterministically: each call blocks until ALL expected
// calls are observed in-flight (proving genuine concurrency — a sequential
// executor deadlocks and trips the bounded run), and the first call of the
// request order may complete only after the last one has completed (forcing
// completion order to be the reverse of request order). No wall-clock sleeps.
//
// swapPairing corrupts exactly one result attribution at the executor boundary
// for the negative control: the first request-order call's response is returned
// carrying the second call's ID and content.
type parallelToolExecutor struct {
	expectedIDs []string
	swapPairing bool

	mu          sync.Mutex
	arrivals    []string
	completions []string
	callsByID   map[string]messages.ToolCall
	returned    []messages.ToolCallResponse

	releaseOnce   sync.Once
	allInFlight   chan struct{}
	bravoComplete chan struct{}
}

func newParallelToolExecutor() *parallelToolExecutor {
	return &parallelToolExecutor{
		expectedIDs:   parallelRequestOrder,
		callsByID:     map[string]messages.ToolCall{},
		allInFlight:   make(chan struct{}),
		bravoComplete: make(chan struct{}),
	}
}

func (e *parallelToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.arrivals = append(e.arrivals, call.ID)
	e.callsByID[call.ID] = call
	arrived := len(e.arrivals)
	e.mu.Unlock()
	if arrived == len(e.expectedIDs) {
		e.releaseOnce.Do(func() { close(e.allInFlight) })
	}

	// Barrier phase 1: hold every call in-flight until all expected calls have
	// been observed. Cooperative cancellation keeps a partially dispatched
	// batch bounded instead of hanging past the session lifetime.
	select {
	case <-e.allInFlight:
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}

	response := messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    parallelResultContent[call.ID],
	}

	// Barrier phase 2: force completion order to the reverse of request order.
	// The last request-order call completes immediately; the first waits until
	// the last has recorded its completion.
	if call.ID == parallelCallBravoID {
		e.mu.Lock()
		e.completions = append(e.completions, call.ID)
		e.mu.Unlock()
		close(e.bravoComplete)
	} else {
		select {
		case <-e.bravoComplete:
		case <-ctx.Done():
			return messages.ToolCallResponse{}, ctx.Err()
		}
		e.mu.Lock()
		e.completions = append(e.completions, call.ID)
		e.mu.Unlock()
	}

	if e.swapPairing && call.ID == parallelCallAlphaID {
		// Negative control only: attribute the first call's result wholesale
		// to the second call — its ID, its tool name, and its content —
		// leaving the second result untouched.
		response.ToolCallID = parallelCallBravoID
		response.Name = parallelCallBravoName
		response.Content = parallelResultContent[parallelCallBravoID]
	}

	e.mu.Lock()
	e.returned = append(e.returned, response)
	e.mu.Unlock()
	return response, nil
}

// snapshot returns a consistent copy of the recorded execution facts.
func (e *parallelToolExecutor) snapshot() (arrivals, completions []string, returned []messages.ToolCallResponse, calls map[string]messages.ToolCall) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.arrivals...),
		append([]string(nil), e.completions...),
		append([]messages.ToolCallResponse(nil), e.returned...),
		copyParallelCalls(e.callsByID)
}

func copyParallelCalls(src map[string]messages.ToolCall) map[string]messages.ToolCall {
	dst := make(map[string]messages.ToolCall, len(src))
	for id, call := range src {
		dst[id] = call
	}
	return dst
}

// validatePairedParallelToolResults is the shared vertical pairing assertion
// used by both the positive path and its swapped-pairing control: every
// expected call was executed exactly once with its fixture name and arguments,
// and every returned response carries exactly one expected call ID paired with
// exactly that call's own result content, 1:1 with no phantoms or duplicates.
// Keeping the failure here lets the swapped-pairing control fail on the same
// code path the positive assertion validates.
func validatePairedParallelToolResults(arrivals, completions []string, returned []messages.ToolCallResponse, calls map[string]messages.ToolCall) error {
	if len(arrivals) != len(parallelRequestOrder) {
		return fmt.Errorf("executor observed %d arrivals %v, want exactly %d calls %v", len(arrivals), arrivals, len(parallelRequestOrder), parallelRequestOrder)
	}
	seen := map[string]int{}
	for _, id := range arrivals {
		seen[id]++
	}
	for _, id := range parallelRequestOrder {
		if seen[id] != 1 {
			return fmt.Errorf("call %q observed %d times, want exactly once", id, seen[id])
		}
		call, ok := calls[id]
		if !ok {
			return fmt.Errorf("call %q not recorded with its full identity", id)
		}
		wantName, wantArgs := parallelExpectedIdentity(id)
		if call.Name != wantName {
			return fmt.Errorf("call %q invoked tool %q, want %q", id, call.Name, wantName)
		}
		if call.Arguments != wantArgs {
			return fmt.Errorf("call %q invoked with arguments %q, want %q", id, call.Arguments, wantArgs)
		}
	}
	if len(returned) != len(parallelRequestOrder) {
		return fmt.Errorf("executor returned %d responses %v, want exactly %d", len(returned), describeResponses(returned), len(parallelRequestOrder))
	}
	contentByCall := map[string]string{}
	for _, resp := range returned {
		call, executed := calls[resp.ToolCallID]
		if !executed {
			return fmt.Errorf("response attributed to unknown call %q; executed calls were %v", resp.ToolCallID, parallelRequestOrder)
		}
		wantContent := parallelResultContent[resp.ToolCallID]
		if resp.Content != wantContent {
			return fmt.Errorf("result for call %q carries content %q, want its own content %q", resp.ToolCallID, resp.Content, wantContent)
		}
		if call.Name != "" && resp.Name != "" && resp.Name != call.Name {
			return fmt.Errorf("result for call %q names tool %q, want %q", resp.ToolCallID, resp.Name, call.Name)
		}
		contentByCall[resp.ToolCallID] = resp.Content
	}
	for _, id := range parallelRequestOrder {
		if _, paired := contentByCall[id]; !paired {
			return fmt.Errorf("no result paired back to call %q; responses were %v (cross-wired or lost)", id, describeResponses(returned))
		}
	}
	return nil
}

func parallelExpectedIdentity(id string) (name, args string) {
	if id == parallelCallAlphaID {
		return parallelCallAlphaName, parallelCallAlphaArgs
	}
	return parallelCallBravoName, parallelCallBravoArgs
}

func describeResponses(resps []messages.ToolCallResponse) string {
	parts := make([]string, 0, len(resps))
	for _, r := range resps {
		parts = append(parts, fmt.Sprintf("{id:%s content:%s}", r.ToolCallID, r.Content))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// runParallelToolCalls drives the real 'agent session' command surface — wired
// through the same composition root as production with the given executor
// swapped into the tool-executor port — over the hermetic record/replay
// transport with a seeded user prompt and recorded audio-out.
func runParallelToolCalls(t *testing.T, wirePath string, executor *parallelToolExecutor) (string, error) {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "response.wav")
	agentCLI, err := wire.InitializeMockAgentCLI(executor, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize agent CLI: %v", err)
	}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--replay", wirePath,
		"--audio-out", outputPath,
		"--max-duration", "3s",
		parallelPrompt,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	return outputPath, err
}

// parallelExchangeToolCall is one function_call_arguments.done event observed
// in the replayed server-to-client exchange.
type parallelExchangeToolCall struct {
	CallID    string
	Name      string
	Arguments string
}

// inspectParallelExchange loads the replayed provider exchange and returns the
// named tool calls in recorded order plus whether output audio follows them.
func inspectParallelExchange(t *testing.T, wirePath string) ([]parallelExchangeToolCall, bool) {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(wirePath)
	if err != nil {
		t.Fatalf("load replayed provider exchange: %v", err)
	}
	var calls []parallelExchangeToolCall
	lastToolCallIndex, lastAudioIndex := -1, -1
	for i, record := range capture.Records {
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		var payload struct {
			CallID    string `json:"call_id"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil {
			continue
		}
		switch payload.Type {
		case "response.function_call_arguments.done":
			calls = append(calls, parallelExchangeToolCall{CallID: payload.CallID, Name: payload.Name, Arguments: payload.Arguments})
			lastToolCallIndex = i
		case "response.output_audio.delta":
			lastAudioIndex = i
		}
	}
	return calls, lastAudioIndex > lastToolCallIndex
}

// verifyFollowUpUserTurn verifies the outbound client-to-provider side of the
// replayed exchange: exactly one seeded user turn carrying the prompt opens
// the session, and exactly one identical follow-up user turn appears strictly
// after every function_call_arguments.done event. Production emits this pair
// only from the inference request that the reconstructed tool outputs
// schedule, so its presence and placement in the strictly matched exchange is
// exchange-level evidence that both results flowed back into the model-facing
// conversation state.
func verifyFollowUpUserTurn(t *testing.T, wirePath string) {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(wirePath)
	if err != nil {
		t.Fatalf("load replayed provider exchange: %v", err)
	}
	seeded, followUp := 0, 0
	lastIssuedCallIndex := -1
	for i, record := range capture.Records {
		switch record.Direction {
		case gwtesting.DirectionServerToClient:
			if record.Type == "response.function_call_arguments.done" {
				lastIssuedCallIndex = i
			}
			continue
		case gwtesting.DirectionClientToServer:
		default:
			continue
		}
		if record.Type != "conversation.item.create" {
			continue
		}
		var payload struct {
			Item struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil || payload.Item.Role != "user" || len(payload.Item.Content) != 1 {
			continue
		}
		if payload.Item.Content[0].Text != parallelPrompt {
			t.Fatalf("outbound user turn %d carries text %q, want the seeded prompt %q", i, payload.Item.Content[0].Text, parallelPrompt)
		}
		if lastIssuedCallIndex >= 0 && i > lastIssuedCallIndex {
			followUp++
		} else {
			seeded++
		}
	}
	if seeded != 1 || followUp != 1 {
		t.Fatalf("exchange shows %d seeded and %d post-tool follow-up user turns, want exactly 1 of each (the follow-up turn proves executed results reached the model-facing conversation)", seeded, followUp)
	}
}

// countOutboundFunctionCallOutputs scans the client-to-server side of the
// replayed exchange for function_call_output conversation items and verifies
func countOutboundFunctionCallOutputs(t *testing.T, wirePath string, executedContents map[string]string) int {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(wirePath)
	if err != nil {
		t.Fatalf("load replayed provider exchange: %v", err)
	}
	delivered := 0
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		var payload struct {
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil {
			continue
		}
		if payload.Item.Type != "function_call_output" {
			continue
		}
		delivered++
		want, executed := executedContents[payload.Item.CallID]
		if !executed {
			t.Fatalf("outbound function_call_output attributes a result to call %q which never executed", payload.Item.CallID)
		}
		if payload.Item.Output != want {
			t.Fatalf("outbound function_call_output for call %q carries %q, want that call's own result %q", payload.Item.CallID, payload.Item.Output, want)
		}
	}
	return delivered
}

// TestSessionParallelToolCallsRoundTripThroughCLI is the full positive path:
// the real agent session CLI receives a seeded request whose replayed provider
// turn issues two concurrent tool calls; both execute exactly once through the
// composed executor with proven overlap and reversed completion order; every
// result is paired back to its own call ID; and the follow-up provider turn —
// gated behind those executed results by the strict replay — resumes speech.
func TestSessionParallelToolCallsRoundTripThroughCLI(t *testing.T) {
	wavPath := toolSingleCallWAVPath(t)
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	reply := loudestWindowSamplesIntegration(t, samples, parallelReplySamples)

	executor := newParallelToolExecutor()
	wirePath := buildParallelToolCallsFixture(t, reply)
	outputPath, runErr := runParallelToolCalls(t, wirePath, executor)
	if runErr != nil {
		t.Fatalf("agent session over replay failed: %v", runErr)
	}
	assertRecordedSpeech(t, outputPath, len(reply))

	exchangeCalls, audioAfter := inspectParallelExchange(t, wirePath)
	if len(exchangeCalls) != 2 {
		t.Fatalf("replayed exchange contains %d function tool calls %v, want exactly 2", len(exchangeCalls), exchangeCalls)
	}
	for i, wantID := range parallelRequestOrder {
		got := exchangeCalls[i]
		wantName, wantArgs := parallelExpectedIdentity(wantID)
		if got.CallID != wantID || got.Name != wantName || got.Arguments != wantArgs {
			t.Fatalf("exchange tool call %d = %+v, want call %q (%s %s) in request order", i, got, wantID, wantName, wantArgs)
		}
	}
	if exchangeCalls[0].CallID == exchangeCalls[1].CallID {
		t.Fatalf("exchange carries duplicate call ID %q; want distinct concurrent call IDs", exchangeCalls[0].CallID)
	}
	if !audioAfter {
		t.Fatal("no output speech produced after the tool calls in the replayed provider exchange")
	}

	arrivals, completions, returned, calls := executor.snapshot()

	// Exactly-once execution, inbound identity pairing, and 1:1 result pairing.
	if err := validatePairedParallelToolResults(arrivals, completions, returned, calls); err != nil {
		t.Fatalf("tool execution/pairing assertion failed: %v\narrivals=%v completions=%v responses=%s",
			err, arrivals, completions, describeResponses(returned))
	}

	// Reversed completion order relative to request order, forced by channel
	// gating inside the executor (no wall-clock sleeps anywhere); both calls
	// necessarily overlapped because neither could complete before both passed
	// the all-in-flight barrier.
	wantCompletions := []string{parallelCallBravoID, parallelCallAlphaID}
	if len(completions) != len(wantCompletions) {
		t.Fatalf("executor recorded %d completions %v, want %d", len(completions), completions, len(wantCompletions))
	}
	for i := range wantCompletions {
		if completions[i] != wantCompletions[i] {
			t.Fatalf("completion order = %v, want reversed request order %v", completions, wantCompletions)
		}
	}

	// Exchange-level consequence of correct pairing: the seeded turn opens the
	// session and exactly one identical follow-up turn opens after the tool
	// batch, proving the reconstructed results drove the next inference pass.
	verifyFollowUpUserTurn(t, wirePath)

	// Outbound exchange pairing: any function_call_output delivered to the
	// provider must pair 1:1 with an executed call and its own content (zero
	// on current main — outbound translation gap documented in the header).
	executedContents := map[string]string{}
	for _, id := range parallelRequestOrder {
		executedContents[id] = parallelResultContent[id]
	}
	if delivered := countOutboundFunctionCallOutputs(t, wirePath, executedContents); delivered == 0 {
		t.Logf("outbound tool-result delivery: 0 function_call_output events in the exchange (documented realtime outbound translation gap on main)")
	}
}

// TestSessionParallelToolCallsSwappedPairingFailsDeterministically is the
// negative control: the same CLI flow with the executor swapping exactly one
// result's ToolCallID must fail the shared pairing assertion deterministically
// — never via timeout or transport error — proving the pairing assertion cannot
// pass vacuously.
func TestSessionParallelToolCallsSwappedPairingFailsDeterministically(t *testing.T) {
	wavPath := toolSingleCallWAVPath(t)
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	reply := loudestWindowSamplesIntegration(t, samples, parallelReplySamples)

	executor := newParallelToolExecutor()
	executor.swapPairing = true
	wirePath := buildParallelToolCallsFixture(t, reply)
	outputPath, runErr := runParallelToolCalls(t, wirePath, executor)
	if runErr != nil {
		t.Fatalf("swapped-pairing control should complete the session deterministically, got run error: %v", runErr)
	}
	assertRecordedSpeech(t, outputPath, len(reply))

	exchangeCalls, _ := inspectParallelExchange(t, wirePath)
	if len(exchangeCalls) != 2 {
		t.Fatalf("swapped-pairing control fixture delivered %d exchange tool calls, want 2 (control must isolate pairing, not issuance)", len(exchangeCalls))
	}

	arrivals, completions, returned, calls := executor.snapshot()
	assertionErr := validatePairedParallelToolResults(arrivals, completions, returned, calls)
	if assertionErr == nil {
		t.Fatalf("shared pairing assertion passed on a swapped-pairing run; responses were %s — the check does not discriminate", describeResponses(returned))
	}
	if !strings.Contains(assertionErr.Error(), parallelCallAlphaID) || !strings.Contains(assertionErr.Error(), parallelCallBravoID) {
		t.Fatalf("pairing assertion error %q does not identify both involved call IDs %q and %q", assertionErr, parallelCallAlphaID, parallelCallBravoID)
	}
	t.Logf("negative control rejected as expected: %v", assertionErr)
}
