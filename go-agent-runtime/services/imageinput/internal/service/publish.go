package service

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
)

func (service *Service) Attach(inner messages.SessionInferencer, parts []messages.ImagePart, options imageinput.TurnOptions) (imageinput.Attachment, error) {
	if service == nil {
		return imageinput.Attachment{}, errors.New("attach image input: service is nil")
	}
	if inner == nil {
		return imageinput.Attachment{}, errors.New("attach image input: session inferencer is nil")
	}
	firstTurn := make(chan error, 1)
	wrapped := &inferencer{
		owner:     service,
		inner:     inner,
		parts:     cloneImageParts(parts),
		options:   options,
		firstTurn: firstTurn,
	}
	return imageinput.Attachment{Inferencer: wrapped, FirstTurn: firstTurn}, nil
}

func (service *Service) Send(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart, options imageinput.TurnOptions) error {
	if session == nil {
		return sendFailure(ctx, nil, "provider session is nil")
	}
	message := messages.Message{Role: messages.RoleUser, ContentParts: make([]messages.ContentPart, 0, len(parts)+1)}
	if text != "" {
		message.ContentParts = append(message.ContentParts, messages.TextPart{Text: text})
	}
	for _, part := range parts {
		message.ContentParts = append(message.ContentParts, messages.ImagePart{
			URL:       part.URL,
			Bytes:     cloneBytes(part.Bytes),
			MediaType: part.MediaType,
		})
	}
	return sendCompleteMessage(ctx, session, message, options.DeferResponse)
}

func sendCompleteMessage(ctx context.Context, session messages.Session, message messages.Message, deferResponse bool) error {
	if deferResponse {
		return sendDeferredMessage(ctx, session, message)
	}
	return sendImmediateMessage(ctx, session, message)
}

func sendDeferredMessage(ctx context.Context, session messages.Session, message messages.Message) error {
	const mode = "without-response"
	if !supportsCompleteMessagesWithoutResponse(session) {
		return sendFailure(ctx, session, mode+" capability is unavailable")
	}
	if sender, ok := session.(imageinput.MessageSenderWithoutResponseWithError); ok {
		if err := sender.SendMessageWithoutResponseWithError(ctx, message); err != nil {
			return &imageinput.SendError{Mode: mode, Cause: err}
		}
		return nil
	}
	sender, ok := session.(imageinput.MessageSenderWithoutResponse)
	if ok && sender.SendMessageWithoutResponse(ctx, message) {
		return nil
	}
	return sendFailure(ctx, session, mode)
}

func sendImmediateMessage(ctx context.Context, session messages.Session, message messages.Message) error {
	const mode = "response"
	if !supportsCompleteMessages(session) {
		return sendFailure(ctx, session, mode+" capability is unavailable")
	}
	if sender, ok := session.(imageinput.MessageSenderWithError); ok {
		if err := sender.SendMessageWithError(ctx, message); err != nil {
			return &imageinput.SendError{Mode: mode, Cause: err}
		}
		return nil
	}
	sender, ok := session.(imageinput.MessageSender)
	if ok && sender.SendMessage(ctx, message) {
		return nil
	}
	return sendFailure(ctx, session, mode)
}

func supportsCompleteMessages(session messages.Session) bool {
	if capability, ok := session.(imageinput.CompleteMessageCapabilities); ok {
		return capability.SupportsCompleteMessages()
	}
	_, boolSender := session.(imageinput.MessageSender)
	_, errorSender := session.(imageinput.MessageSenderWithError)
	return boolSender || errorSender
}

func supportsCompleteMessagesWithoutResponse(session messages.Session) bool {
	if capability, ok := session.(imageinput.CompleteMessageCapabilities); ok {
		return capability.SupportsCompleteMessagesWithoutResponse()
	}
	_, boolSender := session.(imageinput.MessageSenderWithoutResponse)
	_, errorSender := session.(imageinput.MessageSenderWithoutResponseWithError)
	return boolSender || errorSender
}

func sendFailure(ctx context.Context, session messages.Session, mode string) error {
	var cause error
	if ctx != nil {
		cause = ctx.Err()
	}
	if cause == nil && session != nil {
		if done := session.Done(); done != nil {
			select {
			case <-done:
				cause = errors.New("provider session is shut down")
			default:
			}
		}
	}
	return &imageinput.SendError{Mode: mode, Cause: cause}
}

