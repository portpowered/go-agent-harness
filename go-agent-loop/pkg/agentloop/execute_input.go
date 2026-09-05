package agentloop

import (
	"bytes"
	"image"
	"image/jpeg"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// Audio holds raw PCM audio samples.
type Audio struct {
	Samples    []int16
	SampleRate int
	Channels   int
}

// Video holds raw video bytes with an associated MIME type.
type Video struct {
	Bytes     []byte
	MediaType string // e.g. "video/mp4", "video/h264"
}

// File holds arbitrary file bytes with an optional filename and MIME type.
type File struct {
	Bytes     []byte
	Name      string // optional filename
	MediaType string // e.g. "application/pdf", "text/csv"
}

// ExecuteInput is the input for Execute and ExecuteStreaming.
// All fields except Message are optional (nil/zero = omitted).
type ExecuteInput struct {
	// Message is the text content (required for text-based prompts).
	Message string
	// Image is optional image content. Nil means no image.
	Image image.Image
	// Audio is optional audio content. Nil means no audio.
	Audio *Audio
	// Video is optional video content. Nil means no video.
	Video *Video
	// File is optional file content. Nil means no file.
	File *File
	// ContentParts are additional multimodal parts (e.g. from file paths). Appended to the user message after Message/Image/Audio/Video/File.
	ContentParts []messages.ContentPart
	// OutputReasoningStream, when true, merges reasoning/thinking tokens into the same
	// output stream as the text (reasoning first, then text). When false, reasoning tokens are discarded.
	OutputReasoningStream bool
}

// ToMessage converts ExecuteInput to a messages.Message for the conversation buffer.
func (in *ExecuteInput) ToMessage() messages.Message {
	var parts []messages.ContentPart

	if in.Message != "" {
		parts = append(parts, messages.TextPart{Text: in.Message})
	}
	if in.Image != nil {
		if b := encodeImageToJPEG(in.Image); len(b) > 0 {
			parts = append(parts, messages.ImagePart{Bytes: b, MediaType: "image/jpeg"})
		}
	}
	if in.Audio != nil {
		if b := encodeAudioToPCM(in.Audio); len(b) > 0 {
			parts = append(parts, messages.AudioPart{Bytes: b, MediaType: "audio/pcm"})
		}
	}
	if in.Video != nil && len(in.Video.Bytes) > 0 {
		parts = append(parts, messages.VideoPart{Bytes: in.Video.Bytes, MediaType: in.Video.MediaType})
	}
	if in.File != nil && len(in.File.Bytes) > 0 {
		parts = append(parts, messages.FilePart{Bytes: in.File.Bytes, Name: in.File.Name, MediaType: in.File.MediaType})
	}
	if len(in.ContentParts) > 0 {
		parts = append(parts, in.ContentParts...)
	}
	return messages.Message{
		Role:         messages.RoleUser,
		ContentParts: parts,
	}
}

func encodeImageToJPEG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		return nil
	}
	return buf.Bytes()
}

func encodeAudioToPCM(a *Audio) []byte {
	if a == nil || len(a.Samples) == 0 {
		return nil
	}
	return codec.EncodePCM16(a.Samples)
}

// NewExecuteInput creates an ExecuteInput with only text. Convenience for text-only prompts.
func NewExecuteInput(message string) ExecuteInput {
	return ExecuteInput{Message: message}
}
