package cli

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func cliDurationPartialEvents() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("duration-cli", "test")},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("accepted partial transcript")},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0})},
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("accepted partial transcript")},
	}
}

func cliDurationCompleteEvents() []messages.StreamMessage {
	events := cliDurationPartialEvents()
	for index := range events {
		if events[index].Role == messages.RoleAssistant {
			events[index].ResponseID = "duration-cli-response"
		}
	}
	return append(events, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "duration-cli-response",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
}

type cliDurationInferencer struct {
	events             []messages.StreamMessage
	waitForAudioCommit bool
	audioCommits       atomic.Int32
}

func newCLIDurationInferencer(events []messages.StreamMessage) *cliDurationInferencer {
	return &cliDurationInferencer{events: events}
}

func newCLIAudioInputSuccessInferencer(events []messages.StreamMessage) *cliDurationInferencer {
	return &cliDurationInferencer{events: events, waitForAudioCommit: true}
}

func (i *cliDurationInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	responseEvents := i.events
	initialEvents := append([]messages.StreamMessage(nil), i.events...)
	if i.waitForAudioCommit {
		initialEvents = nil
		if len(i.events) > 0 && i.events[0].Type == messages.StreamTypeSessionOpen {
			initialEvents = append(initialEvents, i.events[0])
			responseEvents = i.events[1:]
		}
	}
	session := &cliDurationSession{
		receive:            messages.NewTypedBuffer[messages.StreamMessage](64),
		done:               make(chan struct{}),
		responseEvents:     responseEvents,
		waitForAudioCommit: i.waitForAudioCommit,
		audioCommits:       &i.audioCommits,
	}
	for _, event := range initialEvents {
		if !session.receive.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	return session, nil
}

type cliDurationSession struct {
	receive            *messages.TypedBuffer[messages.StreamMessage]
	done               chan struct{}
	responseEvents     []messages.StreamMessage
	waitForAudioCommit bool
	audioCommits       *atomic.Int32
	responseOnce       sync.Once
	once               sync.Once
}

func (s *cliDurationSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if message.Type == messages.StreamTypeMessageEnd && s.audioCommits != nil {
		s.audioCommits.Add(1)
	}
	if !s.waitForAudioCommit || message.Type != messages.StreamTypeMessageEnd {
		return true
	}
	succeeded := true
	s.responseOnce.Do(func() {
		for _, event := range s.responseEvents {
			if !s.receive.Write(ctx, event) {
				succeeded = false
				return
			}
		}
	})
	return succeeded
}

func (s *cliDurationSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *cliDurationSession) Done() <-chan struct{} { return s.done }

func (s *cliDurationSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

var _ messages.SessionInferencer = (*cliDurationInferencer)(nil)
var _ messages.Session = (*cliDurationSession)(nil)
