package integration

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

// s2s async-tool-result serialization vertical: CLI-verified hermetic proof
// that a scheduled spoken turn waits for an outstanding provider tool call's
// accepted result and grounded continuation before its own audio reaches the
// provider, without losing the local result or wedging session teardown.
//
// The replay transport is deliberately gated at the supported websocket
// dialer seam. The real CLI starts with a positional prompt that produces a
// tool call, then holds the scheduled audio until the result-driven spoken
// continuation reaches MESSAGE.END. The transport rejects any early audio
// append, making the serialization causal rather than timing-based while
// keeping the behavior assertion at the public `agent session` boundary.
//
// The production session path forwards normalized tool results through the
// provider-facing stream. The verifier therefore requires the real outbound
// function_call_output and one explicit continuation boundary, rather than
// treating local RoleTool delivery or a scripted capture record as a substitute
// for wire evidence.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	asyncCollisionPrompt             = "finish the pending weather lookup"
	asyncCollisionCallID             = "call_async_weather_1"
	asyncCollisionToolName           = "get_weather"
	asyncCollisionToolArgs           = `{"city":"Lisbon"}`
	asyncCollisionResult             = `{"temperature_c":24,"condition":"clear","sentinel":"async-result-001"}`
	asyncCollisionSessionID          = "sess_async_tool_result_interrupts_speech"
	asyncCollisionResponseOne        = "resp_async_tool_1"
	asyncCollisionResponseTwo        = "resp_async_speech"
	asyncCollisionResponseThree      = "resp_async_continuation"
	asyncCollisionCloseReason        = "async_collision_complete"
	asyncCollisionDeltaSamples       = 1600
	asyncCollisionDeltaCount         = 2
	asyncCollisionInputSamples       = audio.FrameSize
	asyncCollisionMaxDuration        = 10 * time.Second
	asyncCollisionControlMaxDuration = 250 * time.Millisecond
	asyncCollisionDisposition        = "queue/sequence"
)

// asyncCollisionTrace records the causal milestones asserted by the positive
// proof. The observer and executor use the same mutex, so the order is based on
// runtime events rather than elapsed time.
type asyncCollisionTrace struct {
	mu     sync.Mutex
	events []string
}

func (t *asyncCollisionTrace) record(event string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.events = append(t.events, event)
	t.mu.Unlock()
}

func (t *asyncCollisionTrace) snapshot() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.events...)
}

// asyncCollisionToolExecutor blocks the single provider-issued call until the
// stream observer sees the first audio delta of the unrelated later response.
// It records both the incoming call and the deterministic sentinel response.
type asyncCollisionToolExecutor struct {
	trace *asyncCollisionTrace

	started         chan struct{}
	release         chan struct{}
	resultReady     chan struct{}
	startedOnce     sync.Once
	releaseOnce     sync.Once
	resultReadyOnce sync.Once

	mu       sync.Mutex
	calls    []messages.ToolCall
	returned []messages.ToolCallResponse
}

func newAsyncCollisionToolExecutor(trace *asyncCollisionTrace) *asyncCollisionToolExecutor {
	return &asyncCollisionToolExecutor{
		trace:       trace,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
		resultReady: make(chan struct{}),
	}
}

func (e *asyncCollisionToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	callNumber := len(e.calls)
	e.mu.Unlock()
	e.trace.record(fmt.Sprintf("tool_execute_%d", callNumber))
	e.startedOnce.Do(func() {
		e.trace.record("tool_started")
		close(e.started)
	})

	select {
	case <-e.release:
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}

	e.trace.record("tool_returned")
	response := messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    asyncCollisionResult,
	}
	e.mu.Lock()
	e.returned = append(e.returned, response)
	e.mu.Unlock()
	e.resultReadyOnce.Do(func() { close(e.resultReady) })
	return response, nil
}

func (e *asyncCollisionToolExecutor) releaseResult() {
	e.releaseOnce.Do(func() { close(e.release) })
}

