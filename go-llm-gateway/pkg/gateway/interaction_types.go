package gateway

import (
	"encoding/json"
	"time"
)

// InteractionRequest is the provider-neutral input contract for a logical
// model interaction. Provider adapters translate this shape into their
// provider-specific request bodies.
type InteractionRequest struct {
	InteractionID        string                     `json:"interactionId,omitempty"`
	ContinueFromSequence int64                      `json:"continueFromSequence,omitempty"`
	Provider             string                     `json:"provider,omitempty"`
	Model                string                     `json:"model,omitempty"`
	SystemInstructions   []string                   `json:"systemInstructions,omitempty"`
	Messages             []InteractionMessage       `json:"messages,omitempty"`
	Tools                []InteractionTool          `json:"tools,omitempty"`
	ToolResults          []InteractionToolResult    `json:"toolResults,omitempty"`
	Metadata             map[string]json.RawMessage `json:"metadata,omitempty"`
	Config               json.RawMessage            `json:"config,omitempty"`
}

// InteractionMessage is a provider-neutral conversation item.
type InteractionMessage struct {
	Role         InteractionRole            `json:"role"`
	ContentParts []InteractionContent       `json:"contentParts,omitempty"`
	ToolCalls    []InteractionToolCall      `json:"toolCalls,omitempty"`
	ToolCallID   string                     `json:"toolCallId,omitempty"`
	Name         string                     `json:"name,omitempty"`
	Metadata     map[string]json.RawMessage `json:"metadata,omitempty"`
}

// InteractionRole identifies the author of an interaction message.
type InteractionRole string

const (
	InteractionRoleUser      InteractionRole = "user"
	InteractionRoleAssistant InteractionRole = "assistant"
	InteractionRoleTool      InteractionRole = "tool"
	InteractionRoleSystem    InteractionRole = "system"
)

// InteractionContent is a provider-neutral message content part.
type InteractionContent struct {
	Type      InteractionContentType     `json:"type"`
	Text      string                     `json:"text,omitempty"`
	URL       string                     `json:"url,omitempty"`
	Bytes     []byte                     `json:"bytes,omitempty"`
	MediaType string                     `json:"mediaType,omitempty"`
	Name      string                     `json:"name,omitempty"`
	Metadata  map[string]json.RawMessage `json:"metadata,omitempty"`
}

// InteractionContentType identifies the kind of content in a message part.
type InteractionContentType string

const (
	InteractionContentText  InteractionContentType = "text"
	InteractionContentImage InteractionContentType = "image"
	InteractionContentAudio InteractionContentType = "audio"
	InteractionContentVideo InteractionContentType = "video"
	InteractionContentFile  InteractionContentType = "file"
)

// InteractionTool declares a callable tool available to the model.
type InteractionTool struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Parameters  []InteractionToolParameter `json:"parameters,omitempty"`
	Metadata    map[string]json.RawMessage `json:"metadata,omitempty"`
}

// InteractionToolParameter declares one provider-neutral tool parameter.
type InteractionToolParameter struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// InteractionEventType identifies the normalized observable event kind.
type InteractionEventType string

const (
	InteractionEventStart              InteractionEventType = "interaction.start"
	InteractionEventTextDelta          InteractionEventType = "text.delta"
	InteractionEventFinalMessage       InteractionEventType = "message.final"
	InteractionEventToolCallRequest    InteractionEventType = "tool.call.request"
	InteractionEventToolResultAccepted InteractionEventType = "tool.result.accepted"
	InteractionEventUsage              InteractionEventType = "usage"
	InteractionEventError              InteractionEventType = "error"
	InteractionEventCancellation       InteractionEventType = "cancellation"
	InteractionEventEnd                InteractionEventType = "interaction.end"
)

// InteractionEvent is the normalized event emitted for a model interaction.
type InteractionEvent struct {
	InteractionID string                     `json:"interactionId"`
	Sequence      int64                      `json:"sequence"`
	Type          InteractionEventType       `json:"type"`
	Provider      string                     `json:"provider,omitempty"`
	Model         string                     `json:"model,omitempty"`
	CreatedAt     *time.Time                 `json:"createdAt,omitempty"`
	Correlation   InteractionCorrelation     `json:"correlation,omitempty"`
	TextDelta     *TextDeltaEvent            `json:"textDelta,omitempty"`
	FinalMessage  *InteractionMessage        `json:"finalMessage,omitempty"`
	ToolCall      *InteractionToolCall       `json:"toolCall,omitempty"`
	ToolResult    *InteractionToolResult     `json:"toolResult,omitempty"`
	Usage         *InteractionUsage          `json:"usage,omitempty"`
	Error         *InteractionError          `json:"error,omitempty"`
	Cancellation  *InteractionCancellation   `json:"cancellation,omitempty"`
	Metadata      map[string]json.RawMessage `json:"metadata,omitempty"`
}

// InteractionCorrelation carries stable IDs used to associate related events.
type InteractionCorrelation struct {
	MessageID  string `json:"messageId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
}

// TextDeltaEvent is the payload for text delta events.
type TextDeltaEvent struct {
	Content string `json:"content"`
}

// InteractionToolCall is a normalized model request to invoke a tool.
type InteractionToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// InteractionToolResult is a normalized caller-provided tool result accepted by
// the gateway for continuation.
type InteractionToolResult struct {
	ToolCallID string          `json:"toolCallId"`
	Name       string          `json:"name,omitempty"`
	Content    string          `json:"content,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// InteractionUsage contains provider-neutral token accounting when available.
type InteractionUsage struct {
	InputTokens  int64                      `json:"inputTokens,omitempty"`
	OutputTokens int64                      `json:"outputTokens,omitempty"`
	TotalTokens  int64                      `json:"totalTokens,omitempty"`
	Details      map[string]json.RawMessage `json:"details,omitempty"`
}

// InteractionError contains normalized provider or gateway failure details.
type InteractionError struct {
	Code      string                     `json:"code"`
	Message   string                     `json:"message"`
	Retryable bool                       `json:"retryable,omitempty"`
	Details   map[string]json.RawMessage `json:"details,omitempty"`
}

// InteractionCancellation describes a normalized cancellation event.
type InteractionCancellation struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}
