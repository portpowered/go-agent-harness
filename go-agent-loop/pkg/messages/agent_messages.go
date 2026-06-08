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

// ToolCall represents a request from the model to invoke a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON-encoded arguments
}

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

// ToolDefinition describes a tool available to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  []ToolParameter
}

// ToolParameter describes a single parameter of a tool.
type ToolParameter struct {
	Name        string
	Type        string // "string", "number", "boolean", "object"
	Description string
	Required    bool
}

// Deltas

// StreamMessageType denotes the phase and content kind of a stream message (e.g. TEXT.DELTA, TOOLCALL.END).
// Each content kind (TEXT, TOOLCALL, AUDIO, IMAGE) has START, DELTA, END. MESSAGE_END and ERROR are top-level.
type StreamMessageType string

// The lifecycle of an event goes as follows:

// For a basic text message:
// 1. MESSAGE.START
// 2. TEXT.START
// 3. TEXT.DELTA
// 4. TEXT.END
// 5 .MESSSAGE.END

// For a tool call:
// 1. MESSAGE.START
// 2. TOOLCALL.START
// 3. TOOLCALL.DELTA
// 4. TOOLCALL.END
// 5 .MESSSAGE.END

// For an audio message:
// 1. MESSAGE.START
// 2. AUDIO.START
// 3. AUDIO.DELTA
// 4. AUDIO.END
// 5 .MESSSAGE.END

// For reasoning/thinking tokens (e.g. from OpenRouter/DeepInfra models):
// 1. MESSAGE.START
// 2. REASONING.START
// 3. REASONING.DELTA
// 4. REASONING.END
// 5. (optional TEXT.AUDIO.etc)
// 6. MESSAGE.END

// For an embedding (e.g. safetensors):
// 1. MESSAGE.START
// 2. EMBEDDING.START
// 3. EMBEDDING.DELTA
// 4. EMBEDDING.END
// 5. MESSAGE.END

