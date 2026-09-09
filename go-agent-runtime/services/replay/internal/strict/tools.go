package strict

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

type recordedTool struct {
	call     messages.ToolCall
	response messages.ToolCallResponse
	failed   bool
	used     bool
}

type recordedToolExecutor struct {
	mu    sync.Mutex
	calls map[string]*recordedTool
}

func newRecordedToolExecutor() *recordedToolExecutor {
	return &recordedToolExecutor{calls: make(map[string]*recordedTool)}
}

func (e *recordedToolExecutor) addCall(call messages.ToolCall) error {
	if call.ID == "" || call.Name == "" || !json.Valid([]byte(call.Arguments)) {
		return fmt.Errorf("%w: invalid call %q", replay.ErrBundleIncomplete, call.ID)
	}
	if _, exists := e.calls[call.ID]; exists {
		return fmt.Errorf("%w: duplicate tool call %q", replay.ErrBundleMismatch, call.ID)
	}
	e.calls[call.ID] = &recordedTool{call: call}
	return nil
}

func (e *recordedToolExecutor) addResult(result decodedToolResult) error {
	entry, exists := e.calls[result.callID]
	if !exists {
		return fmt.Errorf("%w: result for unknown call %q", replay.ErrBundleMismatch, result.callID)
	}
	if entry.response.ToolCallID != "" || entry.failed {
		return fmt.Errorf("%w: duplicate tool result %q", replay.ErrBundleMismatch, result.callID)
	}
	if result.name != entry.call.Name {
		return fmt.Errorf("%w: call %q name %q != %q", replay.ErrBundleMismatch, result.callID, result.name, entry.call.Name)
	}
	result.response.ToolCallID = result.callID
	if result.response.Name == "" {
		result.response.Name = result.name
	}
	entry.response, entry.failed = result.response, result.failed
	return nil
}

func (e *recordedToolExecutor) validateShape() error {
	for id, entry := range e.calls {
		if entry.response.ToolCallID == "" && !entry.failed {
			return fmt.Errorf("%w: tool call %q has no result", replay.ErrBundleIncomplete, id)
		}
	}
	return nil
}

func (e *recordedToolExecutor) ExpectedToolCalls() int {
	if e == nil {
		return -1
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (e *recordedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if err := contextError(ctx); err != nil {
		return messages.ToolCallResponse{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, ok := e.calls[call.ID]
	if !ok || entry.call.Name != call.Name || !sameJSON(entry.call.Arguments, call.Arguments) {
		return messages.ToolCallResponse{}, fmt.Errorf("%w: call id=%q name=%q", replay.ErrToolMismatch, call.ID, call.Name)
	}
	if entry.used {
		return messages.ToolCallResponse{}, fmt.Errorf("%w: duplicate call %q", replay.ErrToolMismatch, call.ID)
	}
	entry.used = true
	if entry.failed {
		return entry.response, fmt.Errorf("%w: call %q", replay.ErrToolFailure, call.ID)
	}
	return entry.response, nil
}

func sameJSON(expected, actual string) bool {
	var left, right any
	return json.Unmarshal([]byte(expected), &left) == nil && json.Unmarshal([]byte(actual), &right) == nil && jsonEqual(left, right)
}

func jsonEqual(left, right any) bool {
	return bytes.Equal(mustJSON(left), mustJSON(right))
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

type decodedToolResult struct {
	callID, name string
	response     messages.ToolCallResponse
	failed       bool
}

func decodeToolCall(payload []byte) (messages.ToolCall, error) {
	var raw struct {
		ID             string `json:"ID"`
		Name           string `json:"Name"`
		Arguments      string `json:"Arguments"`
		LowerID        string `json:"id"`
		LowerName      string `json:"name"`
		LowerArguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return messages.ToolCall{}, err
	}
	if raw.ID == "" {
		raw.ID = raw.LowerID
	}
	if raw.Name == "" {
		raw.Name = raw.LowerName
	}
	if raw.Arguments == "" {
		raw.Arguments = raw.LowerArguments
	}
	return messages.ToolCall{ID: raw.ID, Name: raw.Name, Arguments: raw.Arguments}, nil
}

func decodeToolResult(payload []byte) (decodedToolResult, error) {
	var raw struct {
		CallID   string `json:"call_id"`
		Name     string `json:"name"`
		Failed   bool   `json:"failed"`
		Response struct {
			ToolCallID      string `json:"ToolCallID"`
			LowerToolCallID string `json:"tool_call_id"`
			Name            string `json:"Name"`
			LowerName       string `json:"name"`
			Content         string `json:"Content"`
			LowerContent    string `json:"content"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return decodedToolResult{}, err
	}
	if raw.CallID == "" {
		return decodedToolResult{}, errors.New("call_id is empty")
	}
	if raw.Response.ToolCallID == "" {
		raw.Response.ToolCallID = raw.Response.LowerToolCallID
	}
	if raw.Response.Name == "" {
		raw.Response.Name = raw.Response.LowerName
	}
	if raw.Response.Content == "" {
		raw.Response.Content = raw.Response.LowerContent
	}
	return decodedToolResult{callID: raw.CallID, name: raw.Name, failed: raw.Failed, response: messages.ToolCallResponse{ToolCallID: raw.Response.ToolCallID, Name: raw.Response.Name, Content: raw.Response.Content}}, nil
}

var _ messages.ToolExecutor = (*recordedToolExecutor)(nil)
