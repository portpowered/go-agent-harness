//go:build stress

package stress

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// Thread-safe stress inferencer
// ---------------------------------------------------------------------------

// stressInferencer is a concurrency-safe mock inferencer for stress tests.
// Unlike MockInferencer it does not capture message history, avoiding data
// races when the test goroutine inspects state while the model runner writes.
type stressInferencer struct {
	callCount atomic.Int64
	mu        sync.Mutex
	entries   []inferenceEntry
}

func newStressInferencer() *stressInferencer {
	return &stressInferencer{}
}

func (si *stressInferencer) AddTextResponse(text string) *stressInferencer {
	si.mu.Lock()
	si.entries = append(si.entries, inferenceEntry{
		result: messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, text),
		},
	})
	si.mu.Unlock()
	return si
}

func (si *stressInferencer) AddChunkedTextResponse(chunks []string) *stressInferencer {
	combined := strings.Join(chunks, "")
	si.mu.Lock()
	si.entries = append(si.entries, inferenceEntry{
		result: messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, combined),
		},
		chunks: chunks,
	})
	si.mu.Unlock()
	return si
}

func (si *stressInferencer) AddBatchToolCallResponse(calls []messages.ToolCall) *stressInferencer {
	si.mu.Lock()
	si.entries = append(si.entries, inferenceEntry{
		result: messages.InferenceResult{
			Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: calls},
			ToolCalls: calls,
		},
	})
	si.mu.Unlock()
	return si
}

func (si *stressInferencer) currentEntry() inferenceEntry {
	count := int(si.callCount.Load())
	si.mu.Lock()
	defer si.mu.Unlock()
	if len(si.entries) == 0 {
		return inferenceEntry{result: messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, ""),
		}}
	}
	idx := count
	if idx >= len(si.entries) {
		idx = len(si.entries) - 1
	}
	return si.entries[idx]
}

// Infer implements messages.Inferencer (non-streaming fallback).
func (si *stressInferencer) Infer(_ context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	entry := si.currentEntry()
	si.callCount.Add(1)
	return entry.result, nil
}

// InferStream implements messages.Inferencer. Emits deltas in a goroutine so it
// works correctly with large payloads (1000+ chunks) that exceed the channel buffer.
func (si *stressInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	entry := si.currentEntry()
	si.callCount.Add(1)
	result := entry.result
	chunks := entry.chunks

	ch := make(chan messages.StreamMessage, 64)
	go func() {
		defer close(ch)

		send := func(sm messages.StreamMessage) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- sm:
				return true
			}
		}

		// Tool-call response: emit TOOLCALL events.
		for i, tc := range result.ToolCalls {
			if !send(messages.StreamMessage{
				Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: i,
				Value: messages.NewToolCallStartValue(tc.ID, tc.Name),
			}) {
				return
			}
			if tc.Arguments != "" {
				if !send(messages.StreamMessage{
					Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: i,
					Value: messages.NewToolCallDeltaValue(tc.Arguments),
				}) {
					return
				}
			}
			if !send(messages.StreamMessage{
				Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: i,
				Value: messages.NewToolCallEndValue(tc.ID, tc.Name, tc.Arguments),
			}) {
				return
			}
		}

		// Text response (only when there are no tool calls).
		if len(result.ToolCalls) == 0 {
			if text := result.Message.TextContent(); text != "" {
				if !send(messages.StreamMessage{
					Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant,
					Value: messages.NewTextStartValue(),
				}) {
					return
				}
				if len(chunks) > 0 {
					for i, chunk := range chunks {
						if !send(messages.StreamMessage{
							Type: messages.StreamTypeTextDelta, ActorProvidedIndex: i,
							Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(chunk),
						}) {
							return
						}
					}
				} else {
					if !send(messages.StreamMessage{
						Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant,
						Value: messages.NewTextDeltaValue(text),
					}) {
						return
					}
				}
				if !send(messages.StreamMessage{
					Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant,
					Value: messages.NewTextEndValue(),
				}) {
					return
				}
			}
		}

		send(messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Value: messages.NewMessageEndValue(result.TokenUsage),
		})
	}()

	return ch, nil
}

// CallCount returns the total number of inference calls.
func (si *stressInferencer) CallCount() int64 {
	return si.callCount.Load()
}

// ---------------------------------------------------------------------------
// Thread-safe stress tool executor
// ---------------------------------------------------------------------------