const (
	StreamTypeMessageStart StreamMessageType = "MESSAGE.START"
	StreamTypeMessageEnd   StreamMessageType = "MESSAGE.END"

	StreamTypeTextStart StreamMessageType = "TEXT.START"
	StreamTypeTextDelta StreamMessageType = "TEXT.DELTA"
	StreamTypeTextEnd   StreamMessageType = "TEXT.END"

	StreamTypeToolCallStart StreamMessageType = "TOOLCALL.START"
	StreamTypeToolCallDelta StreamMessageType = "TOOLCALL.DELTA"
	StreamTypeToolCallEnd   StreamMessageType = "TOOLCALL.END"

	StreamTypeAudioStart StreamMessageType = "AUDIO.START"
	StreamTypeAudioDelta StreamMessageType = "AUDIO.DELTA"
	StreamTypeAudioEnd   StreamMessageType = "AUDIO.END"

	StreamTypeImageStart StreamMessageType = "IMAGE.START"
	StreamTypeImageDelta StreamMessageType = "IMAGE.DELTA"
	StreamTypeImageEnd   StreamMessageType = "IMAGE.END"

	StreamTypeVideoStart StreamMessageType = "VIDEO.START"
	StreamTypeVideoDelta StreamMessageType = "VIDEO.DELTA"
	StreamTypeVideoEnd   StreamMessageType = "VIDEO.END"

	StreamTypeFileStart StreamMessageType = "FILE.START"
	StreamTypeFileDelta StreamMessageType = "FILE.DELTA"
	StreamTypeFileEnd   StreamMessageType = "FILE.END"

	StreamTypeEmbeddingStart StreamMessageType = "EMBEDDING.START"
	StreamTypeEmbeddingDelta StreamMessageType = "EMBEDDING.DELTA"
	StreamTypeEmbeddingEnd   StreamMessageType = "EMBEDDING.END"

	StreamTypeReasoningStart StreamMessageType = "REASONING.START"
	StreamTypeReasoningDelta StreamMessageType = "REASONING.DELTA"
	StreamTypeReasoningEnd   StreamMessageType = "REASONING.END"

	// VAD events are session-level voice activity detection signals.
	// They flow through the delta inbox but are NOT part of message reconstruction.
	StreamTypeVADSpeechStarted StreamMessageType = "VAD.SPEECH_STARTED"
	StreamTypeVADSpeechStopped StreamMessageType = "VAD.SPEECH_STOPPED"

	// Transcript events carry ASR (speech-to-text) output alongside audio.
	// Unlike TextPart (LLM-generated), TranscriptPart is ASR output that may be imperfect.
	StreamTypeTranscriptStart StreamMessageType = "TRANSCRIPT.START"
	StreamTypeTranscriptDelta StreamMessageType = "TRANSCRIPT.DELTA"
	StreamTypeTranscriptEnd   StreamMessageType = "TRANSCRIPT.END"

	// StreamTypePong is emitted in response to a ping control plane message.
	StreamTypePong StreamMessageType = "PONG"

	// Session lifecycle events bracket the entire session in the delta stream.
	StreamTypeSessionOpen    StreamMessageType = "SESSION.OPEN"
	StreamTypeSessionClose   StreamMessageType = "SESSION.CLOSE"
	StreamTypeSessionCreated StreamMessageType = "SESSION.CREATED"
	StreamTypeSessionUpdated StreamMessageType = "SESSION.UPDATED"
	StreamTypeSessionUpdate  StreamMessageType = "SESSION.UPDATE"

	// StreamTypeResponseCancel is sent TO the inference provider (via session.Send)
	// to cancel an in-progress response. Used for barge-in: when the user starts
	// speaking while the model is still delivering an audio response.
	StreamTypeResponseCancel StreamMessageType = "RESPONSE.CANCEL"

	// StreamTypeRefusal carries the complete accumulated refusal text from a model.
	// Emitted once after all refusal deltas are collected, before MESSAGE.END.
	StreamTypeRefusal StreamMessageType = "REFUSAL"

	StreamTypeLoopEnd StreamMessageType = "LOOP.END"

	// StreamTypeUsageInfo carries token usage for the assistant message that just ended.
	// Emitted by providers immediately after MESSAGE.END; corresponds to that response.
	StreamTypeUsageInfo StreamMessageType = "USAGE.INFO"

	StreamTypeError StreamMessageType = "ERROR"

	// StreamTypeSystemFullMessage carries a complete, assembled message from any
	// participant (model, tool, or user) through the unified delta inbox. This
	// eliminates the separate MessageInbox path and guarantees strict FIFO ordering
	// between deltas and full messages — both now flow through the same queue.
	StreamTypeSystemFullMessage StreamMessageType = "SYSTEM.FULL_MESSAGE"
)

// StreamMessage is one chunk over the wire: type, index (for reordering on packet loss), and type-specific value.
// Wire shape: { "type": "TEXT.DELTA", "index": 0, "value": { "type": "delta_text", "content": "..." } }
type StreamMessage struct {
	Type StreamMessageType

	Role       Role
	ToolCallId string
	Value      StreamMessageValue

	// Ordering: global consistency for retries and interrupts (see ORDERING.md).
	GlobalIndex        int           // Global index in the agent loop; assigned by the engine.
	ActorProvidedID    string        // Unique identifier from the actor that sent this delta.
	ActorProvidedIndex int           // Actor-provided index (ACTOR_PROVIDED_INDEX) for reordering on packet loss.
	ActorStreamID      string        // Unique identifier for the stream of messages from this actor.
	ActorID            ParticipantID // Participant that produced this delta (Model, User, Tool, etc.).
	LoopPassID         int           // Pass ID at time of emission; used to drop deltas stale after an interrupt.
}

// StreamMessageValue is the type-specific payload inside a StreamMessage. Implementations carry inner "type" and data.
type StreamMessageValue interface {
	streamMessageValue()
}

// MessageStartValue is the value for MESSAGE.START (inner type "message_start").
type MessageStartValue struct {
	Type string `json:"type"` // "message_start"
}

func (*MessageStartValue) streamMessageValue() {}