func (e *asyncCollisionToolExecutor) snapshot() (calls []messages.ToolCall, returned []messages.ToolCallResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...), append([]messages.ToolCallResponse(nil), e.returned...)
}

// asyncCollisionObserver watches the deltas consumed by the CLI's session
// runner. The first collision audio delta releases the blocked executor; the
// sentinel RoleTool text delta records local result delivery. This is the
// causal overlap contract used by the test.
type asyncCollisionObserver struct {
	trace       *asyncCollisionTrace
	executor    *asyncCollisionToolExecutor
	firstAudio  []byte
	secondAudio []byte

	turnOneCompleted       chan struct{}
	turnOneCompletedOnce   sync.Once
	collisionCompleted     chan struct{}
	collisionCompletedOnce sync.Once

	mu                        sync.Mutex
	deltas                    []messages.StreamMessage
	responseCompleted         int
	toolCallInResponse        bool
	collisionAudioSeen        int
	collisionResponseComplete bool
}

func newAsyncCollisionObserver(trace *asyncCollisionTrace, executor *asyncCollisionToolExecutor, firstAudio, secondAudio []byte) *asyncCollisionObserver {
	return &asyncCollisionObserver{
		trace:              trace,
		executor:           executor,
		firstAudio:         append([]byte(nil), firstAudio...),
		secondAudio:        append([]byte(nil), secondAudio...),
		turnOneCompleted:   make(chan struct{}),
		collisionCompleted: make(chan struct{}),
	}
}

func (o *asyncCollisionObserver) observe(msg messages.StreamMessage) {
	o.mu.Lock()
	o.deltas = append(o.deltas, msg)
	o.mu.Unlock()

	switch value := msg.Value.(type) {
	case *messages.SessionOpenValue:
		o.trace.record("session_open_observed")
	case *messages.SessionCreatedValue:
		o.trace.record("session_created_observed")
	case *messages.MessageEndValue:
		o.mu.Lock()
		toolResponse := o.toolCallInResponse
		toolResult := msg.Role == messages.RoleTool
		o.toolCallInResponse = false
		if !toolResponse && !toolResult {
			o.responseCompleted++
		}
		firstResponseComplete := !toolResponse && !toolResult && o.responseCompleted == 1
		o.mu.Unlock()
		if firstResponseComplete {
			o.trace.record("turn_one_completed")
			o.turnOneCompletedOnce.Do(func() { close(o.turnOneCompleted) })
		} else if toolResponse {
			o.trace.record("tool_response_completed")
		}
	case *messages.AudioDeltaValue:
		if value == nil {
			return
		}
		if bytes.Equal(value.Content, o.firstAudio) {
			o.trace.record("collision_audio_1_observed")
			o.mu.Lock()
			o.collisionAudioSeen++
			o.mu.Unlock()
			o.executor.releaseResult()
		}
		if bytes.Equal(value.Content, o.secondAudio) {
			o.trace.record("collision_audio_2_observed")
			o.mu.Lock()
			o.collisionAudioSeen++
			o.mu.Unlock()
		}
	case *messages.AudioEndValue:
		o.mu.Lock()
		complete := o.collisionAudioSeen >= asyncCollisionDeltaCount && !o.collisionResponseComplete
		if complete {
			o.collisionResponseComplete = true
		}
		o.mu.Unlock()
		if complete {
			o.trace.record("collision_response_completed")
			o.collisionCompletedOnce.Do(func() { close(o.collisionCompleted) })
		}
	case *messages.ToolCallStartValue:
		o.mu.Lock()
		o.toolCallInResponse = true
		o.mu.Unlock()
		o.trace.record("tool_call_start_observed")
	case *messages.ToolCallEndValue:
		o.mu.Lock()
		o.toolCallInResponse = true
		o.mu.Unlock()
		o.trace.record("tool_call_end_observed")
		o.executor.releaseResult()
	case *messages.TextDeltaValue:
		if msg.Role == messages.RoleTool && value != nil && value.Content == asyncCollisionResult {
			o.trace.record("tool_result_observed")
		}
	}
}

