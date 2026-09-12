// Package conversationlog reduces session stream observations into a durable,
// deterministic per-turn conversation log.
package conversationlog

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// MaxToolEventValueBytes is the maximum UTF-8 byte length retained for a
	// tool call's arguments or result content.
	MaxToolEventValueBytes = 64 * 1024
	// ToolEventTruncationSuffix is appended when a tool value exceeds its cap.
	ToolEventTruncationSuffix = "...[truncated]"

	toolEventTypeCall   = "tool_call"
	toolEventTypeResult = "tool_result"
	toolEventStatusDone = "completed"
	toolEventStatusFail = "failed"
)

// Service is the host-neutral conversation reducer contract. Implementations
// own all mutable reduction state and return detached snapshots. Observe's
// outbound argument identifies messages sent toward the provider; inputIndex
// and outputIndex are the already-persisted audio segment indices, or -1 when
// the message carried no recorded segment.
type Service interface {
	Observe(messages.StreamMessage, bool, int, int)
	ObserveToolCall(messages.ToolCall)
	ObserveToolResult(messages.ToolCall, messages.ToolCallResponse, bool, ...*ImageEvidence)
	Entries() []LogEntry
	TimingEntries() []TurnTiming
	JSONL() ([]byte, error)
}

// TurnInput describes the user side of one observed turn.
type TurnInput struct {
	Text          string   `json:"text"`
	AudioBytes    uint64   `json:"audio_bytes"`
	Committed     bool     `json:"committed"`
	AudioSegments []string `json:"audio_segments,omitempty"`
}

// TurnTiming carries run-specific wall-clock measurements. It is intentionally
// separate from LogEntry so deterministic JSONL remains byte-comparable.
type TurnTiming struct {
	TurnIndex            int    `json:"turn_index"`
	CommittedAt          string `json:"committed_at"`
	FirstResponseAudioMS int64  `json:"first_response_audio_ms,omitempty"`
}

// TurnResponse describes the assistant side of one observed turn.
type TurnResponse struct {
	Text          string   `json:"text"`
	Complete      bool     `json:"complete"`
	AudioBytes    uint64   `json:"audio_bytes"`
	AudioSegments []string `json:"audio_segments,omitempty"`
}

// LogEntry is one deterministic JSONL session-log record. Declaration order
// is part of the artifact contract.
type LogEntry struct {
	TurnIndex  int          `json:"turn_index"`
	Input      TurnInput    `json:"input"`
	Response   TurnResponse `json:"response"`
	ToolEvents []ToolEvent  `json:"tool_events,omitempty"`
}

// ToolEvent is one ordered tool call or result observed at the execution
// boundary. Result events always retain Content, including an empty result.
type ToolEvent struct {
	Sequence   uint64         `json:"sequence"`
	Type       string         `json:"type"`
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Arguments  string         `json:"arguments,omitempty"`
	Status     string         `json:"status,omitempty"`
	Content    string         `json:"content,omitempty"`
	Image      *ImageEvidence `json:"image,omitempty"`
}

// ImageEvidence describes a validated image artifact without embedding its
// pixels in session-log.jsonl.
type ImageEvidence struct {
	Path            string `json:"path"`
	Source          string `json:"source"`
	BrowserID       string `json:"browser_id,omitempty"`
	TargetID        string `json:"target_id,omitempty"`
	MIMEType        string `json:"mime_type"`
	ByteLength      int    `json:"byte_length"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	SHA256          string `json:"sha256"`
	TypedProjection string `json:"typed_projection"`
}

// MarshalJSON keeps call and result records' field presence distinct while
// preserving the historical field order and exact JSONL shape.
func (e ToolEvent) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case toolEventTypeCall:
		return json.Marshal(struct {
			Sequence   uint64 `json:"sequence"`
			Type       string `json:"type"`
			ToolCallID string `json:"tool_call_id"`
			ToolName   string `json:"tool_name"`
			Arguments  string `json:"arguments"`
		}{
			Sequence: e.Sequence, Type: e.Type, ToolCallID: e.ToolCallID,
			ToolName: e.ToolName, Arguments: e.Arguments,
		})
	case toolEventTypeResult:
		return json.Marshal(struct {
			Sequence   uint64         `json:"sequence"`
			Type       string         `json:"type"`
			ToolCallID string         `json:"tool_call_id"`
			ToolName   string         `json:"tool_name"`
			Status     string         `json:"status"`
			Content    string         `json:"content"`
			Image      *ImageEvidence `json:"image,omitempty"`
		}{
			Sequence: e.Sequence, Type: e.Type, ToolCallID: e.ToolCallID,
			ToolName: e.ToolName, Status: e.Status, Content: e.Content, Image: e.Image,
		})
	default:
		return json.Marshal(struct {
			Sequence   uint64         `json:"sequence"`
			Type       string         `json:"type"`
			ToolCallID string         `json:"tool_call_id"`
			ToolName   string         `json:"tool_name"`
			Arguments  string         `json:"arguments,omitempty"`
			Status     string         `json:"status,omitempty"`
			Content    string         `json:"content,omitempty"`
			Image      *ImageEvidence `json:"image,omitempty"`
		}{
			Sequence: e.Sequence, Type: e.Type, ToolCallID: e.ToolCallID,
			ToolName: e.ToolName, Arguments: e.Arguments, Status: e.Status,
			Content: e.Content, Image: e.Image,
		})
	}
}