// NewMessageStartValue returns a value for MESSAGE.START.
func NewMessageStartValue() *MessageStartValue { return &MessageStartValue{Type: "message_start"} }

// TextStartValue is the value for TEXT.START.
type TextStartValue struct {
	Type string `json:"type"` // "text_start"
}

func (*TextStartValue) streamMessageValue() {}

// NewTextStartValue returns a value for TEXT.START.
func NewTextStartValue() *TextStartValue { return &TextStartValue{Type: "text_start"} }

// TextDeltaValue is the value for TEXT.DELTA (inner type "delta_text").
type TextDeltaValue struct {
	Type    string `json:"type"`    // "delta_text"
	Content string `json:"content"` // incremental text
}

func (*TextDeltaValue) streamMessageValue() {}

// NewTextDeltaValue returns a value for TEXT.DELTA.
func NewTextDeltaValue(content string) *TextDeltaValue {
	return &TextDeltaValue{Type: "delta_text", Content: content}
}

// TextEndValue is the value for TEXT.END.
type TextEndValue struct {
	Type string `json:"type"` // "text_end"
}

func (*TextEndValue) streamMessageValue() {}

// NewTextEndValue returns a value for TEXT.END.
func NewTextEndValue() *TextEndValue { return &TextEndValue{Type: "text_end"} }

// ToolCallStartValue is the value for TOOLCALL.START (inner type "tool_use_start").
type ToolCallStartValue struct {
	Type       string `json:"type"` // "tool_use_start"
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
}

func (*ToolCallStartValue) streamMessageValue() {}

// NewToolCallStartValue returns a value for TOOLCALL.START.
func NewToolCallStartValue(id, name string) *ToolCallStartValue {
	return &ToolCallStartValue{Type: "tool_use_start", ToolCallID: id, Name: name}
}

// ToolCallDeltaValue is the value for TOOLCALL.DELTA (inner type "input_json_delta").
type ToolCallDeltaValue struct {
	Type        string `json:"type"`         // "input_json_delta"
	PartialJSON string `json:"partial_json"` // incremental JSON for tool arguments
}

func (*ToolCallDeltaValue) streamMessageValue() {}

// NewToolCallDeltaValue returns a value for TOOLCALL.DELTA.
func NewToolCallDeltaValue(partialJSON string) *ToolCallDeltaValue {
	return &ToolCallDeltaValue{Type: "input_json_delta", PartialJSON: partialJSON}
}

// ToolCallEndValue is the value for TOOLCALL.END (inner type "tool_use_end").
type ToolCallEndValue struct {
	Type       string `json:"type"`      // "tool_use_end"
	Arguments  string `json:"arguments"` // full JSON arguments when block is complete
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
}

func (*ToolCallEndValue) streamMessageValue() {}

// NewToolCallEndValue returns a value for TOOLCALL.END.
func NewToolCallEndValue(toolCallID, name, arguments string) *ToolCallEndValue {
	return &ToolCallEndValue{Type: "tool_use_end", ToolCallID: toolCallID, Name: name, Arguments: arguments}
}

// MessageEndValue is the value for MESSAGE.END (inner type "message_end").
type MessageEndValue struct {
	Type  string     `json:"type"` // "message_end"
	Usage TokenUsage `json:"usage,omitempty"`
}

func (*MessageEndValue) streamMessageValue() {}

// NewMessageEndValue returns a value for MESSAGE.END.
func NewMessageEndValue(usage TokenUsage) *MessageEndValue {
	return &MessageEndValue{Type: "message_end", Usage: usage}
}

// UsageInfoValue is the value for USAGE.INFO: system information denoting token usage
// for the assistant response that just ended (the one that ended with the preceding MESSAGE.END).
type UsageInfoValue struct {
	Type  string     `json:"type"` // "usage_info"
	Usage TokenUsage `json:"usage"`
}

func (*UsageInfoValue) streamMessageValue() {}

// NewUsageInfoValue returns a value for USAGE.INFO.
func NewUsageInfoValue(usage TokenUsage) *UsageInfoValue {
	return &UsageInfoValue{Type: "usage_info", Usage: usage}
}

