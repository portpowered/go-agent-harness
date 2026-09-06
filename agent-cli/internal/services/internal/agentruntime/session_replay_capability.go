package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// websocketReplaySessionInferencer keeps strict websocket replays on their
// captured outbound sequence. The provider session underneath the replay
// dialer is still a real provider session and therefore exposes the live
// response-request method; exposing that method through the replay path would
// make an audio-only tool result send a new response.create event that the
// historical capture cannot accept.
type websocketReplaySessionInferencer struct {
	inner messages.SessionInferencer
}

var _ messages.SessionInferencer = (*websocketReplaySessionInferencer)(nil)

func newWebSocketReplaySessionInferencer(inner messages.SessionInferencer) messages.SessionInferencer {
	if inner == nil {
		return nil
	}
	return &websocketReplaySessionInferencer{inner: inner}
}

func (i *websocketReplaySessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return &websocketReplaySession{Session: session}, nil
}

// websocketReplaySession deliberately does not implement
// messages.SessionResponseRequester or messages.SessionResponseCapability.
// Its embedded public Session contract and the unrelated optional forwarding
// methods preserve replay behavior while keeping response continuation
// requests live-only.
type websocketReplaySession struct {
	messages.Session
}

var _ messages.Session = (*websocketReplaySession)(nil)
var _ messages.SessionSendOutcomeSender = (*websocketReplaySession)(nil)

func (s *websocketReplaySession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.Session, msg)
}

func (s *websocketReplaySession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *websocketReplaySession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *websocketReplaySession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *websocketReplaySession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *websocketReplaySession) InputDrops() int64 {
	counters, ok := s.Session.(messages.SessionDropCounters)
	if !ok {
		return 0
	}
	return counters.InputDrops()
}

func (s *websocketReplaySession) OutputDrops() int64 {
	counters, ok := s.Session.(messages.SessionDropCounters)
	if !ok {
		return 0
	}
	return counters.OutputDrops()
}

func (s *websocketReplaySession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.Session)
}

func (s *websocketReplaySession) TerminalError() error {
	return terminalSessionError(s.Session)
}
