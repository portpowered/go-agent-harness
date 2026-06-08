package messages

import "strings"

// modelReconstructionState holds transient state while walking model deltas
// for reconstruction (current tool call id/name for TOOLCALL.END).
type modelReconstructionState struct {
	currentToolID   string
	currentToolName string
}

// toolReconstructionState holds per-tool accumulation when reconstructing
// tool batch deltas into messages.
type toolReconstructionState struct {
	textBuilder        strings.Builder
	imageChunks        [][]byte
	imageMediaType     string
	audioChunks        [][]byte
	audioMediaType     string
	videoChunks        [][]byte
	videoMediaType     string
	fileChunks         [][]byte
	fileMediaType      string
	fileName           string
	embeddingChunks    [][]byte
	embeddingMediaType string
}

// ReconstructModelMessageFromDeltas builds a single assistant Message from a slice
// of model stream deltas. Works for full messages (including MESSAGE.END) and for
// partial/interrupted streams (e.g. only TEXT.DELTA so far). Deltas are processed
// in order; content is accumulated into one Message.
func ReconstructModelMessageFromDeltas(deltas []StreamMessage) Message {
	var (
		textBuilder        strings.Builder
		hasTextPart        bool
		reasoningBuilder   strings.Builder
		transcriptBuilder  strings.Builder
		refusal            string
		toolCalls          []ToolCall
		audioChunks        [][]byte
		audioMediaType     string
		imageChunks        [][]byte
		imageMediaType     string
		videoChunks        [][]byte
		videoMediaType     string
		fileChunks         [][]byte
		fileMediaType      string
		fileName           string
		embeddingChunks    [][]byte
		embeddingMediaType string
	)
	st := modelReconstructionState{}

	for _, d := range deltas {
		switch v := d.Value.(type) {
		case *TextStartValue:
			hasTextPart = true
		case *TextDeltaValue:
			hasTextPart = true
			textBuilder.WriteString(v.Content)
		case *ReasoningStartValue:
			reasoningBuilder.WriteString("<thinking>\n")
		case *ReasoningDeltaValue:
			reasoningBuilder.WriteString(v.Content)
		case *ReasoningEndValue:
			reasoningBuilder.WriteString("\n</thinking>")
		case *ToolCallStartValue:
			st.currentToolID = v.ToolCallID
			st.currentToolName = v.Name
		case *ToolCallEndValue:
			id := v.ToolCallID
			if id == "" {
				id = st.currentToolID
			}
			name := v.Name
			if name == "" {
				name = st.currentToolName
			}
			toolCalls = append(toolCalls, ToolCall{ID: id, Name: name, Arguments: v.Arguments})
			st.currentToolID = ""
			st.currentToolName = ""
		case *AudioDeltaValue:
			chunk := make([]byte, len(v.Content))
			copy(chunk, v.Content)
			audioChunks = append(audioChunks, chunk)
			if v.MediaType != "" {
				audioMediaType = v.MediaType
			}
		case *ImageStartValue:
			imageMediaType = v.MediaType
		case *ImageDeltaValue:
			chunk := make([]byte, len(v.Content))
			copy(chunk, v.Content)
			imageChunks = append(imageChunks, chunk)
		case *VideoStartValue:
			videoMediaType = v.MediaType
		case *VideoDeltaValue:
			chunk := make([]byte, len(v.Content))
			copy(chunk, v.Content)
			videoChunks = append(videoChunks, chunk)
		case *FileStartValue:
			fileMediaType = v.MediaType
			fileName = v.Name
		case *FileDeltaValue:
			chunk := make([]byte, len(v.Content))
			copy(chunk, v.Content)
			fileChunks = append(fileChunks, chunk)
		case *RefusalValue:
			refusal = v.Message
		case *EmbeddingStartValue:
			embeddingMediaType = v.MediaType
		case *EmbeddingDeltaValue:
			chunk := make([]byte, len(v.Content))
			copy(chunk, v.Content)
			embeddingChunks = append(embeddingChunks, chunk)
		case *TranscriptDeltaValue:
			transcriptBuilder.WriteString(v.Text)
		case *TranscriptEndValue:
			if v.FullText != "" {
				transcriptBuilder.Reset()
				transcriptBuilder.WriteString(v.FullText)
			}
		// VAD events are session-level signals, not message content — skip.
		case *VADSpeechStartedValue, *VADSpeechStoppedValue:
		case *TranscriptStartValue:
		}
	}

	msg := Message{Role: RoleAssistant, Refusal: refusal, ToolCalls: toolCalls}
	var parts []ContentPart
	if t := textBuilder.String(); hasTextPart || t != "" {
		parts = append(parts, NewTextPart(t))
	}
	if r := reasoningBuilder.String(); r != "" {
		parts = append(parts, NewReasoningPart(r))
	}
	if tr := transcriptBuilder.String(); tr != "" {
		parts = append(parts, TranscriptPart{Text: tr})
	}
	if len(audioChunks) > 0 {
		combined := make([]byte, 0)
		for _, c := range audioChunks {
			combined = append(combined, c...)
		}
		if audioMediaType == "" {
			audioMediaType = "audio/pcm"
		}
		parts = append(parts, AudioPart{Bytes: combined, MediaType: audioMediaType})
	}
	if len(imageChunks) > 0 {
		combined := make([]byte, 0)
		for _, c := range imageChunks {
			combined = append(combined, c...)
		}
		parts = append(parts, ImagePart{Bytes: combined, MediaType: imageMediaType})
	}
	if len(videoChunks) > 0 {
		combined := make([]byte, 0)
		for _, c := range videoChunks {
			combined = append(combined, c...)
		}
		parts = append(parts, VideoPart{Bytes: combined, MediaType: videoMediaType})
	}
	if len(fileChunks) > 0 {
		combined := make([]byte, 0)
		for _, c := range fileChunks {
			combined = append(combined, c...)
		}
		parts = append(parts, FilePart{Bytes: combined, MediaType: fileMediaType, Name: fileName})
	}
	if len(embeddingChunks) > 0 {
		combined := make([]byte, 0)
		for _, c := range embeddingChunks {
			combined = append(combined, c...)
		}
		parts = append(parts, EmbeddingPart{Bytes: combined, MediaType: embeddingMediaType})
	}
	msg.ContentParts = parts
	return msg
}