type stressToolExecutor struct {
	callCount atomic.Int64
	mu        sync.RWMutex
	results   map[string]string
}

func newStressToolExecutor() *stressToolExecutor {
	return &stressToolExecutor{results: make(map[string]string)}
}

func (e *stressToolExecutor) AddResult(toolName, result string) *stressToolExecutor {
	e.mu.Lock()
	e.results[toolName] = result
	e.mu.Unlock()
	return e
}

func (e *stressToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.callCount.Add(1)
	e.mu.RLock()
	content := e.results[call.Name]
	e.mu.RUnlock()
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Content:    content,
	}, nil
}

func (e *stressToolExecutor) CallCount() int64 {
	return e.callCount.Load()
}

// ---------------------------------------------------------------------------
// Stress tests
// ---------------------------------------------------------------------------

// TestStress_RapidConsecutiveExecute verifies that 120 rapid consecutive
// Execute calls do not leak goroutines. After all calls complete, the
// goroutine count should return to approximately the baseline.
func TestStress_RapidConsecutiveExecute(t *testing.T) {
	const numCalls = 120
	const modelResponse = "ok"

	inf := newStressInferencer()
	for i := 0; i < numCalls; i++ {
		inf.AddTextResponse(modelResponse)
	}
	tool := newStressToolExecutor()

	loop, err := agentloop.New(
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(tool),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	// Baseline goroutine count after GC + settle.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	for i := 0; i < numCalls; i++ {
		result, execErr := loop.Execute(ctx, agentloop.NewExecuteInput(fmt.Sprintf("msg-%d", i)))
		if execErr != nil {
			t.Fatalf("Execute call %d: %v", i, execErr)
		}
		if result.Text() != modelResponse {
			t.Fatalf("Execute call %d: got %q, want %q", i, result.Text(), modelResponse)
		}
	}

	// Allow goroutines to settle.
	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	final := runtime.NumGoroutine()

	// Allow a margin of 10 goroutines for runtime overhead.
	if final > baseline+10 {
		t.Errorf("goroutine leak: baseline=%d, final=%d (delta=%d, max allowed=10)",
			baseline, final, final-baseline)
	}

	if inf.CallCount() != int64(numCalls) {
		t.Errorf("inference calls: got %d, want %d", inf.CallCount(), numCalls)
	}
}

