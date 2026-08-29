package services

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"slices"
	"strings"
	"sync"
)

type (
	TurnDirection string
	TurnEventType string
	TurnEvent     struct {
		Type                     TurnEventType
		Index                    uint64
		Direction                TurnDirection
		Tick, StartTick, EndTick uint64
	}
	TurnEventSink func(TurnEvent)
	TurnInput     struct {
		Text      string
		Audio     []byte
		MediaType string
	}
	SessionTurn struct {
		Index, StartTick, EndTick uint64
		Direction                 TurnDirection
		Input                     TurnInput
		Response                  messages.Message
	}
	SessionTurnsOptions struct {
		SessionInferencer messages.SessionInferencer
		EventSink         TurnEventSink
	}
	SessionTurns struct {
		transitionMu      sync.Mutex
		mu                sync.RWMutex
		sessionInferencer messages.SessionInferencer
		session           messages.Session
		sink              TurnEventSink
		nextIndex         uint64
		history           []SessionTurn
		active            *SessionTurn
		closed            bool
	}
)

func NewTextTurnInput(text string) TurnInput         { return TurnInput{Text: text} }
func NewAudioTurnInput(a []byte, m string) TurnInput { return TurnInput{"", append(a[:0:0], a...), m} }
func (in TurnInput) Empty() bool                     { return strings.TrimSpace(in.Text) == "" && len(in.Audio) == 0 }

const TurnDirectionUser, TurnDirectionAssistant, TurnDirectionClientToServer, TurnDirectionServerToClient TurnDirection = "user", "assistant", "client_to_server", "server_to_client"
const TurnEventStart, TurnEventEnd TurnEventType = "turn-start", "turn-end"

var (
	ErrTurnAlreadyActive, ErrTurnEndWithoutStart                = errors.New("turn start while another turn is active"), errors.New("turn end without start: no active turn")
	ErrEmptyTurn, ErrInvalidTurnDirection                       = errors.New("turn content must not be empty"), errors.New("turn direction is invalid")
	ErrInvalidTurnTick, ErrSessionEndedWithActiveTurn           = errors.New("turn tick must be strictly increasing"), errors.New("session ended with active turn")
	ErrSessionClosed, ErrTurnMismatch, ErrMissingTurnInferencer = errors.New("session is closed"), errors.New("turn does not match the active turn"), errors.New("session turn inferencer is not configured")
)

func transitionError(op string, cause error) error { return fmt.Errorf("%s turn: %w", op, cause) }
func NewSessionTurns(opts SessionTurnsOptions) *SessionTurns {
	return &SessionTurns{sessionInferencer: opts.SessionInferencer, sink: opts.EventSink, nextIndex: 1}
}
func (s *SessionTurns) StartTurn(input TurnInput, direction TurnDirection, tick uint64) (turn SessionTurn, err error) {
	s.transitionMu.Lock()
	s.mu.Lock()
	var event TurnEvent
	defer func() { s.finishTransition(event, err) }()
	switch {
	case s.closed:
		return SessionTurn{}, transitionError("start", ErrSessionClosed)
	case s.active != nil:
		return SessionTurn{}, transitionError("start", ErrTurnAlreadyActive)
	case input.Empty():
		return SessionTurn{}, transitionError("start", ErrEmptyTurn)
	case direction != TurnDirectionUser && direction != TurnDirectionAssistant && direction != TurnDirectionClientToServer && direction != TurnDirectionServerToClient:
		return SessionTurn{}, transitionError("start", ErrInvalidTurnDirection)
	case len(s.history) != 0 && tick <= s.history[len(s.history)-1].EndTick:
		return SessionTurn{}, transitionError("start", ErrInvalidTurnTick)
	}
	turn = SessionTurn{Index: s.nextIndex, Direction: direction, Input: copyInput(input), StartTick: tick}
	s.active = &turn
	event = TurnEvent{Type: TurnEventStart, Index: turn.Index, Direction: direction, Tick: tick, StartTick: tick}
	return cloneTurn(turn), nil
}
func (s *SessionTurns) EndTurn(index uint64, direction TurnDirection, response messages.Message, tick uint64) (turn SessionTurn, err error) {
	s.transitionMu.Lock()
	s.mu.Lock()
	var event TurnEvent
	defer func() { s.finishTransition(event, err) }()
	if s.closed {
		return SessionTurn{}, transitionError("end", ErrSessionClosed)
	}
	if s.active == nil {
		return SessionTurn{}, transitionError("end", ErrTurnEndWithoutStart)
	}
	active := *s.active
	switch {
	case (index != 0 && index != active.Index) || (direction != "" && direction != active.Direction):
		return SessionTurn{}, transitionError("end", ErrTurnMismatch)
	case strings.TrimSpace(response.TextContent()) == "" && strings.TrimSpace(response.Refusal) == "" && len(response.ToolCalls) == 0 && !hasAudio(response.ContentParts):
		return SessionTurn{}, transitionError("end", ErrEmptyTurn)
	case tick <= active.StartTick:
		return SessionTurn{}, transitionError("end", ErrInvalidTurnTick)
	}
	active.Response, active.EndTick = response, tick
	s.history, s.nextIndex = append(s.history, cloneTurn(active)), s.nextIndex+1
	s.active = nil
	event = TurnEvent{Type: TurnEventEnd, Index: active.Index, Direction: active.Direction, Tick: tick, StartTick: active.StartTick, EndTick: tick}
	return cloneTurn(active), nil
}
func (s *SessionTurns) RunTurn(ctx context.Context, input TurnInput, direction TurnDirection, startTick, endTick uint64) (turn SessionTurn, err error) {
	started, err := s.StartTurn(input, direction, startTick)
	if err != nil {
		return SessionTurn{}, err
	}
	defer func() {
		if err != nil {
			s.abort()
		}
	}()
	session, err := s.sessionFor(ctx)
	if err != nil {
		return SessionTurn{}, err
	}
	if err = sendTurnInput(ctx, session, input); err != nil {
		return SessionTurn{}, err
	}
	response, err := readTurnResponse(ctx, session)
	if err != nil {
		return SessionTurn{}, err
	}
	return s.EndTurn(started.Index, started.Direction, response, endTick)
}
func (s *SessionTurns) History() []SessionTurn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Clone(s.history)
	for i := range out {
		out[i].Input = copyInput(out[i].Input)
	}
	return out
}
func (s *SessionTurns) NextTurnIndex() uint64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.nextIndex }
func (s *SessionTurns) Close() error {
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return transitionError("close", ErrSessionEndedWithActiveTurn)
	}
	s.closed = true
	session := s.session
	s.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}
