package messages

// Stream types and general stream values live here, including the shared envelope, payload interface, discriminators, and value families that are not owned by a narrower tool, audio, session, or terminal file.

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

	// StreamTypeResponseCreate is sent TO the inference provider (via
	// session.Send) after one or more correlated tool results have been
	// accepted. It explicitly asks a realtime provider to generate the grounded
	// continuation for an input with no text representation, such as an
	// audio-only tool continuation; unlike MESSAGE.END, it does not commit user
	// audio.
	StreamTypeResponseCreate StreamMessageType = "RESPONSE.CREATE"

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
	// ResponseID identifies the provider response that owns this message. It
	// lets session consumers ignore a late terminal or output event from an
	// older response after a replacement response has started.
	ResponseID string `json:"response_id,omitempty"`
	// ResponsePurpose is internal lifecycle metadata attached by the session
	// runner to provider output. It keeps a short tool acknowledgement out of
	// final-turn admission and scheduled-response accounting.
	ResponsePurpose ResponsePurpose `json:"response_purpose,omitempty"`
	Value           StreamMessageValue

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
