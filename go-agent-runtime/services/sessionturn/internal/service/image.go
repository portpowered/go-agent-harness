package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

type imageInferencer struct {
	inner   messages.SessionInferencer
	request sessionturn.ImageRequest
}

func newImageInferencer(inner messages.SessionInferencer, request sessionturn.ImageRequest) messages.SessionInferencer {
	request.Parts = cloneImageParts(request.Parts)
	return &imageInferencer{inner: inner, request: request}
}

func (i *imageInferencer) Request() inference.SessionRequest {
	if i == nil || i.inner == nil {
		return inference.SessionRequest{}
	}
	if source, ok := i.inner.(interface {
		Request() inference.SessionRequest
	}); ok {
		return source.Request()
	}
	return inference.SessionRequest{}
}

func (i *imageInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		return nil, fmt.Errorf("%w: image inferencer is not configured", sessionturn.ErrImageSend)
	}
	s, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return &imageSession{Session: s, parts: cloneImageParts(i.request.Parts), deferResponse: i.request.DeferResponse, firstTurn: i.request.FirstTurn, promptSentinel: i.request.PromptSentinel, deferredInstruction: i.request.DeferredInstruction}, nil
}

type imageSession struct {
	messages.Session
	parts               []messages.ImagePart
	deferResponse       bool
	firstTurn           chan error
	promptSentinel      string
	deferredInstruction string
	mu                  sync.Mutex
}

func (s *imageSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeTextDelta {
		return s.Session.Send(ctx, msg)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.parts) == 0 {
		return s.Session.Send(ctx, msg)
	}
	parts := cloneImageParts(s.parts)
	s.parts = nil
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value == nil {
		s.signal(false)
		return false
	}
	text := value.Content
	if text == s.promptSentinel {
		text = ""
		if s.deferResponse {
			text = s.deferredInstruction
		}
	}
	err := sendImageTurn(ctx, s.Session, text, parts, !s.deferResponse)
	s.signal(err == nil)
	return err == nil
}

func (s *imageSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}
func (s *imageSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}
func (s *imageSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s.Send(ctx, msg) {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if err := ctx.Err(); err != nil {
		if err == context.DeadlineExceeded {
			return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
		}
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
}
func (s *imageSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(sessionturn.CompleteMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}
func (s *imageSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}
func (s *imageSession) SupportsCompleteMessages() bool {
	if c, ok := s.Session.(sessionturn.CompleteMessageCapabilities); ok {
		return c.SupportsCompleteMessages()
	}
	_, ok := s.Session.(sessionturn.CompleteMessageSender)
	return ok
}
func (s *imageSession) SupportsCompleteMessagesWithoutResponse() bool {
	if c, ok := s.Session.(sessionturn.CompleteMessageCapabilities); ok {
		return c.SupportsCompleteMessagesWithoutResponse()
	}
	_, ok := s.Session.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok
}
func (s *imageSession) TerminalError() error {
	if source, ok := s.Session.(sessionturn.TerminalErrorSource); ok {
		return source.TerminalError()
	}
	return nil
}
func (s *imageSession) signal(sent bool) {
	if s.firstTurn == nil {
		return
	}
	if sent {
		s.firstTurn <- nil
		return
	}
	s.firstTurn <- fmt.Errorf("%w: provider session rejected image turn", sessionturn.ErrImageSend)
}

func cloneImageParts(parts []messages.ImagePart) []messages.ImagePart {
	cloned := append([]messages.ImagePart(nil), parts...)
	for i := range cloned {
		cloned[i].Bytes = append([]byte(nil), cloned[i].Bytes...)
	}
	return cloned
}
