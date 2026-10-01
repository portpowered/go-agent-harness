package media

import (
	"context"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// MockToolExecutor
// ---------------------------------------------------------------------------

// MockToolExecutor is a configurable test double for messages.ToolExecutor.
// Use AddResult to configure per-tool responses. CallLog records every invocation.
// Use SetToolResponse to return rich ContentParts (e.g. ImagePart) for a tool; it takes precedence over AddResult.
// Safe for concurrent use from ToolRunner's parallel executeBatch.
type MockToolExecutor struct {
	mu            sync.Mutex
	Results       map[string]string
	CustomResults map[string]messages.ToolCallResponse // keyed by tool name; ToolCallID is set from call.ID at Execute time
	CallLog       []messages.ToolCall
	// callSignal is closed and cleared by the next Execute so waiters block
	// on a signal instead of polling.
	callSignal chan struct{}
}

// NewMockToolExecutor returns an empty MockToolExecutor ready for use.
func NewMockToolExecutor() *MockToolExecutor {
	return &MockToolExecutor{
		Results:       make(map[string]string),
		CustomResults: make(map[string]messages.ToolCallResponse),
	}
}

// AddResult registers a string result for the named tool.
func (m *MockToolExecutor) AddResult(toolName, result string) *MockToolExecutor {
	m.mu.Lock()
	m.Results[toolName] = result
	m.mu.Unlock()
	return m
}

// SetToolResponse registers a full ToolCallResponse for the named tool (e.g. with ContentParts for image/audio).
// ToolCallID is filled from the call at Execute time.
func (m *MockToolExecutor) SetToolResponse(toolName string, resp messages.ToolCallResponse) *MockToolExecutor {
	m.mu.Lock()
	m.CustomResults[toolName] = resp
	m.mu.Unlock()
	return m
}

// Calls returns a snapshot of tool calls observed by Execute.
func (m *MockToolExecutor) Calls() []messages.ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := make([]messages.ToolCall, len(m.CallLog))
	copy(calls, m.CallLog)
	return calls
}

// WaitForCalls blocks until at least want calls were observed or timeout
// elapses, and returns the calls observed so far.
func (m *MockToolExecutor) WaitForCalls(want int, timeout time.Duration) []messages.ToolCall {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		calls := append([]messages.ToolCall(nil), m.CallLog...)
		if m.callSignal == nil {
			m.callSignal = make(chan struct{})
		}
		signal := m.callSignal
		m.mu.Unlock()
		if len(calls) >= want {
			return calls
		}
		select {
		case <-signal:
		case <-deadline.C:
			return calls
		}
	}
}

// Execute implements messages.ToolExecutor.
func (m *MockToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	m.mu.Lock()
	m.CallLog = append(m.CallLog, call)
	if m.callSignal != nil {
		close(m.callSignal)
		m.callSignal = nil
	}
	if custom, ok := m.CustomResults[call.Name]; ok {
		custom.ToolCallID = call.ID
		m.mu.Unlock()
		return custom, nil
	}
	content := m.Results[call.Name]
	m.mu.Unlock()
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Content:    content,
	}, nil
}
