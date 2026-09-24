package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

type stopFacts struct {
	continuationFailure bool
	scheduledFailure    bool
	endAdmitted         bool
	obligation          bool
	scheduledComplete   bool
}

func (f stopFacts) HasTerminalToolContinuationFailure() bool  { return f.continuationFailure }
func (f stopFacts) HasTerminalScheduledResponseFailure() bool { return f.scheduledFailure }
func (f stopFacts) LastMessageEndAdmitted() bool              { return f.endAdmitted }
func (f stopFacts) HasToolLifecycleObligation() bool          { return f.obligation }
func (f stopFacts) ScheduledAudioComplete() bool              { return f.scheduledComplete }

func TestShouldStopSelectsTerminalBoundaries(t *testing.T) {
	end := messages.StreamMessage{Type: messages.StreamTypeMessageEnd}
	textEnd := messages.StreamMessage{Type: messages.StreamTypeTextEnd}
	admitted := stopFacts{endAdmitted: true}
	cases := []struct {
		name   string
		msg    messages.StreamMessage
		policy sessionduration.StopPolicy
		want   bool
	}{
		{"provider close", messages.StreamMessage{Type: messages.StreamTypeSessionClose}, sessionduration.StopPolicy{WaitForClose: true}, true},
		{"loop end", messages.StreamMessage{Type: messages.StreamTypeLoopEnd}, sessionduration.StopPolicy{CloseAfterOpen: true}, true},
		{"continuation failure", end, sessionduration.StopPolicy{WaitForClose: true, Facts: stopFacts{continuationFailure: true}}, true},
		{"scheduled failure", end, sessionduration.StopPolicy{WaitForClose: true, Facts: stopFacts{scheduledFailure: true}}, true},
		{"terminal error", messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("boom")}, sessionduration.StopPolicy{WaitForClose: true}, true},
		{"untyped error", messages.StreamMessage{Type: messages.StreamTypeError}, sessionduration.StopPolicy{WaitForClose: true}, true},
		{"nonterminal error", messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewNonTerminalErrorValue("late", "response_cancel_not_active")}, sessionduration.StopPolicy{}, false},
		{"close handshake pending", end, sessionduration.StopPolicy{CloseAfterOpen: true, Facts: admitted}, false},
		{"response end without observer", end, sessionduration.StopPolicy{}, true},
		{"response end admitted", end, sessionduration.StopPolicy{Facts: admitted}, true},
		{"response end not admitted", end, sessionduration.StopPolicy{Facts: stopFacts{}}, false},
		{"response end with tool obligation", end, sessionduration.StopPolicy{Facts: stopFacts{endAdmitted: true, obligation: true}}, false},
		{"scheduled audio pending", end, sessionduration.StopPolicy{CloseAfterScheduledAudio: true, Facts: admitted}, false},
		{"scheduled audio complete", end, sessionduration.StopPolicy{CloseAfterScheduledAudio: true, Facts: stopFacts{endAdmitted: true, scheduledComplete: true}}, true},
		{"text end", textEnd, sessionduration.StopPolicy{}, true},
		{"text end with tool obligation", textEnd, sessionduration.StopPolicy{Facts: stopFacts{obligation: true}}, false},
		{"ordinary delta", messages.StreamMessage{Type: messages.StreamTypeTextDelta}, sessionduration.StopPolicy{}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ShouldStop(testCase.msg, testCase.policy); got != testCase.want {
				t.Fatalf("ShouldStop() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestDrainBufferedStopsAtTerminalBoundaryAndPropagatesFailure(t *testing.T) {
	deltas := messages.NewTypedBuffer[messages.StreamMessage](4)
	for _, msgType := range []messages.StreamMessageType{messages.StreamTypeTextDelta, messages.StreamTypeSessionClose, messages.StreamTypeTextDelta} {
		deltas.Write(context.Background(), messages.StreamMessage{Type: msgType})
	}
	var seen int
	stop, err := DrainBuffered(deltas, func(msg messages.StreamMessage) (bool, error) {
		seen++
		return msg.Type == messages.StreamTypeSessionClose, nil
	})
	if !stop || err != nil || seen != 2 {
		t.Fatalf("DrainBuffered() = %v, %v after %d messages, want stop after two", stop, err, seen)
	}
	failure := errors.New("render failed")
	if _, err := DrainBuffered(deltas, func(messages.StreamMessage) (bool, error) { return false, failure }); !errors.Is(err, failure) {
		t.Fatalf("DrainBuffered() error = %v, want %v", err, failure)
	}
	if stop, err := DrainBuffered(deltas, func(messages.StreamMessage) (bool, error) { return false, nil }); stop || err != nil {
		t.Fatalf("empty DrainBuffered() = %v, %v", stop, err)
	}
	if stop, err := DrainBuffered(nil, nil); stop || err != nil {
		t.Fatalf("nil DrainBuffered() = %v, %v", stop, err)
	}
}

type manualTimer struct{ ch chan time.Time }

func (t *manualTimer) C() <-chan time.Time { return t.ch }
func (t *manualTimer) Stop() bool          { return true }

type manualClock struct {
	timers  chan *manualTimer
	returns []sessionduration.Timer
}

func (c *manualClock) NewTimer(time.Duration) sessionduration.Timer {
	if len(c.returns) > 0 {
		next := c.returns[0]
		c.returns = c.returns[1:]
		return next
	}
	timer := &manualTimer{ch: make(chan time.Time, 1)}
	c.timers <- timer
	return timer
}

func TestDrainStragglersPublishesUntilQuiet(t *testing.T) {
	deltas := messages.NewTypedBuffer[messages.StreamMessage](4)
	clock := &manualClock{timers: make(chan *manualTimer, 4)}
	published := make(chan messages.StreamMessage, 4)
	result := make(chan error, 1)
	go func() {
		result <- DrainStragglers(context.Background(), sessionduration.StragglerDrain{
			Deltas: deltas, Clock: clock, QuietPeriod: time.Millisecond,
			Publish: func(msg messages.StreamMessage) error { published <- msg; return nil },
		})
	}()
	first := <-clock.timers
	deltas.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	<-published
	second := <-clock.timers
	if first == second {
		t.Fatal("straggler did not restart the quiet period")
	}
	second.ch <- time.Time{}
	if err := <-result; err != nil {
		t.Fatalf("DrainStragglers() = %v", err)
	}
}

func TestDrainStragglersBoundsAndFailures(t *testing.T) {
	if err := DrainStragglers(context.Background(), sessionduration.StragglerDrain{}); !errors.Is(err, ErrInvalidStragglerDrain) {
		t.Fatalf("zero quiet period = %v", err)
	}
	if err := DrainStragglers(context.Background(), sessionduration.StragglerDrain{QuietPeriod: time.Millisecond}); !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("missing clock = %v", err)
	}
	clock := &manualClock{timers: make(chan *manualTimer, 8)}
	if err := DrainStragglers(context.Background(), sessionduration.StragglerDrain{QuietPeriod: time.Millisecond, Clock: clock}); err != nil {
		t.Fatalf("nil deltas = %v", err)
	}
	nilClock := &manualClock{returns: []sessionduration.Timer{nil}}
	deltas := messages.NewTypedBuffer[messages.StreamMessage](2)
	if err := DrainStragglers(context.Background(), sessionduration.StragglerDrain{Deltas: deltas, QuietPeriod: time.Millisecond, Clock: nilClock}); !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("nil timer = %v", err)
	}
	failure := errors.New("publish failed")
	deltas.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	if err := DrainStragglers(context.Background(), sessionduration.StragglerDrain{Deltas: deltas, QuietPeriod: time.Millisecond, Clock: clock, Publish: func(messages.StreamMessage) error { return failure }}); !errors.Is(err, failure) {
		t.Fatalf("publish failure = %v", err)
	}
	if err := DrainStragglers(context.Background(), sessionduration.StragglerDrain{Deltas: deltas, QuietPeriod: time.Millisecond, Clock: clock, WallSafety: time.Millisecond}); err != nil {
		t.Fatalf("wall bound = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := DrainStragglers(context.Background(), sessionduration.StragglerDrain{Deltas: deltas, QuietPeriod: time.Millisecond, Clock: &manualClock{returns: []sessionduration.Timer{&manualTimer{ch: firedTimerChannel()}}}}); err != nil {
		t.Fatalf("quiet expiry = %v", err)
	}
	if err := DrainStragglers(ctx, sessionduration.StragglerDrain{Deltas: deltas, QuietPeriod: time.Millisecond, Clock: clock}); err != nil {
		t.Fatalf("cancelled drain = %v", err)
	}
	resetClock := &manualClock{returns: []sessionduration.Timer{&manualTimer{ch: make(chan time.Time)}, nil}}
	deltas.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	if err := DrainStragglers(context.Background(), sessionduration.StragglerDrain{Deltas: deltas, QuietPeriod: time.Millisecond, Clock: resetClock}); !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("nil reset timer = %v", err)
	}
}