func (s *SessionTurns) sessionFor(ctx context.Context) (messages.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		return s.session, nil
	}
	if s.sessionInferencer == nil {
		return nil, transitionError("run", ErrMissingTurnInferencer)
	}
	session, err := s.sessionInferencer.ConnectSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect turn session: %w", err)
	}
	s.session = session
	return session, nil
}
func (s *SessionTurns) finishTransition(event TurnEvent, err error) {
	s.mu.Unlock()
	defer s.transitionMu.Unlock()
	if err == nil && s.sink != nil {
		s.sink(event)
	}
}
func (s *SessionTurns) abort() {
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
}
func sendTurnInput(ctx context.Context, session messages.Session, input TurnInput) error {
	msg := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(input.Text)}
	if len(input.Audio) != 0 {
		msg = messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValueWithMediaType(input.Audio, input.MediaType)}
	}
	if !session.Send(ctx, msg) {
		return errors.Join(ctx.Err(), fmt.Errorf("send turn input: session rejected message"))
	}
	if len(input.Audio) != 0 && !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}) {
		return errors.Join(ctx.Err(), fmt.Errorf("commit turn input: session rejected message"))
	}
	return nil
}
func readTurnResponse(ctx context.Context, session messages.Session) (messages.Message, error) {
	buffer := session.Receive()
	var deltas []messages.StreamMessage
	for {
		var msg messages.StreamMessage
		select {
		case msg = <-buffer.Chan():
		case <-ctx.Done():
			return messages.Message{}, ctx.Err()
		case <-session.Done():
			return messages.Message{}, transitionError("read", ErrSessionClosed)
		}
		switch msg.Type {
		case messages.StreamTypeError:
			value, _ := msg.Value.(*messages.ErrorValue)
			if value != nil && value.IsNonTerminal() {
				continue
			}
			if value != nil && value.Err != nil {
				return messages.Message{}, value.Err
			}
			if value == nil || strings.TrimSpace(value.Message) == "" {
				return messages.Message{}, errors.New("session returned an error")
			}
			return messages.Message{}, errors.New(value.Message)
		}
		deltas = append(deltas, msg)
		if msg.Type == messages.StreamTypeMessageEnd {
			return messages.ReconstructModelMessageFromDeltas(deltas), nil
		}
	}
}
func copyInput(in TurnInput) TurnInput           { in.Audio = append([]byte(nil), in.Audio...); return in }
func cloneTurn(turn SessionTurn) SessionTurn     { turn.Input = copyInput(turn.Input); return turn }
func hasAudio(parts []messages.ContentPart) bool { return slices.ContainsFunc(parts, audioContent) }
func audioContent(part messages.ContentPart) bool {
	audio, ok := part.(messages.AudioPart)
	return ok && (strings.TrimSpace(audio.URL) != "" || len(audio.Bytes) != 0)
}
