package agentloop

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
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

// SendAudioInput injects raw PCM audio into the running session loop for barge-in
// and user audio forwarding. Its unspecified origin preserves the legacy
// interrupting-by-default behavior. Only meaningful in DuplexSession mode.
func (al *AgentLoop) SendAudioInput(ctx context.Context, pcm []byte) error {
	return al.sendSessionInput(ctx, "SendAudioInput", participants.SessionAudio(pcm, messages.SessionAudioInputPolicyDefault), participants.SessionAdmitWaiting)
}

// SendAudioInputWithPolicy injects raw PCM audio together with the admission
// policy that was established by the caller. The policy is evaluated only for
// contentful audio while an eligible response is active; silence is always
// forwarded without cancellation. Unknown policies use the interrupting
// default defined by messages.SessionAudioInputPolicy.
func (al *AgentLoop) SendAudioInputWithPolicy(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy) error {
	return al.sendSessionInput(ctx, "SendAudioInputWithPolicy", participants.SessionAudio(pcm, policy), participants.SessionAdmitWaiting)
}

// SendSessionEvent delivers a pre-built outbound StreamMessage to the running
// session (DuplexSession mode). The message is forwarded to the provider
// session unchanged and in order relative to audio sent via SendAudioInput.
// It carries control-plane turns such as MESSAGE.END, which realtime
// providers translate into input_audio_buffer.commit plus response.create.
func (al *AgentLoop) SendSessionEvent(ctx context.Context, msg messages.StreamMessage) error {
	return al.sendSessionInput(ctx, "SendSessionEvent", participants.SessionEvent(msg), participants.SessionAdmitOrFail)
}

// sendSessionInput admits one input to the session runner's ordered ingress.
func (al *AgentLoop) sendSessionInput(ctx context.Context, operation string, input participants.SessionInput, admission participants.SessionAdmission) error {
	mr := al.engine.GetModelRunner()
	if !mr.SessionMode() {
		return fmt.Errorf("%s: not in session mode", operation)
	}
	return mr.EnqueueSessionInput(ctx, input, admission)
}

// enqueueSessionEvent adapts the runner's ingress to the non-waiting event
// enqueuer used by the tool-result forwarder and acknowledgements.
func enqueueSessionEvent(mr *participants.ModelRunner) func(context.Context, messages.StreamMessage) error {
	return func(ctx context.Context, msg messages.StreamMessage) error {
		return mr.EnqueueSessionInput(ctx, participants.SessionEvent(msg), participants.SessionAdmitOrFail)
	}
}

// SendSessionMessage delivers one complete message through the same bounded,
// ordered session ingress as PCM and control events. Rich providers use this
// for multimodal opening turns; requestResponse selects whether the provider
// starts a response immediately or waits for a later audio boundary.
func (al *AgentLoop) SendSessionMessage(ctx context.Context, msg messages.Message, requestResponse bool) error {
	return al.sendSessionInput(ctx, "SendSessionMessage", participants.SessionMessage(msg, requestResponse), participants.SessionAdmitOrFail)
}

// SendSessionEventWaiting is SendSessionEvent with backpressure: when the
// ordered session ingress is full (for example right after an unpaced audio
// burst admitted through SendAudioInput), it waits for capacity or ctx
// cancellation instead of failing with ErrSessionInputQueueFull. Callers that
// sequence turn boundaries after audio use it.
func (al *AgentLoop) SendSessionEventWaiting(ctx context.Context, msg messages.StreamMessage) error {
	return al.sendSessionInput(ctx, "SendSessionEventWaiting", participants.SessionEvent(msg), participants.SessionAdmitWaiting)
}
