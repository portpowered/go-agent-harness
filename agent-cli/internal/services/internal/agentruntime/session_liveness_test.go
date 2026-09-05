package agentruntime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type livenessTestClock struct {
	mu      sync.Mutex
	timers  []*livenessTestTimer
	created chan struct{}
}

func (c *livenessTestClock) NewTimer(time.Duration) SessionLivenessTimer {
	timer := &livenessTestTimer{ch: make(chan time.Time, 1), active: true}
	c.mu.Lock()
	c.timers = append(c.timers, timer)
	created := c.created
	c.mu.Unlock()
	if created != nil {
		select {
		case created <- struct{}{}:
		default:
		}
	}
	return timer
}

func (c *livenessTestClock) latestActiveTimer() *livenessTestTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.timers) - 1; index >= 0; index-- {
		timer := c.timers[index]
		timer.mu.Lock()
		active := timer.active
		timer.mu.Unlock()
		if active {
			return timer
		}
	}
	return nil
}

func (c *livenessTestClock) fireLatest() bool {
	timer := c.latestActiveTimer()
	return timer != nil && timer.fire()
}

type livenessTestTimer struct {
	mu     sync.Mutex
	ch     chan time.Time
	active bool
}

func (t *livenessTestTimer) C() <-chan time.Time { return t.ch }

func (t *livenessTestTimer) Stop() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	wasActive := t.active
	t.active = false
	t.mu.Unlock()
	return wasActive
}

func (t *livenessTestTimer) fire() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return false
	}
	t.active = false
	t.mu.Unlock()
	t.ch <- time.Time{}
	return true
}

func waitForLivenessFailure(t *testing.T, observer *sessionProgressObserver) error {
	t.Helper()
	select {
	case <-observer.livenessEvents():
		return observer.livenessFailure()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider liveness failure")
		return nil
	}
}

func TestSessionProgressObserver_WatchdogTimesOutExactlyOnce(t *testing.T) {
	clock := &livenessTestClock{}
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "test-provider", "test-model")
	observer.setLivenessClock(clock)
	defer observer.stopLiveness()
	var callbacks atomic.Int32
	observer.livenessObserver = func(error) { callbacks.Add(1) }

	observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	if !clock.fireLatest() {
		t.Fatal("watchdog timer was not armed")
	}
	livenessErr := waitForLivenessFailure(t, observer)
	if !errors.Is(livenessErr, ErrSilentProviderTimeout) {
		t.Fatalf("liveness error = %v, want ErrSilentProviderTimeout", livenessErr)
	}
	var typedErr *SessionLivenessError
	if !errors.As(livenessErr, &typedErr) || typedErr.Classification != SessionSilentProviderTimeoutClassification {
		t.Fatalf("liveness error = %#v, want typed timeout", livenessErr)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("liveness callback count = %d, want 1", callbacks.Load())
	}

	// A late provider event or a second dispatch cannot replace the first
	// timeout cause or invoke the room lifecycle callback again.
	observer.observeProviderEvent(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("late")})
	observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	if callbacks.Load() != 1 {
		t.Fatalf("late liveness callback count = %d, want 1", callbacks.Load())
	}
	if err := observer.finish(livenessErr); !errors.Is(err, ErrSilentProviderTimeout) {
		t.Fatalf("finish error = %v, want timeout", err)
	}
	records := sink.events(SessionDiagnosticEventFailure)
	if len(records) != 1 || records[0].Fields[fieldClassification] != SessionSilentProviderTimeoutClassification {
		t.Fatalf("timeout diagnostic records = %#v, want one typed failure", records)
	}
}

