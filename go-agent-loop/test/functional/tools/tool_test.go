package tools

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// executeOrStream runs one turn with either Execute (non-streaming) or
// ExecuteStreaming (streaming). Used to run the same test assertions for both
// code paths so we catch regressions in either variant (e.g. stream_execute
// terminating early when deltas race execution). An async variant can be
// added later when that API is available.
//
// For both modes, finalText is the semantic "final answer" (last assistant
// text message with no tool calls), so assertions can use finalText == expected
// regardless of mode. The raw stream in streaming mode may include tool results;
// we derive finalText from Messages() for consistency with ExecuteResult.Text().
func executeOrStream(t *testing.T, s *Scenario, mode string, message string) (finalText string, turnMessages []messages.Message) {
	t.Helper()
	if mode == "stream" {
		finalText := s.ExecuteStreamingText(message)
		return finalText, turnMessages
	}
	result := s.Execute(message)
	return result.Text(), result.Messages
}

// ---------------------------------------------------------------------------
// Single tool use
// ---------------------------------------------------------------------------

// TestTool_SingleToolUse validates the canonical tool-use flow:
// request, then agent, then tool, then response.
//
//	loop.Execute("What is the weather in London?")
//	→ model calls get_weather → tool returns result → model gives final answer
//
// Assertions:
//   - The final text matches the mock final response.
//   - The turn produces three messages: assistant (tool call), tool result,
//     assistant (final text).
//   - The full conversation history is: user → assistant (tool call) →
//     tool result → assistant (final text).
//   - The delta buffer contains TOOLCALL.START and TOOLCALL.END events followed
//     by a TEXT.DELTA for the final response.
//   - The inferencer was called exactly twice (once per model turn).
//   - The tool executor was called exactly once with the expected tool name.
//
// Runs for both Execute and ExecuteStreaming to ensure the streaming path
// completes correctly (no early termination when deltas race execution).
func TestTool_SingleToolUse(t *testing.T) {
	const userMessage = "What is the weather in London?"
	const toolResult = "Sunny, 22°C"
	const finalResponse = "It's sunny in London, 22°C."

	tools := []messages.ToolDefinition{
		{Name: "get_weather", Description: "Get the current weather for a city"},
	}

	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			inf := new(MockInferencer).
				AddToolCallResponse("tc1", "get_weather", `{"city":"London"}`).
				AddTextResponse(finalResponse)
			tool := NewMockToolExecutor().AddResult("get_weather", toolResult)
			s := NewScenario(t, inf, tool, agentloop.WithTools(tools))

			finalText, _ := executeOrStream(t, s, mode, userMessage)

			// --- final text ---
			if finalText != finalResponse {
				t.Errorf("Text(): got %q, want %q", finalText, finalResponse)
			}
			// --- full conversation history ---
			AssertMessages(t, s.History(), []ExpectedMessage{
				{Role: messages.RoleUser, Text: userMessage},
				{Role: messages.RoleAssistant, HasToolCalls: true},
				{Role: messages.RoleTool, IsToolResult: true},
				{Role: messages.RoleAssistant, Text: finalResponse},
			})

			// --- delta stream: tool call events then final text delta ---
			AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
				{Type: messages.StreamTypeMessageStart, Role: messages.RoleUser},
				{Type: messages.StreamTypeToolCallStart},
				{Type: messages.StreamTypeToolCallEnd},
				{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: finalResponse},
			})

			// --- inference call count: 1 for tool call + 1 for final response ---
			if inf.CallCount() != 2 {
				t.Errorf("inference calls: got %d, want 2", inf.CallCount())
			}

			// --- tool executor was called once with the expected tool ---
			calls := tool.Calls()
			if len(calls) != 1 {
				t.Fatalf("tool call count: got %d, want 1", len(calls))
			}
			if calls[0].Name != "get_weather" {
				t.Errorf("tool name: got %q, want %q", calls[0].Name, "get_weather")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Multi-turn tool use
// ---------------------------------------------------------------------------

// TestTool_MultiTurnTool validates the sequential multi-tool flow:
// request, then agent, then tool, then agent, then tool, then response.
//
//	loop.Execute("What is the weather and time in London?")
//	→ model calls get_weather → tool result → model calls get_time
//	→ tool result → model gives final answer
//
// Assertions:
//   - The final text matches the mock final response.
//   - The turn produces five messages: assistant (tc1 call), tool result (tc1),
//     assistant (tc2 call), tool result (tc2), assistant (final text).
//   - The full history has six messages: user + those five.
//   - The inferencer was called exactly three times.
//   - The tool executor was called exactly twice (once per tool).
//
// Runs for both Execute and ExecuteStreaming.
func TestTool_MultiTurnTool(t *testing.T) {
	const userMessage = "What is the weather and time in London?"
	const weatherResult = "Sunny, 22°C"
	const timeResult = "14:30 UTC"
	const finalResponse = "London is sunny at 14:30 UTC."

	tools := []messages.ToolDefinition{
		{Name: "get_weather", Description: "Get the current weather for a city"},
		{Name: "get_time", Description: "Get the current time in a timezone"},
	}

	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			inf := new(MockInferencer).
				AddToolCallResponse("tc1", "get_weather", `{"city":"London"}`).
				AddToolCallResponse("tc2", "get_time", `{"timezone":"UTC"}`).
				AddTextResponse(finalResponse)
			tool := NewMockToolExecutor().
				AddResult("get_weather", weatherResult).
				AddResult("get_time", timeResult)
			s := NewScenario(t, inf, tool, agentloop.WithTools(tools))

			finalText, _ := executeOrStream(t, s, mode, userMessage)

			// --- final text ---
			if finalText != finalResponse {
				t.Errorf("Text(): got %q, want %q", finalText, finalResponse)
			}

			// --- full conversation history ---
			AssertMessages(t, s.History(), []ExpectedMessage{
				{Role: messages.RoleUser, Text: userMessage},
				{Role: messages.RoleAssistant, HasToolCalls: true},
				{Role: messages.RoleTool, IsToolResult: true},
				{Role: messages.RoleAssistant, HasToolCalls: true},
				{Role: messages.RoleTool, IsToolResult: true},
				{Role: messages.RoleAssistant, Text: finalResponse},
			})

			// --- delta stream: two rounds of tool call events then final text ---
			AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
				{Type: messages.StreamTypeMessageStart, Role: messages.RoleUser},
				{Type: messages.StreamTypeToolCallStart},
				{Type: messages.StreamTypeToolCallEnd},
				{Type: messages.StreamTypeToolCallStart},
				{Type: messages.StreamTypeToolCallEnd},
				{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: finalResponse},
			})

			// --- inference call count: 3 (tool1 call, tool2 call, final response) ---
			if inf.CallCount() != 3 {
				t.Errorf("inference calls: got %d, want 3", inf.CallCount())
			}

			// --- tool executor was called twice ---
			calls := tool.Calls()
			if len(calls) != 2 {
				t.Fatalf("tool call count: got %d, want 2", len(calls))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Batch tool use
// ---------------------------------------------------------------------------

// TestTool_BatchToolCall validates the parallel batch-tool flow:
// request, then agent, then tool (batch), then response.
//
//	loop.Execute("What is the weather and time in London?")
//	→ model calls [get_weather, get_time] in one response
//	→ both tools execute (in parallel) → model gives final answer
//
// Assertions:
//   - The final text matches the mock final response.
//   - The turn produces four messages: assistant (batch tool call with 2 calls),
//     tool result (tc1), tool result (tc2), assistant (final text).
//   - The full history has five messages: user + those four.
//   - The inferencer was called exactly twice.
//   - The tool executor was called exactly twice, with both expected tool names.
//
// Runs for both Execute and ExecuteStreaming.
func TestTool_BatchToolCall(t *testing.T) {
	const userMessage = "What is the weather and time in London?"
	const weatherResult = "Sunny, 22°C"
	const timeResult = "14:30 UTC"
	const finalResponse = "London is sunny at 14:30 UTC."

	tools := []messages.ToolDefinition{
		{Name: "get_weather", Description: "Get the current weather for a city"},
		{Name: "get_time", Description: "Get the current time in a timezone"},
	}

	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			inf := new(MockInferencer).
				AddBatchToolCallResponse([]messages.ToolCall{
					{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`},
					{ID: "tc2", Name: "get_time", Arguments: `{"timezone":"UTC"}`},
				}).
				AddTextResponse(finalResponse)
			tool := NewMockToolExecutor().
				AddResult("get_weather", weatherResult).
				AddResult("get_time", timeResult)
			s := NewScenario(t, inf, tool, agentloop.WithTools(tools))

			finalText, _ := executeOrStream(t, s, mode, userMessage)

			// --- final text ---
			if finalText != finalResponse {
				t.Errorf("Text(): got %q, want %q", finalText, finalResponse)
			}

			// --- full conversation history ---
			AssertMessages(t, s.History(), []ExpectedMessage{
				{Role: messages.RoleUser, Text: userMessage},
				{Role: messages.RoleAssistant, HasToolCalls: true},
				{Role: messages.RoleTool, IsToolResult: true},
				{Role: messages.RoleTool, IsToolResult: true},
				{Role: messages.RoleAssistant, Text: finalResponse},
			})

			// --- delta stream: batch tool call events then final text ---
			AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
				{Type: messages.StreamTypeMessageStart, Role: messages.RoleUser},
				{Type: messages.StreamTypeToolCallStart},
				{Type: messages.StreamTypeToolCallEnd},
				{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: finalResponse},
			})

			// --- inference call count: 2 (batch call + final response) ---
			if inf.CallCount() != 2 {
				t.Errorf("inference calls: got %d, want 2", inf.CallCount())
			}

			// --- tool executor was called twice (once per tool in the batch) ---
			calls := tool.Calls()
			if len(calls) != 2 {
				t.Fatalf("tool call count: got %d, want 2", len(calls))
			}

			// Verify both tool names appear in the call log (order may vary due to parallelism).
			seenNames := make(map[string]bool)
			for _, call := range calls {
				seenNames[call.Name] = true
			}
			for _, want := range []string{"get_weather", "get_time"} {
				if !seenNames[want] {
					t.Errorf("tool %q was not called", want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tool result with image (non-text) content in history
// ---------------------------------------------------------------------------

// TestTool_ToolResultWithImageContentInHistory ensures that when a tool returns
// only image content (no text), the assembled tool message in conversation
// history includes the ImagePart so the model receives it on the next turn.
func TestTool_ToolResultWithImageContentInHistory(t *testing.T) {
	const userMessage = "Take a screenshot."
	const finalResponse = "I see the screenshot."
	imageBytes := []byte("fake-png-data")
	mediaType := "image/png"

	tools := []messages.ToolDefinition{
		{Name: "screenshot", Description: "Capture a screenshot"},
	}

	inf := new(MockInferencer).
		AddToolCallResponse("tc1", "screenshot", `{}`).
		AddTextResponse(finalResponse)
	tool := NewMockToolExecutor().
		SetToolResponse("screenshot", messages.ToolCallResponse{
			ContentParts: []messages.ContentPart{
				messages.ImagePart{Bytes: imageBytes, MediaType: mediaType},
			},
		})
	s := NewScenario(t, inf, tool, agentloop.WithTools(tools))

	result := s.Execute(userMessage)
	if result.Text() != finalResponse {
		t.Errorf("Text(): got %q, want %q", result.Text(), finalResponse)
	}

	history := s.History()
	var toolMsg *messages.Message
	for i := range history {
		if history[i].Role == messages.RoleTool {
			toolMsg = &history[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("no RoleTool message in history")
	}
	// Tool message must contain the image content (not only empty text).
	hasImage := false
	for _, p := range toolMsg.ContentParts {
		if ip, ok := p.(messages.ImagePart); ok && len(ip.Bytes) > 0 {
			hasImage = true
			if ip.MediaType != mediaType {
				t.Errorf("tool message ImagePart MediaType: got %q, want %q", ip.MediaType, mediaType)
			}
			if string(ip.Bytes) != string(imageBytes) {
				t.Errorf("tool message ImagePart Bytes: mismatch")
			}
			break
		}
	}
	if !hasImage {
		t.Error("tool message in history should contain ImagePart when tool returns image-only content")
	}
}

// TestTool_ToolCallsInModelMessageRecordedInHistory ensures that when the model
// returns a message with tool calls (TOOLCALL.START / DELTA / END), the
// assembled assistant message in conversation history includes the ToolCalls
// slice with correct ID, Name, and Arguments.
func TestTool_ToolCallsInModelMessageRecordedInHistory(t *testing.T) {
	const userMessage = "What is the weather in London?"
	const toolID = "tc1"
	const toolName = "get_weather"
	const toolArgs = `{"city":"London"}`
	const toolResult = "Sunny, 22°C"
	const finalResponse = "It's sunny in London, 22°C."

	tools := []messages.ToolDefinition{
		{Name: toolName, Description: "Get the current weather for a city"},
	}

	inf := new(MockInferencer).
		AddToolCallResponse(toolID, toolName, toolArgs).
		AddTextResponse(finalResponse)
	tool := NewMockToolExecutor().AddResult(toolName, toolResult)
	s := NewScenario(t, inf, tool, agentloop.WithTools(tools))

	s.Execute(userMessage)

	history := s.History()
	var assistantWithToolCalls *messages.Message
	for i := range history {
		m := &history[i]
		if m.Role == messages.RoleAssistant && len(m.ToolCalls) > 0 {
			assistantWithToolCalls = m
			break
		}
	}
	if assistantWithToolCalls == nil {
		t.Fatal("conversation history should contain an assistant message with tool calls")
	}
	if len(assistantWithToolCalls.ToolCalls) < 1 {
		t.Fatalf("assistant message ToolCalls: got %d, want at least 1", len(assistantWithToolCalls.ToolCalls))
	}
	tc := assistantWithToolCalls.ToolCalls[0]
	if tc.ID != toolID {
		t.Errorf("ToolCalls[0].ID: got %q, want %q", tc.ID, toolID)
	}
	if tc.Name != toolName {
		t.Errorf("ToolCalls[0].Name: got %q, want %q", tc.Name, toolName)
	}
	if tc.Arguments != toolArgs {
		t.Errorf("ToolCalls[0].Arguments: got %q, want %q", tc.Arguments, toolArgs)
	}
}