func (o *asyncCollisionObserver) snapshot() []messages.StreamMessage {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]messages.StreamMessage(nil), o.deltas...)
}

type asyncCollisionRunResult struct {
	outputPath    string
	sessionOutput string
	outbound      []asyncCollisionOutbound
	executor      *asyncCollisionToolExecutor
	observer      *asyncCollisionObserver
	trace         *asyncCollisionTrace
	runErr        error
}

func runAsyncCollisionScenario(t *testing.T, fixtureCollision, expectedCollision, continuation [][]int16, options asyncCollisionRunOptions) asyncCollisionRunResult {
	t.Helper()
	options = options.normalized()
	trace := &asyncCollisionTrace{}
	executor := newAsyncCollisionToolExecutor(trace)
	observer := newAsyncCollisionObserver(trace, executor, pcm16LEBytes(fixtureCollision[0]), pcm16LEBytes(fixtureCollision[1]))
	signals := newAsyncCollisionSignals()
	inputAudio := asyncCollisionInputAudio()
	wirePath, capture := buildAsyncCollisionFixture(t, fixtureCollision, continuation, inputAudio)
	outputPath, sessionOutput, outbound, runErr := runAsyncCollisionCLI(t, wirePath, capture, inputAudio, executor, observer, signals, options)
	return asyncCollisionRunResult{
		outputPath:    outputPath,
		sessionOutput: sessionOutput,
		outbound:      outbound,
		executor:      executor,
		observer:      observer,
		trace:         trace,
		runErr:        runErr,
	}
}

func runAsyncCollisionCLI(t *testing.T, wirePath string, capture gwtesting.SessionCapture, inputAudio []byte, executor *asyncCollisionToolExecutor, observer *asyncCollisionObserver, signals *asyncCollisionSignals, options asyncCollisionRunOptions) (string, string, []asyncCollisionOutbound, error) {
	t.Helper()
	options = options.normalized()
	control := signals.control(observer.turnOneCompleted, observer.collisionCompleted, inputAudio)
	control.trace = executor.trace
	control.dropProviderResult = options.dropProviderResult
	control.withholdTerminal = options.withholdTerminal
	replayDialer, err := newAsyncCollisionReplayDialer(capture, control)
	if err != nil {
		t.Fatalf("build gated async collision replay dialer: %v", err)
	}
	sessionInferencer, err := servicetest.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "replay", Model: "gpt-realtime"},
		oaiprovider.WithWebSocketDialer(replayDialer),
	)
	if err != nil {
		t.Fatalf("build OpenAI realtime session inferencer: %v", err)
	}
	sessionInferencer = &asyncCollisionSessionInferencer{inner: sessionInferencer, trace: executor.trace}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		executor,
		&mockInferencer{response: "unused"},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	agentCLI.SetSessionStreamObserver(observer.observe)
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	outputPath := filepath.Join(t.TempDir(), "async-collision-response.wav")
	inputPath := filepath.Join(t.TempDir(), "async-collision-first-turn.raw")
	if err := os.WriteFile(inputPath, inputAudio, 0o600); err != nil {
		t.Fatalf("write async collision input fixture: %v", err)
	}
	recordingDir := filepath.Join(t.TempDir(), "async-collision-recording")
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--replay", wirePath,
		"--record-dir", recordingDir,
		"--audio-in-turn", inputPath,
		"--wait-for-close",
		"--audio-out", outputPath,
		"--max-duration", options.maxDuration.String(),
		asyncCollisionPrompt,
	})
	commandTimeout := 3 * options.maxDuration
	if commandTimeout < 2*time.Second {
		commandTimeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	runErr := rootCmd.ExecuteContext(ctx)
	return outputPath, writer.StdoutString(), replayDialer.outboundSnapshot(), runErr
}

