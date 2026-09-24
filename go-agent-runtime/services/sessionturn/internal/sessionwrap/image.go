package sessionwrap

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// ImageInferencer attaches validated image parts to the first user turn.
type ImageInferencer struct {
	inner messages.SessionInferencer
	parts []messages.ImagePart
	// firstTurn reports the outcome of the one image turn: nil once its wire
	// events reached the provider's outbound queue, or the rejection. It is
	// buffered so signaling never blocks the model runner.
	firstTurn chan error
	// deferResponse queues only the image item when audio input will complete
	// the turn.
	deferResponse bool
}

var _ messages.SessionInferencer = (*ImageInferencer)(nil)

// NewImageInferencer copies the parts and allocates the first-turn signal.
func NewImageInferencer(inner messages.SessionInferencer, parts []messages.ImagePart, deferResponse bool) *ImageInferencer {
	return &ImageInferencer{inner: inner, parts: CloneImageParts(parts), firstTurn: make(chan error, 1), deferResponse: deferResponse}
}

// Unwrap exposes the provider inferencer for host introspection.
func (i *ImageInferencer) Unwrap() messages.SessionInferencer { return i.inner }

// FirstTurn reports whether the image turn reached the provider.
func (i *ImageInferencer) FirstTurn() <-chan error { return i.firstTurn }

// ConnectSession connects the provider session that carries the image turn.
func (i *ImageInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return &imageSession{
		Session:       session,
		forwarder:     forwarder{inner: session},
		parts:         CloneImageParts(i.parts),
		firstTurn:     i.firstTurn,
		deferResponse: i.deferResponse,
	}, nil
}

// imageSession exposes the stream session plus the explicitly forwarded
// optional capabilities, so a later read_image result reaches the same
// provider connection.
type imageSession struct {
	messages.Session
	forwarder
	parts         []messages.ImagePart
	mu            sync.Mutex
	firstTurn     chan error
	deferResponse bool
}

// Send replaces the first text delta with one multimodal user message.
func (s *imageSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeTextDelta {
		return s.Session.Send(ctx, msg)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.parts) == 0 {
		return s.Session.Send(ctx, msg)
	}
	parts := CloneImageParts(s.parts)
	s.parts = nil
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value == nil {
		s.signalFirstTurn(false)
		return false
	}
	text := value.Content
	if text == sessionturn.ImageOnlyPrompt {
		text = ""
		if s.deferResponse {
			text = sessionturn.ImageDeferredInstruction
		}
	}
	sent := SendImageTurn(ctx, s.Session, text, parts, !s.deferResponse)
	s.signalFirstTurn(sent)
	return sent
}

// signalFirstTurn reports the image-turn outcome exactly once without
// blocking.
func (s *imageSession) signalFirstTurn(sent bool) {
	if s.firstTurn == nil {
		return
	}
	if sent {
		s.firstTurn <- nil
		return
	}
	s.firstTurn <- ImageSendError()
}

// ImageSendError is the rejection reported for an image turn.
func ImageSendError() error {
	return fmt.Errorf("%w: provider session rejected image turn", sessionturn.ErrImageSend)
}

// SendImageTurn sends text and parts as one user message, requesting a
// response or only queueing the item.
func SendImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart, requestResponse bool) bool {
	content := make([]messages.ContentPart, 0, len(parts)+1)
	if text != "" {
		content = append(content, messages.TextPart{Text: text})
	}
	for _, part := range parts {
		content = append(content, messages.ImagePart{Bytes: append([]byte(nil), part.Bytes...), MediaType: part.MediaType})
	}
	message := messages.Message{Role: messages.RoleUser, ContentParts: content}
	if requestResponse {
		sender, ok := session.(sessionturn.CompleteMessageSender)
		return ok && sender.SendMessage(ctx, message)
	}
	sender, ok := session.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, message)
}

// CloneImageParts deep-copies image parts.
func CloneImageParts(parts []messages.ImagePart) []messages.ImagePart {
	cloned := slices.Clone(parts)
	for i := range cloned {
		cloned[i].Bytes = append([]byte(nil), cloned[i].Bytes...)
	}
	return cloned
}