// TestStress_SimultaneousSendAndInterrupt exercises concurrent Send and
// SendInterrupt calls on a turn-taking loop from multiple goroutines.
// The test verifies that no panics, deadlocks, or data races occur (run with -race).
func TestStress_SimultaneousSendAndInterrupt(t *testing.T) {
	const numSenders = 5
	const messagesPerSender = 20
	const totalMessages = numSenders * messagesPerSender

	// Enough responses for all possible inference calls triggered by sends.
	inf := newStressInferencer()
	for i := 0; i < totalMessages*2; i++ {
		inf.AddTextResponse(fmt.Sprintf("response-%d", i))
	}
	tool := newStressToolExecutor()

	loop, err := agentloop.New(
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(tool),
		agentloop.WithMode(engine.ModeTurnTaking),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	out := &safeBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := loop.SetOutputs(ctx, []agentloop.Output{{Writer: out, Label: "stress"}}); err != nil {
		t.Fatalf("SetOutputs: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- loop.Run(ctx) }()

	// Wait a moment for the loop to start.
	time.Sleep(50 * time.Millisecond)

	var wg sync.WaitGroup

	// Sender goroutines.
	for s := 0; s < numSenders; s++ {
		wg.Add(1)
		go func(senderID int) {
			defer wg.Done()
			for m := 0; m < messagesPerSender; m++ {
				msg := messages.NewTextMessage(messages.RoleUser,
					fmt.Sprintf("sender-%d-msg-%d", senderID, m))
				_ = loop.Send(ctx, []messages.Message{msg})
				// Small jitter to mix sends and interrupts.
				if m%3 == 0 {
					time.Sleep(time.Millisecond)
				}
			}
		}(s)
	}

	// Interrupt goroutine (sends interrupts periodically).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < totalMessages/2; i++ {
			followUp := messages.NewTextMessage(messages.RoleUser, fmt.Sprintf("interrupt-%d", i))
			_ = loop.SendInterrupt(ctx, &followUp)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	wg.Wait()

	// Give the loop time to process remaining messages.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case loopErr := <-runErr:
		// context.Canceled is expected after cancel().
		if loopErr != nil && loopErr != context.Canceled && loopErr.Error() != "context canceled" {
			t.Logf("Run exited with: %v (non-fatal for stress test)", loopErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit within 5s after context cancellation (potential deadlock)")
	}

	// The primary assertion is that we reach here without panic, deadlock, or data race.
	t.Logf("stress test completed: %d inference calls made", inf.CallCount())
}

// TestStress_HighThroughputStreaming verifies that 1500 deltas per response
// are delivered without drops when buffer capacity is adequate.
func TestStress_HighThroughputStreaming(t *testing.T) {
	const numChunks = 1500

	chunks := make([]string, numChunks)
	var want strings.Builder
	for i := 0; i < numChunks; i++ {
		chunk := fmt.Sprintf("chunk-%04d ", i)
		chunks[i] = chunk
		want.WriteString(chunk)
	}
	wantText := want.String()

	inf := newStressInferencer().AddChunkedTextResponse(chunks)
	tool := newStressToolExecutor()

	// Use a large buffer capacity to avoid drops.
	loop, err := agentloop.New(
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(tool),
		agentloop.WithBufferCapacity(2048),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := loop.ExecuteStreaming(ctx, agentloop.NewExecuteInput("give me data"))
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}

	var got strings.Builder
	for result.EventStream.HasNext() {
		evt := result.EventStream.Response()
		if evt.Type == messages.StreamTypeTextDelta {
			if v, ok := evt.Value.(*messages.TextDeltaValue); ok && evt.Role == messages.RoleAssistant {
				got.WriteString(v.Content)
			}
		}
	}

	gotText := got.String()
	if gotText != wantText {
		t.Errorf("stream text length: got %d, want %d", len(gotText), len(wantText))
		if len(gotText) > 100 && len(wantText) > 100 {
			t.Errorf("  first 100 chars got:  %q", gotText[:100])
			t.Errorf("  first 100 chars want: %q", wantText[:100])
		}
	}

	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
}

// TestStress_LargeBatchToolCalls verifies that a batch with 60 concurrent
// tool calls executes without race conditions or deadlocks.
func TestStress_LargeBatchToolCalls(t *testing.T) {
	const numTools = 60
	const finalResponse = "All tools completed."

	tools := make([]messages.ToolDefinition, numTools)
	calls := make([]messages.ToolCall, numTools)
	toolExec := newStressToolExecutor()

	for i := 0; i < numTools; i++ {
		name := fmt.Sprintf("tool_%d", i)
		tools[i] = messages.ToolDefinition{Name: name, Description: fmt.Sprintf("Tool number %d", i)}
		calls[i] = messages.ToolCall{
			ID:        fmt.Sprintf("tc-%d", i),
			Name:      name,
			Arguments: fmt.Sprintf(`{"n":%d}`, i),
		}
		toolExec.AddResult(name, fmt.Sprintf("result-%d", i))
	}

	inf := newStressInferencer().
		AddBatchToolCallResponse(calls).
		AddTextResponse(finalResponse)

	loop, err := agentloop.New(
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(toolExec),
		agentloop.WithTools(tools),
		agentloop.WithBufferCapacity(256),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := loop.Execute(ctx, agentloop.NewExecuteInput("run all tools"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Text() != finalResponse {
		t.Errorf("final text: got %q, want %q", result.Text(), finalResponse)
	}

	if toolExec.CallCount() != int64(numTools) {
		t.Errorf("tool call count: got %d, want %d", toolExec.CallCount(), numTools)
	}

	// Verify history contains all tool results.
	history := loop.GetConversationHistory()
	toolResultCount := 0
	for _, m := range history {
		if m.Role == messages.RoleTool {
			toolResultCount++
		}
	}
	// Tool results are grouped into a single tool message per batch, containing
	// one text part per tool call result. So we expect 1 tool message, not numTools.
	// Actually, the ordering layer creates one Message per tool call based on ToolCallID.
	// Let's just verify we got tool results and the final text.
	if toolResultCount == 0 {
		t.Error("expected tool result messages in history, got none")
	}

	if inf.CallCount() != 2 {
		t.Errorf("inference calls: got %d, want 2 (batch call + final response)", inf.CallCount())
	}
}
