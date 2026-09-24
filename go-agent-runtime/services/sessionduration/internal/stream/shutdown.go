package stream

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

type stragglerDrainError string

func (e stragglerDrainError) Error() string { return string(e) }

// ErrInvalidStragglerDrain identifies a drain without a positive quiet period.
const ErrInvalidStragglerDrain stragglerDrainError = "session straggler drain requires a positive quiet period"

// ShouldStop selects the terminal boundary of a session loop. Provider closure
// and terminal provider errors are authoritative; ordinary response boundaries
// stop only when no configured close handshake or tool obligation remains.
func ShouldStop(msg messages.StreamMessage, policy sessionduration.StopPolicy) bool {
	if msg.Type == messages.StreamTypeSessionClose || msg.Type == messages.StreamTypeLoopEnd {
		return true
	}
	facts := policy.Facts
	if msg.Type == messages.StreamTypeMessageEnd && facts != nil &&
		(facts.HasTerminalToolContinuationFailure() || facts.HasTerminalScheduledResponseFailure()) {
		return true
	}
	if isTerminalError(msg) {
		return true
	}
	if policy.CloseAfterOpen || policy.WaitForClose {
		return false
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		return responseEndStops(policy)
	}
	if msg.Type == messages.StreamTypeTextEnd {
		return facts == nil || !facts.HasToolLifecycleObligation()
	}
	return false
}

func responseEndStops(policy sessionduration.StopPolicy) bool {
	facts := policy.Facts
	if facts == nil {
		return true
	}
	if !facts.LastMessageEndAdmitted() || facts.HasToolLifecycleObligation() {
		return false
	}
	return !policy.CloseAfterScheduledAudio || facts.ScheduledAudioComplete()
}

func isTerminalError(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return !ok || value.IsTerminal()
}

// DrainBuffered consumes only messages already published to deltas. It never
// waits for a future provider message and stops at the first message handle
// reports as a terminal boundary. A clean loop completion is a publication
// barrier, so hosts inspect this buffer before treating it as terminal.
func DrainBuffered(deltas *messages.TypedBuffer[messages.StreamMessage], handle func(messages.StreamMessage) (bool, error)) (bool, error) {
	if deltas == nil || handle == nil {
		return false, nil
	}
	for {
		msg, ok := deltas.Read()
		if !ok {
			return false, nil
		}
		if stop, err := handle(msg); err != nil || stop {
			return stop, err
		}
	}
}

// DrainStragglers publishes provider output until the quiet period elapses
// without another message, the optional wall bound expires, or ctx ends.
func DrainStragglers(ctx context.Context, drain sessionduration.StragglerDrain) error { //nolint:contextcheck // nil contexts use the session API's documented background behavior.
	if drain.QuietPeriod <= 0 {
		return ErrInvalidStragglerDrain
	}
	if drain.Clock == nil {
		return fmt.Errorf("session straggler drain: %w", sessionduration.ErrSchedulerUnavailable)
	}
	if drain.Deltas == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	idle, err := newQuietTimer(drain.Clock, drain.QuietPeriod, nil)
	if err != nil {
		return err
	}
	defer func() { idle.Stop() }()
	var wall <-chan time.Time
	if drain.WallSafety > 0 {
		wallTimer := time.NewTimer(drain.WallSafety)
		defer wallTimer.Stop()
		wall = wallTimer.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-wall:
			return nil
		case <-idle.C():
			return nil
		case msg, ok := <-drain.Deltas.Chan():
			if !ok {
				return nil
			}
			if idle, err = publishStraggler(drain, idle, msg); err != nil {
				return err
			}
		}
	}
}

func publishStraggler(drain sessionduration.StragglerDrain, idle sessionduration.Timer, msg messages.StreamMessage) (sessionduration.Timer, error) {
	if drain.Publish != nil {
		if err := drain.Publish(msg); err != nil {
			return idle, err
		}
	}
	return newQuietTimer(drain.Clock, drain.QuietPeriod, idle)
}

// newQuietTimer stops and drains previous before starting the next quiet
// period on clock.
func newQuietTimer(clock sessionduration.TimerScheduler, quiet time.Duration, previous sessionduration.Timer) (sessionduration.Timer, error) {
	if previous != nil && !previous.Stop() {
		select {
		case <-previous.C():
		default:
		}
	}
	next := clock.NewTimer(quiet)
	if next == nil {
		return previous, fmt.Errorf("session duration clock returned a nil drain timer: %w", sessionduration.ErrSchedulerUnavailable)
	}
	return next, nil
}

// FanInErrors forwards at most one non-nil failure from each source until ctx
// ends. A single source is returned unchanged.
func FanInErrors(ctx context.Context, sources []<-chan error) <-chan error {
	live := make([]<-chan error, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			live = append(live, source)
		}
	}
	if len(live) <= 1 {
		if len(live) == 0 {
			return nil
		}
		return live[0]
	}
	// Each source forwards at most one failure, so the buffer never blocks.
	merged := make(chan error, len(live))
	for _, source := range live {
		go forwardFirstError(ctx, source, merged)
	}
	return merged
}

func forwardFirstError(ctx context.Context, source <-chan error, merged chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-source:
			if !ok {
				return
			}
			if err != nil {
				merged <- err
				return
			}
		}
	}
}

// FanInDone closes its result when any source closes, until ctx ends. A
// single source is returned unchanged.
func FanInDone(ctx context.Context, sources []<-chan struct{}) <-chan struct{} {
	live := make([]<-chan struct{}, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			live = append(live, source)
		}
	}
	if len(live) <= 1 {
		if len(live) == 0 {
			return nil
		}
		return live[0]
	}
	merged := make(chan struct{})
	var once sync.Once
	for _, source := range live {
		go func(source <-chan struct{}) {
			select {
			case <-source:
				once.Do(func() { close(merged) })
			case <-ctx.Done():
			}
		}(source)
	}
	return merged
}
