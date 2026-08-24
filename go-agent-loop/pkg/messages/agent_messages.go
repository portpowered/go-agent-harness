package messages

// This package denotes the messages that are sent from the participants to the agent loop.
// There are two classes of messages: (deltas, and full messages)
// Deltas are for incremental changes to the conversation, and full messages are for complete messages.

// Full messages

// Role represents who authored a message in the conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// ContentPart is one segment of message content (	text, image, audio, etc.).
// Implementations are sealed via contentPart().
type ContentPart interface {
	contentPart()
}

type ControlPlanePart struct {
	ControlPlaneMessageType ControlPlaneMessageType
}

func (ControlPlanePart) contentPart() {}

type ControlPlaneMessageType string

const (
	ControlPlaneMessageTypeStop         ControlPlaneMessageType = "stop"
	ControlPlaneMessageTypePause        ControlPlaneMessageType = "pause"
	ControlPlaneMessageTypeResume       ControlPlaneMessageType = "resume"
	ControlPlaneMessageTypeInterrupt    ControlPlaneMessageType = "interrupt"
	ControlPlaneMessageTypeSessionClose ControlPlaneMessageType = "session_close"
	ControlPlaneMessageTypePing         ControlPlaneMessageType = "ping"
)

// TextPart is plain text content.
type TextPart struct {
	Text string
}

func (TextPart) contentPart() {}

// ImagePart is image content, either by URL or inline bytes.
type ImagePart struct {
	URL       string // optional: image URL
	Bytes     []byte // optional: inline image data
	MediaType string // e.g. "image/png", "image/jpeg"
}

func (ImagePart) contentPart() {}

// AudioPart is audio content, either by URL or inline bytes.
type AudioPart struct {
	URL       string // optional: audio URL
	Bytes     []byte // optional: inline audio data
	MediaType string // e.g. "audio/mpeg", "audio/wav"
}

func (AudioPart) contentPart() {}

// TranscriptPart is ASR (automatic speech recognition) text output.
// Separate from TextPart because transcript text may be imperfect
// (ASR output) while TextPart is LLM-generated text.
type TranscriptPart struct {
	Text string // accumulated ASR transcript text
}

func (TranscriptPart) contentPart() {}

// VideoPart is video content, either by URL or inline bytes.
type VideoPart struct {
	URL       string // optional: video URL
	Bytes     []byte // optional: inline video data
	MediaType string // e.g. "video/mp4", "video/h264"
}

func (VideoPart) contentPart() {}

// FilePart is arbitrary file content with a MIME type and optional filename.
type FilePart struct {
	URL       string // optional: file URL
	Bytes     []byte // optional: inline file data
	Name      string // optional: filename
	MediaType string // e.g. "application/pdf", "text/csv"
}

func (FilePart) contentPart() {}

// EmbeddingPart is embedding content (e.g. vectors in safetensors format), either by URL or inline bytes.
type EmbeddingPart struct {
	URL       string // optional: embedding file URL (e.g. .safetensors)
	Bytes     []byte // optional: inline embedding data
	MediaType string // e.g. "application/vnd.safetensors", "application/octet-stream"
}

func (EmbeddingPart) contentPart() {}

// UsageInfoPart is a content part for a system-information message that carries token usage
// for a specific response. Use with RoleSystem to form a message that records usage for a turn.
type UsageInfoPart struct {
	Usage TokenUsage
}

func (UsageInfoPart) contentPart() {}

// Message is a single entry in the conversation history.
// Content is expressed as one or more ContentParts (text, image, audio) to support multimodal protocols.
//
// # IMPORTANT — Field Conversion Paths
//
// Every exported field on this struct must be handled in three places when a new
// field is added. Failure to do so silently drops the field during streaming
// reconstruction (as happened with the Refusal field).
//
//  1. responseToMessage()               — go-llm-gateway: converts a provider's
//     non-streaming response into a Message.
//  2. streamSSEToGateway()              — go-llm-gateway: maps provider SSE
//     events into StreamMessage deltas for each field.
//  3. ReconstructModelMessageFromDeltas — go-agent-loop (this package): reassembles
//     a Message from StreamMessage deltas.
//
// A round-trip field-preservation test in reconstruction_roundtrip_test.go acts as
// the safety net: it uses reflect to enumerate every exported field and will fail
// if a field is not accounted for.
type Message struct {
	Role         Role
	ContentParts []ContentPart // multimodal content; use TextContent() for plain-text view
	Refusal      string        // non-empty when the model refused the request (OpenAI `refusal` field)
	ToolCalls    []ToolCall
	ToolCallID   string // set when Role == RoleTool, references the originating ToolCall.ID
	Name         string // optional name for tool results
	Index        *int   // Position of the message in the conversation, if its nil, we presume that its supposed to be added to the next message in the step.

	// Ordering: global consistency for retries and interrupts (see ORDERING.md).
	GlobalIndex        int           // Global index in the agent loop; assigned by the engine.
	ActorProvidedID    string        // Unique identifier from the actor that sent this message.
	ActorProvidedIndex int           // Index as understood by the actor that sent it.
	ActorStreamID      string        // Unique identifier for the stream of messages from this actor.
	ActorID            ParticipantID // Participant that produced this message (Model, User, Tool, etc.).
}

// TextContent returns the concatenation of all text parts. Use for a plain-text view of the message.
func (m *Message) TextContent() string {
	if m == nil {
		return ""
	}
	var out string
	for _, p := range m.ContentParts {
		if t, ok := p.(TextPart); ok {
			out += t.Text
		}
	}
	return out
}

func (m *Message) ReasoningContent() string {
	if m == nil {
		return ""
	}
	for _, p := range m.ContentParts {
		if t, ok := p.(ReasoningPart); ok {
			return t.Reasoning
		}
	}
	return ""
}

// NewTextMessage builds a message with a single text part. Convenience for text-only content.
func NewTextMessage(role Role, text string) Message {
	return Message{
		Role:         role,
		ContentParts: []ContentPart{TextPart{Text: text}},
	}
}

// NewReason
func NewReasoningMessage(role Role, reasoning string) Message {
	return Message{
		Role:         role,
		ContentParts: []ContentPart{ReasoningPart{Reasoning: reasoning}},
	}
}

// NewReasoningPart returns a ContentPart for reasoning.
func NewReasoningPart(reasoning string) ReasoningPart { return ReasoningPart{Reasoning: reasoning} }

// ReasoningPart is reasoning content.
type ReasoningPart struct {
	Reasoning string
}

func (ReasoningPart) contentPart() {}

// NewTextPart returns a ContentPart for plain text.
func NewTextPart(text string) TextPart { return TextPart{Text: text} }

// HasText returns true if the message has at least one text part.
func (m *Message) HasText() bool {
	if m == nil {
		return false
	}
	for _, p := range m.ContentParts {
		if _, ok := p.(TextPart); ok {
			return true
		}
	}
	return false
}

// HasOnlyReasoning returns true if the message contains only reasoning content
// (no tool calls, no text, no images, no audio). Such messages are internal
// model "thinking" and should not trigger user output or tool execution.
func (m *Message) HasOnlyReasoning() bool {
	if m == nil {
		return false
	}
	if len(m.ToolCalls) > 0 {
		return false
	}
	hasReasoning := false
	for _, p := range m.ContentParts {
		switch p.(type) {
		case ReasoningPart:
			hasReasoning = true
		case TextPart, ImagePart, AudioPart, VideoPart, FilePart, EmbeddingPart:
			return false // has user-facing content
		case UsageInfoPart:
			// system info (e.g. token usage); not user-facing, not reasoning
		}
	}
	return hasReasoning
}