// ErrorValue is the value for ERROR (inner type "error").
type ErrorValue struct {
	Type           string `json:"type"`                     // "error"
	Message        string `json:"message"`                  // error description
	Classification string `json:"classification,omitempty"` // public gateway taxonomy classification
	ErrorType      string `json:"error_type,omitempty"`     // provider error category
	Code           string `json:"code,omitempty"`           // provider error code
	Param          string `json:"param,omitempty"`          // provider parameter associated with the error
	EventID        string `json:"event_id,omitempty"`       // related client event ID when provided
}

func (*ErrorValue) streamMessageValue() {}

// NewErrorValue returns a value for ERROR.
func NewErrorValue(message string) *ErrorValue {
	return &ErrorValue{Type: "error", Message: message}
}

// NewErrorValueWithClassification returns an ERROR value with a public gateway
// taxonomy classification for stream/event consumers.
func NewErrorValueWithClassification(message, classification string) *ErrorValue {
	return &ErrorValue{Type: "error", Message: message, Classification: classification}
}

// NewErrorValueWithDetails returns an ERROR value with provider-supplied context.
func NewErrorValueWithDetails(message, errorType, code, param, eventID string) *ErrorValue {
	return &ErrorValue{
		Type:      "error",
		Message:   message,
		ErrorType: errorType,
		Code:      code,
		Param:     param,
		EventID:   eventID,
	}
}

// RefusalValue is the value for REFUSAL (inner type "refusal").
// Carries the complete accumulated refusal text from a model response.
type RefusalValue struct {
	Type    string `json:"type"`    // "refusal"
	Message string `json:"message"` // complete refusal text
}

func (*RefusalValue) streamMessageValue() {}

// NewRefusalValue returns a value for REFUSAL.
func NewRefusalValue(message string) *RefusalValue {
	return &RefusalValue{Type: "refusal", Message: message}
}

// AudioStartValue is the value for AUDIO.START (inner type "audio_start").
type AudioStartValue struct {
	Type string `json:"type"` // "audio_start"
}

func (*AudioStartValue) streamMessageValue() {}

// NewAudioStartValue returns a value for AUDIO.START.
func NewAudioStartValue() *AudioStartValue { return &AudioStartValue{Type: "audio_start"} }

// AudioDeltaValue is the value for AUDIO.DELTA (inner type "delta_audio").
type AudioDeltaValue struct {
	Type      string `json:"type"`                 // "delta_audio"
	Content   []byte `json:"content"`              // incremental audio bytes (PCM 16kHz in memory)
	MediaType string `json:"media_type,omitempty"` // provider audio format when known
}

func (*AudioDeltaValue) streamMessageValue() {}

// NewAudioDeltaValue returns a value for AUDIO.DELTA.
// Content should be raw PCM bytes (16kHz) in memory; encoding (OPUS, base64) is the caller's concern.
func NewAudioDeltaValue(content []byte) *AudioDeltaValue {
	return &AudioDeltaValue{Type: "delta_audio", Content: content}
}

// NewAudioDeltaValueWithMediaType returns an AUDIO.DELTA value with format metadata.
func NewAudioDeltaValueWithMediaType(content []byte, mediaType string) *AudioDeltaValue {
	return &AudioDeltaValue{Type: "delta_audio", Content: content, MediaType: mediaType}
}

// AudioEndValue is the value for AUDIO.END (inner type "audio_end").
type AudioEndValue struct {
	Type string `json:"type"` // "audio_end"
}

func (*AudioEndValue) streamMessageValue() {}

// NewAudioEndValue returns a value for AUDIO.END.
func NewAudioEndValue() *AudioEndValue { return &AudioEndValue{Type: "audio_end"} }

// VADSpeechStartedValue is the value for VAD.SPEECH_STARTED.
// VAD events are session-level signals with no payload beyond the signal itself.
type VADSpeechStartedValue struct {
	Type string `json:"type"` // "vad_speech_started"
}

