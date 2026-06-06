package openai

import "encoding/json"

// ── Request types ──────────────────────────────────────────────────────────

// chatRequest is the request body for the OpenAI chat completions API.
// All fields use the standard OpenAI schema; compatible APIs such as OpenRouter
// accept the same structure with extended content type support.
type chatRequest struct {
	Model            string         `json:"model"`
	Messages         []requestMsg   `json:"messages"`
	Tools            []chatTool     `json:"tools,omitempty"`
	MaxTokens        *int           `json:"max_tokens,omitempty"`
	Temperature      *float64       `json:"temperature,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	Stop             interface{}    `json:"stop,omitempty"` // string, []string, or nil
	Stream           bool           `json:"stream,omitempty"`
	StreamOptions    *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// requestMsg is a single message in the chat request.
// Content is a union of string or []contentPart; all content types are supported
// across all roles to allow maximum compatibility with third-party APIs.
type requestMsg struct {
	Role       string        `json:"role"`
	Content    *contentField `json:"content,omitempty"`
	Name       string        `json:"name,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall    `json:"tool_calls,omitempty"`
}

// contentField is the content of a message: either a plain string or an array of
// typed content parts. Custom JSON marshaling handles the union.
type contentField struct {
	str   *string
	parts []contentPart
}

func strContent(s string) *contentField            { return &contentField{str: &s} }
func arrContent(parts []contentPart) *contentField { return &contentField{parts: parts} }

func (c *contentField) MarshalJSON() ([]byte, error) {
	if c == nil || (c.str == nil && c.parts == nil) {
		return []byte("null"), nil
	}
	if c.str != nil {
		return json.Marshal(*c.str)
	}
	return json.Marshal(c.parts)
}

// contentPart is a single typed entry in a content array.
// All content types are represented here to support any OpenAI-compatible API.
type contentPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	Refusal    string      `json:"refusal,omitempty"`
	ImageURL   *imageURL   `json:"image_url,omitempty"`
	InputAudio *inputAudio `json:"input_audio,omitempty"`
	VideoURL   *videoURL   `json:"video_url,omitempty"`
	File       *fileAttach `json:"file,omitempty"`
}

type imageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type inputAudio struct {
	Data   string `json:"data"`   // base64-encoded audio bytes
	Format string `json:"format"` // "wav" or "mp3"
}

type videoURL struct {
	URL string `json:"url"`
}

type fileAttach struct {
	FileID   string `json:"file_id,omitempty"`
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"` // base64-encoded file bytes
}

// toolCall is a function call in an assistant message or response.
type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function toolCallFunc `json:"function"`
}

type toolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// chatTool defines a callable function in the request.
type chatTool struct {
	Type     string      `json:"type"` // always "function"
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ── Response types (non-streaming) ────────────────────────────────────────

type chatResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []responseChoice `json:"choices"`
	Usage   responseUsage    `json:"usage"`
}

type responseChoice struct {
	Index        int         `json:"index"`
	Message      responseMsg `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type responseMsg struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Refusal   string     `json:"refusal,omitempty"`
	ToolCalls []toolCall `json:"tool_calls"`
	Audio     *audioResp `json:"audio"`
}

type audioResp struct {
	ID         string `json:"id"`
	ExpiresAt  int64  `json:"expires_at"`
	Data       string `json:"data"` // base64-encoded audio
	Transcript string `json:"transcript"`
}

type responseUsage struct {
	PromptTokens            int                `json:"prompt_tokens"`
	CompletionTokens        int                `json:"completion_tokens"`
	TotalTokens             int                `json:"total_tokens"`
	CompletionTokensDetails *tokenUsageDetails `json:"completion_tokens_details,omitempty"`
}

type tokenUsageDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// ── Streaming types ────────────────────────────────────────────────────────

type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *responseUsage `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type streamDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	Refusal   string           `json:"refusal,omitempty"`
	Reasoning string           `json:"reasoning,omitempty"`
	Audio     *streamAudio     `json:"audio,omitempty"`
	ToolCalls []streamToolCall `json:"tool_calls,omitempty"`
}

type streamAudio struct {
	Data string `json:"data"` // base64-encoded audio chunk
}

type streamToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function streamToolCallFn `json:"function"`
}

type streamToolCallFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}
