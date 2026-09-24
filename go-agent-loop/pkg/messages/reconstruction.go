package messages

import "strings"

// binaryAccumulator collects copied delta chunks for one binary content kind.
type binaryAccumulator struct {
	chunks    [][]byte
	mediaType string
}

func (a *binaryAccumulator) add(content []byte) {
	chunk := make([]byte, len(content))
	copy(chunk, content)
	a.chunks = append(a.chunks, chunk)
}

func (a *binaryAccumulator) present() bool { return len(a.chunks) > 0 }

func (a *binaryAccumulator) bytes() []byte {
	combined := make([]byte, 0)
	for _, c := range a.chunks {
		combined = append(combined, c...)
	}
	return combined
}

// binaryReconstructionState holds the binary content kinds shared by model
// and tool reconstruction.
type binaryReconstructionState struct {
	image     binaryAccumulator
	audio     binaryAccumulator
	video     binaryAccumulator
	file      binaryAccumulator
	fileName  string
	embedding binaryAccumulator
}

func (m *binaryReconstructionState) appendImagePart(parts []ContentPart) []ContentPart {
	if m.image.present() {
		parts = append(parts, ImagePart{Bytes: m.image.bytes(), MediaType: m.image.mediaType})
	}
	return parts
}

// appendTrailingParts appends video, file, and embedding parts, which follow
// the same order in model and tool messages.
func (m *binaryReconstructionState) appendTrailingParts(parts []ContentPart) []ContentPart {
	if m.video.present() {
		parts = append(parts, VideoPart{Bytes: m.video.bytes(), MediaType: m.video.mediaType})
	}
	if m.file.present() {
		parts = append(parts, FilePart{Bytes: m.file.bytes(), MediaType: m.file.mediaType, Name: m.fileName})
	}
	if m.embedding.present() {
		parts = append(parts, EmbeddingPart{Bytes: m.embedding.bytes(), MediaType: m.embedding.mediaType})
	}
	return parts
}

// modelReconstructionState holds transient state while walking model deltas
// for reconstruction (current tool call id/name for TOOLCALL.END) and the
// accumulated message content.
type modelReconstructionState struct {
	currentToolID   string
	currentToolName string
	text            strings.Builder
	hasTextPart     bool
	reasoning       strings.Builder
	transcript      strings.Builder
	refusal         string
	toolCalls       []ToolCall
	media           binaryReconstructionState
}

// toolReconstructionState holds per-tool accumulation when reconstructing
// tool batch deltas into messages.
type toolReconstructionState struct {
	text  strings.Builder
	media binaryReconstructionState
}

// ReconstructModelMessageFromDeltas builds a single assistant Message from a slice
// of model stream deltas. Works for full messages (including MESSAGE.END) and for
// partial/interrupted streams (e.g. only TEXT.DELTA so far). Deltas are processed
// in order; content is accumulated into one Message.
func ReconstructModelMessageFromDeltas(deltas []StreamMessage) Message {
	st := &modelReconstructionState{}
	for _, d := range deltas {
		if !st.applyTextual(d.Value) {
			st.applyMedia(d.Value)
		}
	}
	return Message{Role: RoleAssistant, Refusal: st.refusal, ToolCalls: st.toolCalls, ContentParts: st.parts()}
}

// applyTextual accumulates text, reasoning, transcript, refusal, and tool call
// deltas. It reports whether the value was one of those kinds. VAD events and
// TRANSCRIPT.START are session-level signals, not message content, and are
// ignored by both apply methods.
func (st *modelReconstructionState) applyTextual(value StreamMessageValue) bool {
	switch v := value.(type) {
	case *TextStartValue:
		st.hasTextPart = true
	case *TextDeltaValue:
		st.hasTextPart = true
		st.text.WriteString(v.Content)
	case *ReasoningStartValue:
		st.reasoning.WriteString("<thinking>\n")
	case *ReasoningDeltaValue:
		st.reasoning.WriteString(v.Content)
	case *ReasoningEndValue:
		st.reasoning.WriteString("\n</thinking>")
	case *ToolCallStartValue:
		st.currentToolID = v.ToolCallID
		st.currentToolName = v.Name
	case *ToolCallEndValue:
		st.endToolCall(v)
	case *RefusalValue:
		st.refusal = v.Message
	case *TranscriptDeltaValue:
		st.transcript.WriteString(v.Text)
	case *TranscriptEndValue:
		if v.FullText != "" {
			st.transcript.Reset()
			st.transcript.WriteString(v.FullText)
		}
	default:
		return false
	}
	return true
}

func (st *modelReconstructionState) endToolCall(v *ToolCallEndValue) {
	id := v.ToolCallID
	if id == "" {
		id = st.currentToolID
	}
	name := v.Name
	if name == "" {
		name = st.currentToolName
	}
	st.toolCalls = append(st.toolCalls, ToolCall{ID: id, Name: name, Arguments: v.Arguments})
	st.currentToolID = ""
	st.currentToolName = ""
}