func (*VADSpeechStartedValue) streamMessageValue() {}

// NewVADSpeechStartedValue returns a value for VAD.SPEECH_STARTED.
func NewVADSpeechStartedValue() *VADSpeechStartedValue {
	return &VADSpeechStartedValue{Type: "vad_speech_started"}
}

// VADSpeechStoppedValue is the value for VAD.SPEECH_STOPPED.
type VADSpeechStoppedValue struct {
	Type string `json:"type"` // "vad_speech_stopped"
}

func (*VADSpeechStoppedValue) streamMessageValue() {}

// NewVADSpeechStoppedValue returns a value for VAD.SPEECH_STOPPED.
func NewVADSpeechStoppedValue() *VADSpeechStoppedValue {
	return &VADSpeechStoppedValue{Type: "vad_speech_stopped"}
}

// TranscriptStartValue is the value for TRANSCRIPT.START.
type TranscriptStartValue struct {
	Type string `json:"type"` // "transcript_start"
}

func (*TranscriptStartValue) streamMessageValue() {}

// NewTranscriptStartValue returns a value for TRANSCRIPT.START.
func NewTranscriptStartValue() *TranscriptStartValue {
	return &TranscriptStartValue{Type: "transcript_start"}
}

// TranscriptDeltaValue is the value for TRANSCRIPT.DELTA.
type TranscriptDeltaValue struct {
	Type string `json:"type"` // "transcript_delta"
	Text string `json:"text"` // incremental ASR transcript text
}

func (*TranscriptDeltaValue) streamMessageValue() {}

// NewTranscriptDeltaValue returns a value for TRANSCRIPT.DELTA.
func NewTranscriptDeltaValue(text string) *TranscriptDeltaValue {
	return &TranscriptDeltaValue{Type: "transcript_delta", Text: text}
}

// TranscriptEndValue is the value for TRANSCRIPT.END.
type TranscriptEndValue struct {
	Type     string `json:"type"`      // "transcript_end"
	FullText string `json:"full_text"` // complete accumulated transcript
}

func (*TranscriptEndValue) streamMessageValue() {}

// NewTranscriptEndValue returns a value for TRANSCRIPT.END.
func NewTranscriptEndValue(fullText string) *TranscriptEndValue {
	return &TranscriptEndValue{Type: "transcript_end", FullText: fullText}
}

// PongValue is the value for PONG, emitted in response to a ping control plane message.
type PongValue struct {
	Type      string `json:"type"`      // "pong"
	Timestamp int64  `json:"timestamp"` // Unix milliseconds at response time
}

func (*PongValue) streamMessageValue() {}

// NewPongValue returns a value for PONG with the current timestamp.
func NewPongValue(timestamp int64) *PongValue {
	return &PongValue{Type: "pong", Timestamp: timestamp}
}

// SessionOpenValue is the value for SESSION.OPEN, emitted as the first event in a session.
type SessionOpenValue struct {
	Type      string `json:"type"`       // "session_open"
	SessionID string `json:"session_id"` // unique session identifier
	Mode      string `json:"mode"`       // e.g. "audio_inference"
}

func (*SessionOpenValue) streamMessageValue() {}

// NewSessionOpenValue returns a value for SESSION.OPEN.
func NewSessionOpenValue(sessionID, mode string) *SessionOpenValue {
	return &SessionOpenValue{Type: "session_open", SessionID: sessionID, Mode: mode}
}

// SessionCloseValue is the value for SESSION.CLOSE, emitted before LOOP.END.
type SessionCloseValue struct {
	Type      string `json:"type"`       // "session_close"
	SessionID string `json:"session_id"` // unique session identifier
	Reason    string `json:"reason"`     // e.g. "client_close", "error", "timeout"
}

func (*SessionCloseValue) streamMessageValue() {}

// NewSessionCloseValue returns a value for SESSION.CLOSE.
func NewSessionCloseValue(sessionID, reason string) *SessionCloseValue {
	return &SessionCloseValue{Type: "session_close", SessionID: sessionID, Reason: reason}
}