func verifyAsyncCollisionAudio(outputPath string, collision, continuation [][]int16) error {
	wavBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return fmt.Errorf("read recorded --audio-out WAV: %w", err)
	}
	rate, got, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		return fmt.Errorf("parse recorded --audio-out WAV: %w", err)
	}
	if rate != 16000 {
		return fmt.Errorf("recorded --audio-out WAV rate = %d, want 16000", rate)
	}
	segments := []struct {
		name   string
		deltas [][]int16
	}{
		{name: "continuation", deltas: continuation},
		{name: "collision", deltas: collision},
	}
	want := make([]int16, 0, asyncCollisionDeltaSamples*asyncCollisionDeltaCount*2)
	for _, segment := range segments {
		for _, delta := range segment.deltas {
			want = append(want, delta...)
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("audio sample count = %d, want %d; collision/continuation delta loss or duplication", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			deltaOrdinal := i / asyncCollisionDeltaSamples
			segmentIndex := deltaOrdinal / asyncCollisionDeltaCount
			deltaIndex := deltaOrdinal % asyncCollisionDeltaCount
			if segmentIndex >= len(segments) {
				return fmt.Errorf("audio sample mismatch at index %d, got %d want %d", i, got[i], want[i])
			}
			return fmt.Errorf("audio sample mismatch at index %d (%s delta span %d), got %d want %d", i, segments[segmentIndex].name, deltaIndex, got[i], want[i])
		}
	}
	return nil
}

func validateAsyncCollisionExecution(calls []messages.ToolCall, returned []messages.ToolCallResponse) error {
	if len(calls) != 1 {
		return fmt.Errorf("outstanding call %q executed %d times, want exactly once", asyncCollisionCallID, len(calls))
	}
	call := calls[0]
	if call.ID != asyncCollisionCallID || call.Name != asyncCollisionToolName || call.Arguments != asyncCollisionToolArgs {
		return fmt.Errorf("executed call = {id:%q name:%q args:%q}, want {id:%q name:%q args:%q}", call.ID, call.Name, call.Arguments, asyncCollisionCallID, asyncCollisionToolName, asyncCollisionToolArgs)
	}
	if len(returned) != 1 {
		return fmt.Errorf("call %q produced %d local results, want exactly one", asyncCollisionCallID, len(returned))
	}
	if returned[0].ToolCallID != asyncCollisionCallID || returned[0].Content != asyncCollisionResult {
		return fmt.Errorf("local result = {id:%q content:%q}, want original ID %q and sentinel %q", returned[0].ToolCallID, returned[0].Content, asyncCollisionCallID, asyncCollisionResult)
	}
	return nil
}

func validateAsyncCollisionToolDeltas(deltas []messages.StreamMessage) error {
	var toolDeltas []messages.StreamMessage
	for _, delta := range deltas {
		if delta.Role == messages.RoleTool {
			toolDeltas = append(toolDeltas, delta)
		}
	}
	if len(toolDeltas) == 0 {
		return fmt.Errorf("session loop emitted no local RoleTool result for outstanding call %q", asyncCollisionCallID)
	}
	for _, delta := range toolDeltas {
		// The enclosing MESSAGE.START/END delimiters belong to the whole
		// batch and intentionally carry no individual ToolCallID. Content
		// deltas are the per-call correlation evidence.
		switch delta.Type {
		case messages.StreamTypeTextStart, messages.StreamTypeTextDelta, messages.StreamTypeTextEnd:
		default:
			continue
		}
		if delta.ToolCallId != asyncCollisionCallID {
			return fmt.Errorf("local tool-result delta %s carries ToolCallID %q, want %q", delta.Type, delta.ToolCallId, asyncCollisionCallID)
		}
	}
	resultMessages := messages.ReconstructToolMessagesFromDeltas(toolDeltas)
	if len(resultMessages) != 1 || resultMessages[0].ToolCallID != asyncCollisionCallID || resultMessages[0].TextContent() != asyncCollisionResult {
		return fmt.Errorf("reconstructed local result = %v, want exactly one sentinel for %q", resultMessages, asyncCollisionCallID)
	}
	return nil
}