// ReconstructToolMessagesFromDeltas builds one Message per tool result from a
// slice of tool stream deltas. Expects MESSAGE.START to begin a batch and
// MESSAGE.END to end it; ToolCallId on each delta associates content with a tool.
// Works for full batches and for partial/interrupted (partial content per tool).
func ReconstructToolMessagesFromDeltas(deltas []StreamMessage) []Message {
	perTool := make(map[string]*toolReconstructionState)
	var toolOrder []string

	ensure := func(id string) *toolReconstructionState {
		if _, exists := perTool[id]; !exists {
			toolOrder = append(toolOrder, id)
			perTool[id] = &toolReconstructionState{}
		}
		return perTool[id]
	}

	for _, d := range deltas {
		switch v := d.Value.(type) {
		case *MessageStartValue:
			perTool = make(map[string]*toolReconstructionState)
			toolOrder = nil
		case *TextStartValue:
			_ = v
			ensure(d.ToolCallId)
		case *TextDeltaValue:
			if st := perTool[d.ToolCallId]; st != nil {
				st.textBuilder.WriteString(v.Content)
			}
		case *ImageStartValue:
			st := ensure(d.ToolCallId)
			st.imageMediaType = v.MediaType
		case *ImageDeltaValue:
			if st := perTool[d.ToolCallId]; st != nil {
				chunk := make([]byte, len(v.Content))
				copy(chunk, v.Content)
				st.imageChunks = append(st.imageChunks, chunk)
			}
		case *AudioStartValue:
			st := ensure(d.ToolCallId)
			st.audioMediaType = "audio/pcm"
		case *AudioDeltaValue:
			if st := perTool[d.ToolCallId]; st != nil {
				chunk := make([]byte, len(v.Content))
				copy(chunk, v.Content)
				st.audioChunks = append(st.audioChunks, chunk)
			}
		case *VideoStartValue:
			st := ensure(d.ToolCallId)
			st.videoMediaType = v.MediaType
		case *VideoDeltaValue:
			if st := perTool[d.ToolCallId]; st != nil {
				chunk := make([]byte, len(v.Content))
				copy(chunk, v.Content)
				st.videoChunks = append(st.videoChunks, chunk)
			}
		case *FileStartValue:
			st := ensure(d.ToolCallId)
			st.fileMediaType = v.MediaType
			st.fileName = v.Name
		case *FileDeltaValue:
			if st := perTool[d.ToolCallId]; st != nil {
				chunk := make([]byte, len(v.Content))
				copy(chunk, v.Content)
				st.fileChunks = append(st.fileChunks, chunk)
			}
		case *EmbeddingStartValue:
			st := ensure(d.ToolCallId)
			st.embeddingMediaType = v.MediaType
		case *EmbeddingDeltaValue:
			if st := perTool[d.ToolCallId]; st != nil {
				chunk := make([]byte, len(v.Content))
				copy(chunk, v.Content)
				st.embeddingChunks = append(st.embeddingChunks, chunk)
			}
		}
	}

	var out []Message
	for _, id := range toolOrder {
		st := perTool[id]
		if st == nil {
			continue
		}
		var parts []ContentPart
		if text := st.textBuilder.String(); text != "" {
			parts = append(parts, NewTextPart(text))
		}
		if len(st.imageChunks) > 0 {
			combined := make([]byte, 0)
			for _, c := range st.imageChunks {
				combined = append(combined, c...)
			}
			parts = append(parts, ImagePart{Bytes: combined, MediaType: st.imageMediaType})
		}
		if len(st.audioChunks) > 0 {
			combined := make([]byte, 0)
			for _, c := range st.audioChunks {
				combined = append(combined, c...)
			}
			parts = append(parts, AudioPart{Bytes: combined, MediaType: st.audioMediaType})
		}
		if len(st.videoChunks) > 0 {
			combined := make([]byte, 0)
			for _, c := range st.videoChunks {
				combined = append(combined, c...)
			}
			parts = append(parts, VideoPart{Bytes: combined, MediaType: st.videoMediaType})
		}
		if len(st.fileChunks) > 0 {
			combined := make([]byte, 0)
			for _, c := range st.fileChunks {
				combined = append(combined, c...)
			}
			parts = append(parts, FilePart{Bytes: combined, MediaType: st.fileMediaType, Name: st.fileName})
		}
		if len(st.embeddingChunks) > 0 {
			combined := make([]byte, 0)
			for _, c := range st.embeddingChunks {
				combined = append(combined, c...)
			}
			parts = append(parts, EmbeddingPart{Bytes: combined, MediaType: st.embeddingMediaType})
		}
		if len(parts) == 0 {
			parts = []ContentPart{NewTextPart("")}
		}
		out = append(out, Message{
			Role:         RoleTool,
			ContentParts: parts,
			ToolCallID:   id,
		})
	}
	return out
}