type inferencer struct {
	owner     *Service
	inner     messages.SessionInferencer
	parts     []messages.ImagePart
	options   imageinput.TurnOptions
	firstTurn chan error
}

var _ messages.SessionInferencer = (*inferencer)(nil)

func (inferencer *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	rawSession, err := inferencer.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	if rawSession == nil {
		return nil, errors.New("image input session inferencer returned a nil session")
	}
	wrapped := &session{
		owner:     inferencer.owner,
		Session:   rawSession,
		parts:     cloneImageParts(inferencer.parts),
		options:   inferencer.options,
		firstTurn: inferencer.firstTurn,
	}
	go wrapped.watchDone()
	return wrapped, nil
}

type session struct {
	messages.Session
	owner     *Service
	parts     []messages.ImagePart
	options   imageinput.TurnOptions
	firstTurn chan error
	mu        sync.Mutex
	once      sync.Once
}

var _ imageinput.ForwardingSession = (*session)(nil)

func (session *session) Send(ctx context.Context, message messages.StreamMessage) bool {
	if message.Type != messages.StreamTypeTextDelta {
		return session.Session.Send(ctx, message)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.parts) == 0 {
		return session.Session.Send(ctx, message)
	}
	parts := cloneImageParts(session.parts)
	session.parts = nil
	value, ok := message.Value.(*messages.TextDeltaValue)
	if !ok || value == nil {
		session.signal(sendFailure(ctx, session.Session, "first image turn"))
		return false
	}
	text := value.Content
	if text == imageinput.ImageOnlyPrompt {
		text = ""
		if session.options.DeferResponse {
			text = imageinput.DeferredInstruction
		}
	}
	err := session.owner.Send(ctx, session.Session, text, parts, session.options)
	session.signal(err)
	return err == nil
}

func (session *session) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, session.Session)
}

func (session *session) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(session.Session)
}

func (session *session) SendMessage(ctx context.Context, message messages.Message) bool {
	if sender, ok := session.Session.(imageinput.MessageSenderWithError); ok {
		return sender.SendMessageWithError(ctx, message) == nil
	}
	sender, ok := session.Session.(imageinput.MessageSender)
	return ok && sender.SendMessage(ctx, message)
}

func (session *session) SendMessageWithoutResponse(ctx context.Context, message messages.Message) bool {
	if sender, ok := session.Session.(imageinput.MessageSenderWithoutResponseWithError); ok {
		return sender.SendMessageWithoutResponseWithError(ctx, message) == nil
	}
	sender, ok := session.Session.(imageinput.MessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, message)
}

func (session *session) SupportsCompleteMessages() bool {
	return supportsCompleteMessages(session.Session)
}

func (session *session) SupportsCompleteMessagesWithoutResponse() bool {
	return supportsCompleteMessagesWithoutResponse(session.Session)
}

func (session *session) TerminalError() error {
	if source, ok := session.Session.(imageinput.TerminalErrorSource); ok {
		return source.TerminalError()
	}
	return nil
}

// UnderlyingSession is intentionally narrow: a host may preserve an
// unrelated transport capability while the image service owns all image and
// complete-message forwarding behavior.
func (session *session) UnderlyingSession() messages.Session { return session.Session }

func (session *session) Close() error {
	err := session.Session.Close()
	if err != nil {
		session.signal(&imageinput.SendError{Mode: "session close", Cause: err})
	} else {
		session.signal(&imageinput.SendError{Mode: "session close"})
	}
	return err
}

func (session *session) signal(err error) {
	if session.firstTurn == nil {
		return
	}
	session.once.Do(func() {
		if err == nil {
			session.firstTurn <- nil
			return
		}
		if errors.Is(err, imageinput.ErrSend) {
			session.firstTurn <- err
			return
		}
		session.firstTurn <- &imageinput.SendError{Mode: "first image turn", Cause: err}
	})
}

func (session *session) watchDone() {
	done := session.Session.Done()
	if done == nil {
		return
	}
	<-done
	session.signal(&imageinput.SendError{Mode: "provider shutdown"})
}

func cloneImageParts(parts []messages.ImagePart) []messages.ImagePart {
	cloned := make([]messages.ImagePart, len(parts))
	for index, part := range parts {
		cloned[index] = messages.ImagePart{
			URL:       part.URL,
			Bytes:     cloneBytes(part.Bytes),
			MediaType: part.MediaType,
		}
	}
	return cloned
}