func validateAsyncCollisionTrace(events []string) error {
	required := []string{
		"tool_started",
		"turn_one_completed",
		"later_turn_requested",
		"collision_audio_1_observed",
		"tool_returned",
		"tool_result_observed",
		"collision_audio_2_observed",
		"collision_response_completed",
		"continuation_requested",
	}
	positions := make(map[string]int, len(events))
	counts := make(map[string]int, len(events))
	for i, event := range events {
		counts[event]++
		positions[event] = i
	}
	for _, event := range required {
		if counts[event] != 1 {
			if counts[event] == 0 {
				return fmt.Errorf("causal trace missing %q: %v", event, events)
			}
			return fmt.Errorf("causal trace contains %d %q events, want exactly one: %v", counts[event], event, events)
		}
	}
	// The scheduled request is downstream of the complete tool lifecycle. The
	// provider result and its continuation therefore precede the later request.
	constraints := [][2]string{
		{"tool_started", "tool_returned"},
		{"tool_returned", "tool_result_observed"},
		{"continuation_requested", "turn_one_completed"},
		{"turn_one_completed", "later_turn_requested"},
		{"later_turn_requested", "collision_audio_1_observed"},
		{"collision_audio_1_observed", "collision_audio_2_observed"},
		{"collision_audio_2_observed", "collision_response_completed"},
	}
	for _, constraint := range constraints {
		before, after := constraint[0], constraint[1]
		if positions[before] >= positions[after] {
			return fmt.Errorf("causal trace order = %v, want %q before %q", events, before, after)
		}
	}
	return nil
}

func countAsyncProviderResults(outbound []asyncCollisionOutbound) (int, error) {
	count := 0
	for _, event := range outbound {
		if event.Type != "conversation.item.create" {
			continue
		}
		var payload struct {
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return 0, fmt.Errorf("decode outbound conversation.item.create: %w", err)
		}
		if payload.Item.Type != "function_call_output" {
			continue
		}
		count++
		if payload.Item.CallID != asyncCollisionCallID || payload.Item.Output != asyncCollisionResult {
			return count, fmt.Errorf("outbound function_call_output = {call_id:%q output:%q}, want original ID %q and sentinel %q", payload.Item.CallID, payload.Item.Output, asyncCollisionCallID, asyncCollisionResult)
		}
	}
	return count, nil
}

func validateAsyncCollisionProviderResult(outbound []asyncCollisionOutbound) error {
	count, err := countAsyncProviderResults(outbound)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("provider-facing result loss for outstanding call %q: observed %d function_call_output events, want exactly one", asyncCollisionCallID, count)
	}
	return nil
}

func validateAsyncCollisionProviderBoundary(events []string, outbound []asyncCollisionOutbound, expectProviderResult bool) error {
	count, err := countAsyncProviderResults(outbound)
	if err != nil {
		return err
	}
	wantCount := 0
	if expectProviderResult {
		wantCount = 1
	}
	if count != wantCount {
		return fmt.Errorf("%s provider-facing result for outstanding call %q: observed %d function_call_output events, want %d", asyncCollisionDisposition, asyncCollisionCallID, count, wantCount)
	}
	positions := make(map[string]int, len(events))
	counts := make(map[string]int, len(events))
	for index, event := range events {
		positions[event] = index
		counts[event]++
	}
	if counts["provider_result_sent"] != wantCount {
		return fmt.Errorf("provider-facing result was observed %d times in the causal trace, want %d", counts["provider_result_sent"], wantCount)
	}
	if !expectProviderResult {
		return nil
	}
	if positions["provider_result_sent"] >= positions["continuation_requested"] {
		return fmt.Errorf("%s provider result boundary is out of order: %v", asyncCollisionDisposition, events)
	}
	return nil
}