func TestSessionProgressObserver_WatchdogResetsOnProviderEvent(t *testing.T) {
	clock := &livenessTestClock{}
	observer := newSessionProgressObserver(nil, nil, "test-provider", "test-model")
	observer.setLivenessClock(clock)
	defer observer.stopLiveness()

	observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	first := clock.latestActiveTimer()
	if first == nil {
		t.Fatal("watchdog timer was not armed")
	}
	observer.observeProviderEvent(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("progress")})
	second := clock.latestActiveTimer()
	if second == nil || second == first {
		t.Fatal("provider progress did not replace the watchdog timer")
	}
	if first.fire() {
		t.Fatal("replaced watchdog timer remained active")
	}
	select {
	case <-observer.livenessEvents():
		t.Fatal("replaced watchdog timer woke the liveness controller")
	default:
	}
	if err := observer.livenessFailure(); err != nil {
		t.Fatalf("replaced watchdog timer produced failure: %v", err)
	}

	if !second.fire() {
		t.Fatal("current watchdog timer did not fire")
	}
	if err := waitForLivenessFailure(t, observer); !errors.Is(err, ErrSilentProviderTimeout) {
		t.Fatalf("liveness error = %v, want timeout", err)
	}
}

func TestSessionProgressObserver_WatchdogDisarmsForTerminalAndLocalTool(t *testing.T) {
	t.Run("normal terminal", func(t *testing.T) {
		clock := &livenessTestClock{}
		observer := newSessionProgressObserver(nil, nil, "test-provider", "test-model")
		observer.setLivenessClock(clock)
		defer observer.stopLiveness()

		observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("done")})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
		if clock.fireLatest() {
			t.Fatal("terminal response left the watchdog armed")
		}
		if err := observer.livenessFailure(); err != nil {
			t.Fatalf("terminal response produced liveness failure: %v", err)
		}
	})

	t.Run("local tool", func(t *testing.T) {
		clock := &livenessTestClock{}
		observer := newSessionProgressObserver(nil, nil, "test-provider", "test-model")
		observer.setLivenessClock(clock)
		defer observer.stopLiveness()

		observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		observer.beginLocalToolExecution()
		if clock.fireLatest() {
			t.Fatal("local tool execution left the watchdog armed")
		}
		observer.endLocalToolExecution()
		observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		if !clock.fireLatest() {
			t.Fatal("ordinary continuation did not re-arm the watchdog")
		}
		if err := waitForLivenessFailure(t, observer); !errors.Is(err, ErrSilentProviderTimeout) {
			t.Fatalf("liveness error = %v, want timeout after continuation", err)
		}
	})
}

func TestSessionProgressObserver_DoesNotArmWhileSessionIsQuiet(t *testing.T) {
	clock := &livenessTestClock{}
	observer := newSessionProgressObserver(nil, nil, "test-provider", "test-model")
	observer.setLivenessClock(clock)
	defer observer.stopLiveness()

	observer.observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("session", "test")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("unsolicited")})
	if timer := clock.latestActiveTimer(); timer != nil {
		t.Fatal("quiet session unexpectedly armed provider watchdog")
	}
}

func TestSessionProgressObserver_WatchdogUsesInjectedDeterministicClock(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	observer := newSessionProgressObserver(nil, nil, "test-provider", "test-model")
	observer.setLivenessClock(sessionLivenessClockFromSource(clock))
	defer observer.stopLiveness()

	observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	clock.AdvanceTo(9)
	if err := observer.livenessFailure(); err != nil {
		t.Fatalf("watchdog fired before injected deadline: %v", err)
	}
	clock.AdvanceTo(10)
	if err := waitForLivenessFailure(t, observer); !errors.Is(err, ErrSilentProviderTimeout) {
		t.Fatalf("liveness error = %v, want timeout at injected deadline", err)
	}
}