// SessionCreatedValue is the value for SESSION.CREATED, emitted when the server
// confirms a session has been established. It carries the session configuration
// returned by the inference provider.
type SessionCreatedValue struct {
	Type      string `json:"type"`       // "session_created"
	SessionID string `json:"session_id"` // unique session identifier from the server
	Model     string `json:"model"`      // model in use for the session
}

func (*SessionCreatedValue) streamMessageValue() {}

// NewSessionCreatedValue returns a value for SESSION.CREATED.
func NewSessionCreatedValue(sessionID, model string) *SessionCreatedValue {
	return &SessionCreatedValue{Type: "session_created", SessionID: sessionID, Model: model}
}

// SessionUpdatedValue is the value for SESSION.UPDATED, emitted when the server
// confirms a session configuration update (in response to SESSION.UPDATE).
type SessionUpdatedValue struct {
	Type      string `json:"type"`       // "session_updated"
	SessionID string `json:"session_id"` // unique session identifier
}

func (*SessionUpdatedValue) streamMessageValue() {}

// NewSessionUpdatedValue returns a value for SESSION.UPDATED.
func NewSessionUpdatedValue(sessionID string) *SessionUpdatedValue {
	return &SessionUpdatedValue{Type: "session_updated", SessionID: sessionID}
}

// SessionUpdateConfig holds the parameters sent in a SESSION.UPDATE message.
// Set in AgentLoopConfig via WithSessionConfig; the model runner sends this
// automatically after SESSION.CREATED.
type SessionUpdateConfig struct {
	Instructions string   // system prompt / instructions for the session
	Model        string   // model name (e.g. "grok-3")
	Modalities   []string // input/output modalities (e.g. ["audio", "text"])
}

// SessionUpdateValue is the value for SESSION.UPDATE (outbound), used to send
// a session configuration update to the inference provider.
type SessionUpdateValue struct {
	Type         string   `json:"type"`                   // "session_update"
	Instructions string   `json:"instructions,omitempty"` // system prompt
	Model        string   `json:"model,omitempty"`        // model name
	Modalities   []string `json:"modalities,omitempty"`   // input/output modalities
}

func (*SessionUpdateValue) streamMessageValue() {}

// NewSessionUpdateValue returns a SESSION.UPDATE value from a SessionUpdateConfig.
func NewSessionUpdateValue(cfg *SessionUpdateConfig) *SessionUpdateValue {
	return &SessionUpdateValue{
		Type:         "session_update",
		Instructions: cfg.Instructions,
		Model:        cfg.Model,
		Modalities:   cfg.Modalities,
	}
}

// ReasoningStartValue is the value for REASONING.START (inner type "reasoning_start").
type ReasoningStartValue struct {
	Type string `json:"type"` // "reasoning_start"
}

func (*ReasoningStartValue) streamMessageValue() {}

// NewReasoningStartValue returns a value for REASONING.START.
func NewReasoningStartValue() *ReasoningStartValue {
	return &ReasoningStartValue{Type: "reasoning_start"}
}

// ReasoningDeltaValue is the value for REASONING.DELTA (inner type "delta_reasoning").
type ReasoningDeltaValue struct {
	Type    string `json:"type"`    // "delta_reasoning"
	Content string `json:"content"` // incremental reasoning text
}

func (*ReasoningDeltaValue) streamMessageValue() {}

// NewReasoningDeltaValue returns a value for REASONING.DELTA.
func NewReasoningDeltaValue(content string) *ReasoningDeltaValue {
	return &ReasoningDeltaValue{Type: "delta_reasoning", Content: content}
}

// ReasoningEndValue is the value for REASONING.END (inner type "reasoning_end").
type ReasoningEndValue struct {
	Type string `json:"type"` // "reasoning_end"
}

func (*ReasoningEndValue) streamMessageValue() {}

// NewReasoningEndValue returns a value for REASONING.END.
func NewReasoningEndValue() *ReasoningEndValue { return &ReasoningEndValue{Type: "reasoning_end"} }