func validateAsyncCollisionContinuation(outbound []asyncCollisionOutbound, expectedInputAudio []byte, expectProviderResult bool) error {
	userTurnIndices := make([]int, 0, 1)
	responseCreateIndices := make([]int, 0, 3)
	providerResultIndices := make([]int, 0, 1)
	inputAppendIndex, inputCommitIndex := -1, -1
	providerResultCount, err := countAsyncProviderResults(outbound)
	if err != nil {
		return err
	}
	wantProviderResultCount := 0
	if expectProviderResult {
		wantProviderResultCount = 1
	}
	if providerResultCount != wantProviderResultCount {
		return fmt.Errorf("%s provider result for outstanding call %q: observed %d function_call_output events, want %d", asyncCollisionDisposition, asyncCollisionCallID, providerResultCount, wantProviderResultCount)
	}
	for index, event := range outbound {
		switch event.Type {
		case "conversation.item.create":
			var payload struct {
				Item struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("decode outbound continuation item: %w", err)
			}
			if payload.Item.Type == "message" {
				userTurnIndices = append(userTurnIndices, index)
			} else if payload.Item.Type == "function_call_output" && payload.Item.CallID == asyncCollisionCallID {
				providerResultIndices = append(providerResultIndices, index)
			}
		case "response.create":
			responseCreateIndices = append(responseCreateIndices, index)
		case "input_audio_buffer.append":
			if inputAppendIndex >= 0 {
				return fmt.Errorf("provider exchange contains multiple input_audio_buffer.append events")
			}
			if err := validateAsyncOutboundInputAudio(event.Payload, expectedInputAudio); err != nil {
				return err
			}
			inputAppendIndex = index
		case "input_audio_buffer.commit":
			if inputCommitIndex >= 0 {
				return fmt.Errorf("provider exchange contains multiple input_audio_buffer.commit events")
			}
			inputCommitIndex = index
		}
	}
	if len(userTurnIndices) != 1 {
		return fmt.Errorf("provider exchange contains %d text user turns, want only the seeded prompt", len(userTurnIndices))
	}
	if len(responseCreateIndices) != 3 {
		return fmt.Errorf("provider exchange contains %d response.create events, want the initial audio turn, later collision turn, and result-driven continuation", len(responseCreateIndices))
	}
	if inputAppendIndex < 0 || inputCommitIndex < 0 {
		return fmt.Errorf("provider exchange is missing the later-turn input audio append/commit boundary")
	}
	if userTurnIndices[0] >= responseCreateIndices[0] || responseCreateIndices[0] >= responseCreateIndices[1] || responseCreateIndices[1] >= inputAppendIndex || inputAppendIndex >= inputCommitIndex || inputCommitIndex >= responseCreateIndices[2] {
		return fmt.Errorf("initial text/tool continuation/later-turn audio boundary is out of order: user=%d initial response.create=%d continuation response.create=%d append=%d commit=%d later response.create=%d", userTurnIndices[0], responseCreateIndices[0], responseCreateIndices[1], inputAppendIndex, inputCommitIndex, responseCreateIndices[2])
	}
	if expectProviderResult {
		if len(providerResultIndices) != 1 {
			return fmt.Errorf("%s provider result for %q was correlated %d times, want exactly one", asyncCollisionDisposition, asyncCollisionCallID, len(providerResultIndices))
		}
		if providerResultIndices[0] <= responseCreateIndices[0] || providerResultIndices[0] >= responseCreateIndices[1] {
			return fmt.Errorf("%s provider result for %q was not sent between the initial tool response and its continuation response.create", asyncCollisionDisposition, asyncCollisionCallID)
		}
	} else if len(providerResultIndices) != 0 {
		return fmt.Errorf("result-loss control still carried %d provider results for %q", len(providerResultIndices), asyncCollisionCallID)
	}
	return nil
}