func firedTimerChannel() chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func TestFanInForwardsFirstFailureAndAnyCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if FanInErrors(ctx, []<-chan error{nil}) != nil || FanInDone(ctx, []<-chan struct{}{nil}) != nil {
		t.Fatal("empty fan-in returned a signal")
	}
	single := make(chan error)
	if FanInErrors(ctx, []<-chan error{nil, single}) != (<-chan error)(single) {
		t.Fatal("single error source was wrapped")
	}
	first, second, closed := make(chan error, 2), make(chan error, 1), make(chan error)
	close(closed)
	merged := FanInErrors(ctx, []<-chan error{first, second, closed})
	failure := errors.New("pump failed")
	first <- nil
	first <- failure
	if err := <-merged; !errors.Is(err, failure) {
		t.Fatalf("merged failure = %v", err)
	}
	singleDone := make(chan struct{})
	if FanInDone(ctx, []<-chan struct{}{singleDone}) != (<-chan struct{})(singleDone) {
		t.Fatal("single done source was wrapped")
	}
	a, b := make(chan struct{}), make(chan struct{})
	done := FanInDone(ctx, []<-chan struct{}{a, b})
	close(b)
	<-done
	cancel()
}

func TestTerminationErrorDecoratesProviderClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deltaErr := &engine.StreamDeltaError{Value: messages.NewErrorValueWithTerminal("quota exceeded", "rate_limited", messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, messages.TerminalOutputNone)}
	err := TerminationError(ctx, deltaErr)
	if !errors.Is(err, context.Canceled) || !errors.As(err, new(*engine.StreamDeltaError)) {
		t.Fatalf("TerminationError() = %v, want caller cancellation and delta identity", err)
	}
	if got := TerminationError(context.Background(), nil); got != nil {
		t.Fatalf("TerminationError(nil) = %v", got)
	}
	plain := errors.New("plain")
	if got := decorateStreamTerminalError(plain); errors.As(got, new(*streamTerminalError)) || !errors.Is(got, plain) {
		t.Fatalf("plain error decorated: %v", got)
	}
	unclassified := &engine.StreamDeltaError{Value: &messages.ErrorValue{}}
	if got := decorateStreamTerminalError(unclassified); errors.As(got, new(*streamTerminalError)) {
		t.Fatalf("unclassified error decorated: %v", got)
	}
}
