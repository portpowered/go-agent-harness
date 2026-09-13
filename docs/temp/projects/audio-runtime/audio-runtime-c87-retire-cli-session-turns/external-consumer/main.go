package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns"
	turnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns/wire"
)

type consumerSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	connects  int
	closeCall int
	sent      []string
}

func newConsumerSession() *consumerSession {
	return &consumerSession{receive: messages.NewTypedBuffer[messages.StreamMessage](8), done: make(chan struct{})}
}

func (s *consumerSession) ConnectSession(context.Context) (messages.Session, error) {
	s.connects++
	return s, nil
}

func (s *consumerSession) Send(_ context.Context, message messages.StreamMessage) bool {
	s.sent = append(s.sent, string(message.Type))
	if message.Type != messages.StreamTypeTextDelta && message.Type != messages.StreamTypeAudioDelta {
		return true
	}
	s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("external consumer response")})
	s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	return true
}

func (s *consumerSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *consumerSession) Done() <-chan struct{}                                  { return s.done }
func (s *consumerSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeCall++
		close(s.done)
	})
	return nil
}

type report struct {
	Schema            string   `json:"schema"`
	ConstructedVia    string   `json:"constructed_via"`
	Connections       int      `json:"connections"`
	History           int      `json:"history"`
	SecondConnections int      `json:"second_connections"`
	SecondHistory     int      `json:"second_history"`
	NextIndex         uint64   `json:"next_index"`
	AudioCopied       bool     `json:"audio_copied"`
	SnapshotCopied    bool     `json:"snapshot_copied"`
	InvalidPreserved  bool     `json:"invalid_transition_preserved"`
	Isolated          bool     `json:"isolated"`
	SentTypes         []string `json:"sent_types"`
	Closed            bool     `json:"closed"`
}

func run() (report, error) {
	provider := newConsumerSession()
	secondProvider := newConsumerSession()
	service := turnwire.NewService(turnwire.Dependencies{SessionInferencer: provider})
	secondService := turnwire.NewService(turnwire.Dependencies{SessionInferencer: secondProvider})
	input := []byte{1, 2, 3, 4}
	if _, err := service.RunTurn(context.Background(), sessionturns.TurnInput{Text: "hello"}, sessionturns.TurnDirectionUser, 1, 2); err != nil {
		return report{}, fmt.Errorf("text turn: %w", err)
	}
	if _, err := service.RunTurn(context.Background(), sessionturns.TurnInput{Audio: append([]byte(nil), input...), MediaType: "audio/pcm"}, sessionturns.TurnDirectionUser, 3, 4); err != nil {
		return report{}, fmt.Errorf("audio turn: %w", err)
	}
	if _, err := secondService.RunTurn(context.Background(), sessionturns.TurnInput{Text: "independent"}, sessionturns.TurnDirectionUser, 1, 2); err != nil {
		return report{}, fmt.Errorf("independent turn: %w", err)
	}
	input[0] = 99
	history := service.History()
	audioCopied := len(history) == 2 && history[1].Input.Audio[0] == 1
	history[1].Input.Audio[0] = 88
	snapshotCopied := service.History()[1].Input.Audio[0] == 1
	_, invalidErr := service.StartTurn(sessionturns.TurnInput{Text: "late"}, sessionturns.TurnDirectionUser, 4)
	if !errors.Is(invalidErr, sessionturns.ErrInvalidTurnTick) {
		return report{}, fmt.Errorf("invalid transition: %w", invalidErr)
	}
	if err := service.Close(); err != nil {
		return report{}, fmt.Errorf("close: %w", err)
	}
	if err := secondService.Close(); err != nil {
		return report{}, fmt.Errorf("independent close: %w", err)
	}
	return report{
		Schema:            "audio-runtime.c87.public-session-turns/v1",
		ConstructedVia:    "sessionturns/wire.NewService",
		Connections:       provider.connects,
		History:           len(history),
		SecondConnections: secondProvider.connects,
		SecondHistory:     len(secondService.History()),
		NextIndex:         service.NextTurnIndex(),
		AudioCopied:       audioCopied,
		SnapshotCopied:    snapshotCopied,
		InvalidPreserved:  true,
		Isolated:          len(secondService.History()) == 1 && secondService.NextTurnIndex() == 2 && len(history) == 2,
		SentTypes:         append([]string(nil), provider.sent...),
		Closed:            provider.closeCall == 1,
	}, nil
}

func main() {
	value, err := run()
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		panic(err)
	}
}