func TestRunAgentLoopSessionWithDuration_WatchdogWakesLoop(t *testing.T) {
	livenessClock := &livenessTestClock{created: make(chan struct{}, 1)}
	observer := newSessionProgressObserver(nil, nil, "test-provider", "test-model")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inferencer := &durationTestInferencer{
		connectedCh: make(chan struct{}),
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("silent-session", "test")},
		},
	}
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runAgentLoopSession(
			ctx,
			io.Discard,
			inferencer,
			sessionLoopOptions{
				Prompt:        "wait for a response",
				WaitForClose:  true,
				observer:      observer,
				livenessClock: livenessClock,
			},
		)
	}()

	select {
	case err := <-runErrCh:
		t.Fatalf("session stopped before watchdog arm: %v", err)
	case <-livenessClock.created:
	case <-time.After(2 * time.Second):
		select {
		case <-inferencer.connectedCh:
		default:
			t.Fatal("session did not connect or arm provider watchdog")
		}
		t.Fatal("session connected but did not arm provider watchdog after dispatch")
	}
	if !livenessClock.fireLatest() {
		t.Fatal("session watchdog timer did not fire")
	}
	select {
	case err := <-runErrCh:
		if !errors.Is(err, ErrSilentProviderTimeout) {
			t.Fatalf("session error = %v, want timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session loop did not stop after provider watchdog timeout")
	}
}

func TestRunRoom_WatchdogOnlyFailsSilentParticipant(t *testing.T) {
	ids := []string{"silent", "viable"}
	inferencers := map[string]*roomTestInferencer{
		"silent": {events: []messages.StreamMessage{roomTestSessionOpen("silent")}},
		"viable": {events: []messages.StreamMessage{roomTestSessionOpen("viable")}},
	}
	viableConnected := make(chan struct{})
	inferencers["viable"].onConnect = func() { close(viableConnected) }
	options, _ := newRoomTestRunOptions(ids, inferencers)
	options.Manifest.Room.MaxDuration = time.Hour
	options.Manifest.Participants[0].OpeningPrompt = "the silent participant needs a response"
	livenessClock := &livenessTestClock{created: make(chan struct{}, 4)}
	options.LivenessClock = livenessClock

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, options)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	select {
	case <-livenessClock.created:
	case <-ctx.Done():
		t.Fatal("room did not arm the silent participant watchdog")
	}
	select {
	case <-viableConnected:
	case <-ctx.Done():
		t.Fatal("viable participant did not connect")
	}
	if !livenessClock.fireLatest() {
		t.Fatal("room silent participant watchdog did not fire")
	}
	var viableSessions []*roomTestSession
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(viableSessions) == 0 {
		viableSessions = inferencers["viable"].sessionsSnapshot()
		if len(viableSessions) > 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("viable session was not recorded after connect")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if len(viableSessions) != 1 {
		t.Fatalf("viable sessions = %d, want one", len(viableSessions))
	}
	// Keep the room open long enough for the silent participant's failure to
	// be observed, then let the surviving participant finish so the live-room
	// coordinator reaches its terminal empty-participant boundary.
	viableSessions[0].end()

	var got roomTestRunOutcome
	select {
	case got = <-outcome:
	case <-time.After(2 * time.Second):
		t.Fatal("room did not terminate after silent participant timeout")
	}
	if got.err != nil {
		t.Fatalf("room after isolated silent participant returned an error: %v", got.err)
	}
	if got.result.Reason != RoomTerminationStopped {
		t.Fatalf("room reason = %q, want stopped after the viable participant ended", got.result.Reason)
	}
	silent, ok := got.result.Participants["silent"]
	if !ok {
		t.Fatal("room result is missing silent participant")
	}
	if silent.Reason != ParticipantTerminationError || silent.Classification != SessionSilentProviderTimeoutClassification {
		t.Fatalf("silent participant result = %+v, want timeout error", silent)
	}
	viable, ok := got.result.Participants["viable"]
	if !ok {
		t.Fatal("room result is missing viable participant")
	}
	if viable.Classification == SessionSilentProviderTimeoutClassification {
		t.Fatalf("viable participant inherited silent timeout: %+v", viable)
	}
}

var _ SessionLivenessClock = (*livenessTestClock)(nil)
var _ SessionLivenessTimer = (*livenessTestTimer)(nil)