func validateAsyncCollisionTerminal(sessionOutput string, runErr error) error {
	want := "[session closed: " + asyncCollisionCloseReason + "]"
	if strings.Contains(sessionOutput, want) {
		return nil
	}
	if runErr != nil {
		return fmt.Errorf("required terminal event %q was not observed before bounded CLI run ended (session output %q): %w", want, sessionOutput, runErr)
	}
	return fmt.Errorf("required terminal event %q was not observed (session output %q)", want, sessionOutput)
}

// validateAsyncCollisionRun is the shared verifier for the positive path and
// every control. A control can disable only the assertion it intentionally
// damages; the other runtime, correlation, continuation, and terminal checks
// remain identical.
func validateAsyncCollisionRun(run asyncCollisionRunResult, collision, continuation [][]int16, verifyAudio, expectProviderResult bool) error {
	if err := validateAsyncCollisionTerminal(run.sessionOutput, run.runErr); err != nil {
		return err
	}
	if run.runErr != nil {
		return fmt.Errorf("agent session async collision replay failed: %w", run.runErr)
	}
	if verifyAudio {
		if err := verifyAsyncCollisionAudio(run.outputPath, collision, continuation); err != nil {
			return err
		}
	}
	calls, returned := run.executor.snapshot()
	if err := validateAsyncCollisionExecution(calls, returned); err != nil {
		return err
	}
	if err := validateAsyncCollisionToolDeltas(run.observer.snapshot()); err != nil {
		return err
	}
	if err := validateAsyncCollisionTrace(run.trace.snapshot()); err != nil {
		return err
	}
	if err := validateAsyncCollisionProviderBoundary(run.trace.snapshot(), run.outbound, expectProviderResult); err != nil {
		return err
	}
	if err := validateAsyncCollisionContinuation(run.outbound, asyncCollisionInputAudio(), expectProviderResult); err != nil {
		return err
	}
	return nil
}

func cloneAsyncCollisionDeltas(deltas [][]int16) [][]int16 {
	clone := make([][]int16, len(deltas))
	for i, delta := range deltas {
		clone[i] = append([]int16(nil), delta...)
	}
	return clone
}

// TestSessionAsyncToolResultInterruptsSpeechThroughCLI is the first-story
// positive collision proof. It validates every observable, including the
// exact-one provider-facing result and its original call ID.
func TestSessionAsyncToolResultInterruptsSpeechThroughCLI(t *testing.T) {
	collision, continuation := asyncCollisionAudio(t)
	run := runAsyncCollisionScenario(t, collision, collision, continuation, asyncCollisionRunOptions{})
	if err := validateAsyncCollisionRun(run, collision, continuation, true, true); err != nil {
		calls, returned := run.executor.snapshot()
		t.Logf("async collision run: err=%v trace=%v outbound=%v calls=%+v returned=%+v deltas=%v", run.runErr, run.trace.snapshot(), summarizeAsyncCollisionOutbound(run.outbound), calls, returned, summarizeAsyncCollisionDeltas(run.observer.snapshot()))
		t.Fatal(err)
	}
	if err := validateAsyncCollisionProviderResult(run.outbound); err != nil {
		t.Fatal(err)
	}
	cancelCount := 0
	for _, event := range run.outbound {
		if event.Type == "response.cancel" {
			cancelCount++
		}
	}
	if cancelCount != 0 {
		t.Fatalf("completion-gated async tool continuation emitted %d response.cancel events, want none: %v", cancelCount, summarizeAsyncCollisionOutbound(run.outbound))
	}
	t.Logf("provider-facing result delivered exactly once for %q", asyncCollisionCallID)
}

func summarizeAsyncCollisionOutbound(outbound []asyncCollisionOutbound) []string {
	types := make([]string, len(outbound))
	for i, event := range outbound {
		types[i] = event.Type
	}
	return types
}