// LoopEndValue is the value for LOOP.END (inner type "loop_end").
type LoopEndValue struct {
	Type string `json:"type"` // "loop_end"
}

func (*LoopEndValue) streamMessageValue() {}

// NewLoopEndValue returns a value for LOOP.END.
func NewLoopEndValue() *LoopEndValue { return &LoopEndValue{Type: "loop_end"} }

// ImageStartValue is the value for IMAGE.START (inner type "image_start").
type ImageStartValue struct {
	Type      string `json:"type"`                 // "image_start"
	MediaType string `json:"media_type,omitempty"` // e.g. "image/png", "image/jpeg"
}

func (*ImageStartValue) streamMessageValue() {}

// NewImageStartValue returns a value for IMAGE.START.
// mediaType carries the MIME type of the image (e.g. "image/png"); pass "" if unknown.
func NewImageStartValue(mediaType string) *ImageStartValue {
	return &ImageStartValue{Type: "image_start", MediaType: mediaType}
}

// ImageDeltaValue is the value for IMAGE.DELTA (inner type "delta_image").
type ImageDeltaValue struct {
	Type    string `json:"type"`    // "delta_image"
	Content []byte `json:"content"` // incremental image data
}

func (*ImageDeltaValue) streamMessageValue() {}

// NewImageDeltaValue returns a value for IMAGE.DELTA.
// Content should be raw image bytes; encoding is the caller's concern.
func NewImageDeltaValue(content []byte) *ImageDeltaValue {
	return &ImageDeltaValue{Type: "delta_image", Content: content}
}

// ImageEndValue is the value for IMAGE.END (inner type "image_end").
type ImageEndValue struct {
	Type string `json:"type"` // "image_end"
}

func (*ImageEndValue) streamMessageValue() {}

// NewImageEndValue returns a value for IMAGE.END.
func NewImageEndValue() *ImageEndValue { return &ImageEndValue{Type: "image_end"} }

// VideoStartValue is the value for VIDEO.START (inner type "video_start").
type VideoStartValue struct {
	Type      string `json:"type"`                 // "video_start"
	MediaType string `json:"media_type,omitempty"` // e.g. "video/mp4"
}

func (*VideoStartValue) streamMessageValue() {}

// NewVideoStartValue returns a value for VIDEO.START.
// mediaType carries the MIME type of the video (e.g. "video/mp4"); pass "" if unknown.
func NewVideoStartValue(mediaType string) *VideoStartValue {
	return &VideoStartValue{Type: "video_start", MediaType: mediaType}
}

// VideoDeltaValue is the value for VIDEO.DELTA (inner type "delta_video").
type VideoDeltaValue struct {
	Type    string `json:"type"`    // "delta_video"
	Content []byte `json:"content"` // incremental video bytes
}

func (*VideoDeltaValue) streamMessageValue() {}

// NewVideoDeltaValue returns a value for VIDEO.DELTA.
func NewVideoDeltaValue(content []byte) *VideoDeltaValue {
	return &VideoDeltaValue{Type: "delta_video", Content: content}
}

// VideoEndValue is the value for VIDEO.END (inner type "video_end").
type VideoEndValue struct {
	Type string `json:"type"` // "video_end"
}

func (*VideoEndValue) streamMessageValue() {}

// NewVideoEndValue returns a value for VIDEO.END.
func NewVideoEndValue() *VideoEndValue { return &VideoEndValue{Type: "video_end"} }

// FileStartValue is the value for FILE.START (inner type "file_start").
type FileStartValue struct {
	Type      string `json:"type"`                 // "file_start"
	MediaType string `json:"media_type,omitempty"` // MIME type
	Name      string `json:"name,omitempty"`       // optional filename
}

func (*FileStartValue) streamMessageValue() {}

// NewFileStartValue returns a value for FILE.START.
func NewFileStartValue(mediaType, name string) *FileStartValue {
	return &FileStartValue{Type: "file_start", MediaType: mediaType, Name: name}
}