func (st *modelReconstructionState) applyMedia(value StreamMessageValue) {
	media := &st.media
	switch v := value.(type) {
	case *AudioDeltaValue:
		media.audio.add(v.Content)
		if v.MediaType != "" {
			media.audio.mediaType = v.MediaType
		}
	case *ImageStartValue:
		media.image.mediaType = v.MediaType
	case *ImageDeltaValue:
		media.image.add(v.Content)
	case *VideoStartValue:
		media.video.mediaType = v.MediaType
	case *VideoDeltaValue:
		media.video.add(v.Content)
	case *FileStartValue:
		media.file.mediaType = v.MediaType
		media.fileName = v.Name
	case *FileDeltaValue:
		media.file.add(v.Content)
	case *EmbeddingStartValue:
		media.embedding.mediaType = v.MediaType
	case *EmbeddingDeltaValue:
		media.embedding.add(v.Content)
	}
}

func (st *modelReconstructionState) parts() []ContentPart {
	var parts []ContentPart
	if t := st.text.String(); st.hasTextPart || t != "" {
		parts = append(parts, NewTextPart(t))
	}
	if r := st.reasoning.String(); r != "" {
		parts = append(parts, NewReasoningPart(r))
	}
	if tr := st.transcript.String(); tr != "" {
		parts = append(parts, TranscriptPart{Text: tr})
	}
	if st.media.audio.present() {
		audioMediaType := st.media.audio.mediaType
		if audioMediaType == "" {
			audioMediaType = "audio/pcm"
		}
		parts = append(parts, AudioPart{Bytes: st.media.audio.bytes(), MediaType: audioMediaType})
	}
	parts = st.media.appendImagePart(parts)
	return st.media.appendTrailingParts(parts)
}

// ReconstructToolMessagesFromDeltas builds one Message per tool result from a
// slice of tool stream deltas. Expects MESSAGE.START to begin a batch and
// MESSAGE.END to end it; ToolCallId on each delta associates content with a tool.
// Works for full batches and for partial/interrupted (partial content per tool).
func ReconstructToolMessagesFromDeltas(deltas []StreamMessage) []Message {
	batch := &toolReconstructionBatch{perTool: make(map[string]*toolReconstructionState)}
	for _, d := range deltas {
		batch.apply(d)
	}
	return batch.messages()
}

// toolReconstructionBatch tracks per-tool state in first-seen order.
type toolReconstructionBatch struct {
	perTool map[string]*toolReconstructionState
	order   []string
}

func (b *toolReconstructionBatch) ensure(id string) *toolReconstructionState {
	if _, exists := b.perTool[id]; !exists {
		b.order = append(b.order, id)
		b.perTool[id] = &toolReconstructionState{}
	}
	return b.perTool[id]
}

// apply handles batch and content-start boundaries, which create per-tool
// state. Content deltas are applied only to a tool whose content started.
func (b *toolReconstructionBatch) apply(d StreamMessage) {
	switch v := d.Value.(type) {
	case *MessageStartValue:
		b.perTool = make(map[string]*toolReconstructionState)
		b.order = nil
	case *TextStartValue:
		b.ensure(d.ToolCallId)
	case *ImageStartValue:
		b.ensure(d.ToolCallId).media.image.mediaType = v.MediaType
	case *AudioStartValue:
		b.ensure(d.ToolCallId).media.audio.mediaType = "audio/pcm"
	case *VideoStartValue:
		b.ensure(d.ToolCallId).media.video.mediaType = v.MediaType
	case *FileStartValue:
		st := b.ensure(d.ToolCallId)
		st.media.file.mediaType = v.MediaType
		st.media.fileName = v.Name
	case *EmbeddingStartValue:
		b.ensure(d.ToolCallId).media.embedding.mediaType = v.MediaType
	default:
		if st := b.perTool[d.ToolCallId]; st != nil {
			st.applyDelta(d.Value)
		}
	}
}

func (st *toolReconstructionState) applyDelta(value StreamMessageValue) {
	switch v := value.(type) {
	case *TextDeltaValue:
		st.text.WriteString(v.Content)
	case *ImageDeltaValue:
		st.media.image.add(v.Content)
	case *AudioDeltaValue:
		st.media.audio.add(v.Content)
	case *VideoDeltaValue:
		st.media.video.add(v.Content)
	case *FileDeltaValue:
		st.media.file.add(v.Content)
	case *EmbeddingDeltaValue:
		st.media.embedding.add(v.Content)
	}
}

func (b *toolReconstructionBatch) messages() []Message {
	var out []Message
	for _, id := range b.order {
		st := b.perTool[id]
		if st == nil {
			continue
		}
		out = append(out, Message{
			Role:         RoleTool,
			ContentParts: st.parts(),
			ToolCallID:   id,
		})
	}
	return out
}

func (st *toolReconstructionState) parts() []ContentPart {
	var parts []ContentPart
	if text := st.text.String(); text != "" {
		parts = append(parts, NewTextPart(text))
	}
	parts = st.media.appendImagePart(parts)
	if st.media.audio.present() {
		parts = append(parts, AudioPart{Bytes: st.media.audio.bytes(), MediaType: st.media.audio.mediaType})
	}
	parts = st.media.appendTrailingParts(parts)
	if len(parts) == 0 {
		parts = []ContentPart{NewTextPart("")}
	}
	return parts
}