func summarizeAsyncCollisionDeltas(deltas []messages.StreamMessage) []string {
	types := make([]string, len(deltas))
	for i, delta := range deltas {
		types[i] = string(delta.Type)
	}
	return types
}

// TestSessionAsyncToolResultProviderResultLossFailsVerifier is the result-loss
// control. It keeps the collision, audio, continuation, and terminal path
// healthy, while suppressing only a provider-facing function_call_output at
// the replay transport boundary. The shared verifier still checks every
// unrelated outcome before the targeted result-loss assertion names the call.
func TestSessionAsyncToolResultProviderResultLossFailsVerifier(t *testing.T) {
	collision, continuation := asyncCollisionAudio(t)
	run := runAsyncCollisionScenario(t, collision, collision, continuation, asyncCollisionRunOptions{
		dropProviderResult: true,
	})
	if err := validateAsyncCollisionRun(run, collision, continuation, true, false); err != nil {
		calls, returned := run.executor.snapshot()
		t.Fatalf("result-loss control changed an unrelated collision outcome: %v\ntrace=%v outbound=%v calls=%+v returned=%+v", err, run.trace.snapshot(), summarizeAsyncCollisionOutbound(run.outbound), calls, returned)
	}

	assertionErr := validateAsyncCollisionProviderResult(run.outbound)
	if assertionErr == nil {
		t.Fatal("result-loss control was not detected by the provider-result verifier")
	}
	if !strings.Contains(assertionErr.Error(), asyncCollisionCallID) {
		t.Fatalf("result-loss diagnostic %q does not name outstanding call %q", assertionErr, asyncCollisionCallID)
	}
}

// TestSessionAsyncToolResultAudioDamageFailsVerifier mutates one collision
// delta while leaving transport completion and the continuation untouched.
// The shared PCM verifier must identify the affected collision span.
func TestSessionAsyncToolResultAudioDamageFailsVerifier(t *testing.T) {
	collision, continuation := asyncCollisionAudio(t)
	damaged := cloneAsyncCollisionDeltas(collision)
	damaged[1][0] ^= 1
	run := runAsyncCollisionScenario(t, damaged, collision, continuation, asyncCollisionRunOptions{})
	if err := validateAsyncCollisionRun(run, collision, continuation, false, true); err != nil {
		calls, returned := run.executor.snapshot()
		t.Fatalf("audio-damage control changed an unrelated runtime outcome: %v\ntrace=%v outbound=%v calls=%+v returned=%+v", err, run.trace.snapshot(), summarizeAsyncCollisionOutbound(run.outbound), calls, returned)
	}

	assertionErr := verifyAsyncCollisionAudio(run.outputPath, collision, continuation)
	if assertionErr == nil {
		t.Fatal("audio-damage control was not detected by the byte-exact PCM verifier")
	}
	if !strings.Contains(assertionErr.Error(), "collision delta span 1") {
		t.Fatalf("audio-damage diagnostic %q does not identify collision delta 1", assertionErr)
	}
}

// TestSessionAsyncToolResultMissingTerminalFailsBounded is the wedge control.
// It withholds only the fixture's terminal event; the bounded CLI must return
// and the shared verifier must report the missing exact terminal boundary.
func TestSessionAsyncToolResultMissingTerminalFailsBounded(t *testing.T) {
	collision, continuation := asyncCollisionAudio(t)
	run := runAsyncCollisionScenario(t, collision, collision, continuation, asyncCollisionRunOptions{
		maxDuration:      asyncCollisionControlMaxDuration,
		withholdTerminal: true,
	})
	assertionErr := validateAsyncCollisionRun(run, collision, continuation, true, true)
	if assertionErr == nil {
		t.Fatal("missing-terminal control unexpectedly passed the terminal verifier")
	}
	if !strings.Contains(assertionErr.Error(), "required terminal event") && !strings.Contains(assertionErr.Error(), "bounded CLI run") {
		t.Fatalf("missing-terminal diagnostic %q does not identify terminal/timeout failure", assertionErr)
	}
}