// FileDeltaValue is the value for FILE.DELTA (inner type "delta_file").
type FileDeltaValue struct {
	Type    string `json:"type"`    // "delta_file"
	Content []byte `json:"content"` // incremental file bytes
}

func (*FileDeltaValue) streamMessageValue() {}

// NewFileDeltaValue returns a value for FILE.DELTA.
func NewFileDeltaValue(content []byte) *FileDeltaValue {
	return &FileDeltaValue{Type: "delta_file", Content: content}
}

// FileEndValue is the value for FILE.END (inner type "file_end").
type FileEndValue struct {
	Type string `json:"type"` // "file_end"
}

func (*FileEndValue) streamMessageValue() {}

// NewFileEndValue returns a value for FILE.END.
func NewFileEndValue() *FileEndValue { return &FileEndValue{Type: "file_end"} }

// EmbeddingStartValue is the value for EMBEDDING.START (inner type "embedding_start").
type EmbeddingStartValue struct {
	Type      string `json:"type"`                 // "embedding_start"
	MediaType string `json:"media_type,omitempty"` // e.g. "application/vnd.safetensors"
}

func (*EmbeddingStartValue) streamMessageValue() {}

// NewEmbeddingStartValue returns a value for EMBEDDING.START.
// mediaType carries the MIME type of the embedding (e.g. "application/vnd.safetensors"); pass "" if unknown.
func NewEmbeddingStartValue(mediaType string) *EmbeddingStartValue {
	return &EmbeddingStartValue{Type: "embedding_start", MediaType: mediaType}
}

// EmbeddingDeltaValue is the value for EMBEDDING.DELTA (inner type "delta_embedding").
type EmbeddingDeltaValue struct {
	Type    string `json:"type"`    // "delta_embedding"
	Content []byte `json:"content"` // incremental embedding bytes (e.g. safetensors chunk)
}

func (*EmbeddingDeltaValue) streamMessageValue() {}

// NewEmbeddingDeltaValue returns a value for EMBEDDING.DELTA.
func NewEmbeddingDeltaValue(content []byte) *EmbeddingDeltaValue {
	return &EmbeddingDeltaValue{Type: "delta_embedding", Content: content}
}

// EmbeddingEndValue is the value for EMBEDDING.END (inner type "embedding_end").
type EmbeddingEndValue struct {
	Type string `json:"type"` // "embedding_end"
}

func (*EmbeddingEndValue) streamMessageValue() {}

// NewEmbeddingEndValue returns a value for EMBEDDING.END.
func NewEmbeddingEndValue() *EmbeddingEndValue { return &EmbeddingEndValue{Type: "embedding_end"} }

// TokenUsage tracks token consumption for a single inference call.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	ReasoningTokens  int // thinking/reasoning tokens (e.g. from OpenRouter completion_tokens_details)
}

// InferenceResultValue is the value for SYSTEM.FULL_MESSAGE. It carries a
// complete, assembled message from any participant (model, tool, or user) through
// the unified delta inbox so that full messages and streaming deltas share a single
// FIFO queue, eliminating race conditions between the two paths.
type InferenceResultValue struct {
	Type    string  `json:"type"`   // "inference_result"
	Source  string  `json:"source"` // participant id: "model", "tool", "user"
	Message Message `json:"message"`
}

func (*InferenceResultValue) streamMessageValue() {}

// NewInferenceResultValue returns a value for SYSTEM.FULL_MESSAGE.
func NewInferenceResultValue(source string, msg Message) *InferenceResultValue {
	return &InferenceResultValue{Type: "inference_result", Source: source, Message: msg}
}

// ResponseCancelValue is the value for RESPONSE.CANCEL (outbound), sent to the
// inference provider via session.Send to cancel an in-progress response (barge-in).
type ResponseCancelValue struct {
	Type string `json:"type"` // "response_cancel"
}

func (*ResponseCancelValue) streamMessageValue() {}

// NewResponseCancelValue returns a value for RESPONSE.CANCEL.
func NewResponseCancelValue() *ResponseCancelValue {
	return &ResponseCancelValue{Type: "response_cancel"}
}
