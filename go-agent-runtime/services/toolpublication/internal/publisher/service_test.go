package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication/internal/publisher"
)

type publicationSink struct {
	mu       sync.Mutex
	updates  [][]messages.ToolDefinition
	updateCh chan []messages.ToolDefinition
	err      error
}

func newPublicationSink() *publicationSink {
	return &publicationSink{updateCh: make(chan []messages.ToolDefinition, 8)}
}

func (s *publicationSink) SendSessionUpdate(ctx context.Context, definitions []messages.ToolDefinition) error {
	if s.err != nil {
		return s.err
	}
	copyDefinitions := messages.CanonicalToolDefinitions(definitions)
	s.mu.Lock()
	s.updates = append(s.updates, copyDefinitions)
	s.mu.Unlock()
	select {
	case s.updateCh <- copyDefinitions:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *publicationSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.updates)
}

type manualTimerFactory struct {
	created chan *manualTimer
}

type manualTimer struct {
	ch      chan time.Time
	resetCh chan struct{}
	mu      sync.Mutex
	open    bool
}

func newManualTimerFactory() *manualTimerFactory {
	return &manualTimerFactory{created: make(chan *manualTimer, 8)}
}

func (f *manualTimerFactory) NewTimer(time.Duration) toolpublication.Timer {
	timer := &manualTimer{ch: make(chan time.Time, 1), resetCh: make(chan struct{}, 8), open: true}
	f.created <- timer
	return timer
}

func (t *manualTimer) C() <-chan time.Time { return t.ch }

func (t *manualTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasOpen := t.open
	t.open = false
	return wasOpen
}

func (t *manualTimer) Reset(time.Duration) bool {
	t.mu.Lock()
	wasOpen := t.open
	t.open = true
	t.mu.Unlock()
	t.resetCh <- struct{}{}
	return wasOpen
}

func (t *manualTimer) fire() {
	t.mu.Lock()
	t.open = false
	t.mu.Unlock()
	t.ch <- time.Unix(0, 0)
}

func publicationDefinition(name, description string) messages.ToolDefinition {
	return messages.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters: []messages.ToolParameter{{
			Name:        "value",
			Type:        "string",
			Description: "value",
			Required:    true,
		}},
		ParametersClosed: true,
	}
}

func mergedDefinitions(base, dynamic []messages.ToolDefinition) []messages.ToolDefinition {
	base = messages.CanonicalToolDefinitions(base)
	dynamic = messages.CanonicalToolDefinitions(dynamic)
	merged := append([]messages.ToolDefinition(nil), base...)
	baseNames := make(map[string]struct{}, len(base))
	for _, definition := range base {
		baseNames[definition.Name] = struct{}{}
	}
	for _, definition := range dynamic {
		if _, exists := baseNames[definition.Name]; !exists {
			merged = append(merged, definition)
		}
	}
	return messages.CanonicalToolDefinitions(merged)
}

func newPublicationService() toolpublication.Service {
	return publisher.NewService(nil)
}

func waitRefresh(t *testing.T, ctx context.Context, calls <-chan int, want int) {
	t.Helper()
	select {
	case got := <-calls:
		if got != want {
			t.Fatalf("refresh call = %d, want %d", got, want)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for refresh %d: %v", want, ctx.Err())
	}
}

func waitUpdate(t *testing.T, ctx context.Context, sink *publicationSink) []messages.ToolDefinition {
	t.Helper()
	select {
	case update := <-sink.updateCh:
		return update
	case <-ctx.Done():
		t.Fatalf("timed out waiting for session update: %v", ctx.Err())
		return nil
	}
}

func waitTimerResets(t *testing.T, ctx context.Context, timer *manualTimer, count int) {
	t.Helper()
	for range count {
		select {
		case <-timer.resetCh:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for timer reset: %v", ctx.Err())
		}
	}
}

func stopPublisher(t *testing.T, publisher toolpublication.Publisher) {
	t.Helper()
	publisher.Stop()
	publisher.Stop()
}

func TestPublisherReadinessCoalescesAndRejectsStaleGenerations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	base := []messages.ToolDefinition{publicationDefinition("stable", "stable")}
	pageA := []messages.ToolDefinition{publicationDefinition("page_a", "A")}
	pageB := []messages.ToolDefinition{publicationDefinition("page_b", "B")}
	pageC := []messages.ToolDefinition{publicationDefinition("page_c", "C")}
	current := append([]messages.ToolDefinition(nil), pageA...)
	var currentMu sync.Mutex
	refreshCalls := 0
	refreshCall := make(chan int, 8)
	refresh := func(refreshContext context.Context) ([]messages.ToolDefinition, error) {
		currentMu.Lock()
		refreshCalls++
		call := refreshCalls
		definitions := append([]messages.ToolDefinition(nil), current...)
		currentMu.Unlock()
		select {
		case refreshCall <- call:
		case <-refreshContext.Done():
			return nil, refreshContext.Err()
		}
		return definitions, nil
	}
	events := make(chan toolpublication.Event)
	timers := newManualTimerFactory()
	sink := newPublicationSink()
	publisher, err := newPublicationService().NewPublisher(toolpublication.Options{
		StaticStableDefinitions: base,
		InitialDefinitions:      append(append([]messages.ToolDefinition(nil), base...), pageA...),
		Watch:                   func(context.Context) <-chan toolpublication.Event { return events },
		Refresh:                 refresh,
		TimerFactory:            timers,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	publisher.Start(ctx, sink)

	// A selection observed during the provider handshake is consumed but cannot
	// publish before Ready releases the initial-update barrier.
	events <- toolpublication.Event{Type: toolpublication.EventSelected, Sequence: 1}
	select {
	case <-sink.updateCh:
		t.Fatal("publication overtook the readiness barrier")
	default:
	}
	publisher.Ready()
	waitRefresh(t, ctx, refreshCall, 1)
	if sink.count() != 0 {
		t.Fatalf("initial no-op update count = %d, want zero", sink.count())
	}

	currentMu.Lock()
	current = append([]messages.ToolDefinition(nil), pageB...)
	currentMu.Unlock()
	events <- toolpublication.Event{Type: toolpublication.EventSelected, BrowserID: "browser", TargetID: "tab", Generation: 4, Sequence: 10}
	events <- toolpublication.Event{Type: toolpublication.EventGenerationChanged, BrowserID: "browser", TargetID: "tab", Generation: 4, Sequence: 11}
	events <- toolpublication.Event{Type: toolpublication.EventCatalogChanged, BrowserID: "browser", TargetID: "tab", Generation: 4, Sequence: 12}
	timer := <-timers.created
	waitTimerResets(t, ctx, timer, 2)
	timer.fire()
	waitRefresh(t, ctx, refreshCall, 2)
	if got, want := waitUpdate(t, ctx, sink), mergedDefinitions(base, pageB); !reflect.DeepEqual(got, want) {
		t.Fatalf("coalesced surface = %#v, want %#v", got, want)
	}
	state := publisher.Snapshot()
	if state.LastSuccessfulGeneration != 4 || state.LastSuccessfulEventSequence != 12 {
		t.Fatalf("successful event state = %#v", state)
	}

	// A stale generation is observed for sequence accounting but cannot replace
	// the successful surface or schedule another refresh.
	events <- toolpublication.Event{Type: toolpublication.EventCatalogChanged, BrowserID: "browser", TargetID: "tab", Generation: 3, Sequence: 13}
	select {
	case <-timers.created:
		t.Fatal("stale generation created a settle timer")
	case <-time.After(20 * time.Millisecond):
	}
	state = publisher.Snapshot()
	if state.LatestEventSequence != 13 || state.LastSuccessfulGeneration != 4 {
		t.Fatalf("stale generation changed state = %#v", state)
	}

	currentMu.Lock()
	current = append([]messages.ToolDefinition(nil), pageC...)
	currentMu.Unlock()
	events <- toolpublication.Event{Type: toolpublication.EventGenerationChanged, BrowserID: "browser", TargetID: "tab", Generation: 5, Sequence: 14}
	events <- toolpublication.Event{Type: toolpublication.EventCatalogChanged, BrowserID: "browser", TargetID: "tab", Generation: 4, Sequence: 15}
	waitTimerResets(t, ctx, timer, 1)
	timer.fire()
	waitRefresh(t, ctx, refreshCall, 3)
	if got, want := waitUpdate(t, ctx, sink), mergedDefinitions(base, pageC); !reflect.DeepEqual(got, want) {
		t.Fatalf("new generation surface = %#v, want %#v", got, want)
	}
	state = publisher.Snapshot()
	if state.LastSuccessfulGeneration != 5 || state.LastSuccessfulEventSequence != 14 || state.LatestEventSequence != 15 {
		t.Fatalf("generation precedence state = %#v", state)
	}
	stopPublisher(t, publisher)
}

func TestPublisherDigestNoOpAndSchemaFailureIdentity(t *testing.T) {
	base := []messages.ToolDefinition{publicationDefinition("stable", "stable")}
	first := publicationDefinition("page", "first")
	first.ParameterSchema = json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`)
	second := first
	second.ParameterSchema = json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}}}`)
	service := newPublicationService()
	makePublisher := func(initial messages.ToolDefinition) toolpublication.State {
		publisher, err := service.NewPublisher(toolpublication.Options{
			StaticStableDefinitions: base,
			InitialDefinitions:      []messages.ToolDefinition{initial},
			Watch:                   func(context.Context) <-chan toolpublication.Event { return make(chan toolpublication.Event) },
			Refresh:                 func(context.Context) ([]messages.ToolDefinition, error) { return nil, nil },
			TimerFactory:            newManualTimerFactory(),
		})
		if err != nil {
			t.Fatalf("NewPublisher: %v", err)
		}
		return publisher.Snapshot()
	}
	firstState := makePublisher(first)
	secondState := makePublisher(second)
	if firstState.LastSuccessfulDigest == secondState.LastSuccessfulDigest {
		t.Fatal("complete parameter schema change did not change canonical digest")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	page := []messages.ToolDefinition{publicationDefinition("page", "page")}
	refreshErr := errors.New("catalog transport unavailable")
	events := make(chan toolpublication.Event, 2)
	timers := newManualTimerFactory()
	refreshCalls := 0
	initialRefreshDone := make(chan struct{})
	var initialRefreshOnce sync.Once
	publisher, err := service.NewPublisher(toolpublication.Options{
		StaticStableDefinitions: base,
		InitialDefinitions:      append(append([]messages.ToolDefinition(nil), base...), page...),
		Watch:                   func(context.Context) <-chan toolpublication.Event { return events },
		Refresh: func(context.Context) ([]messages.ToolDefinition, error) {
			refreshCalls++
			if refreshCalls == 1 {
				initialRefreshOnce.Do(func() { close(initialRefreshDone) })
				return page, nil
			}
			return nil, refreshErr
		},
		TimerFactory: timers,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	publisher.Start(ctx, newPublicationSink())
	publisher.Ready()
	select {
	case <-initialRefreshDone:
	case <-ctx.Done():
		t.Fatalf("initial refresh did not complete: %v", ctx.Err())
	}
	events <- toolpublication.Event{Type: toolpublication.EventCatalogChanged, BrowserID: "browser", TargetID: "tab", Generation: 2, Sequence: 7}
	timer := <-timers.created
	timer.fire()
	publicationErr := <-publisher.Errors()
	if !errors.Is(publicationErr, toolpublication.ErrSessionDynamicToolPublication) || !errors.Is(publicationErr, refreshErr) {
		t.Fatalf("refresh error = %v, want publication and catalog causes", publicationErr)
	}
	state := publisher.Snapshot()
	if !reflect.DeepEqual(state.LastSuccessfulDefinitions, mergedDefinitions(base, page)) || state.LastSuccessfulGeneration != 0 {
		t.Fatalf("failed refresh advanced last success = %#v", state)
	}
	if state.Lifecycle != toolpublication.LifecycleFailed || len(publicationErr.Error()) > 420 {
		t.Fatalf("failure state = %#v, error length=%d", state, len(publicationErr.Error()))
	}
	stopPublisher(t, publisher)
}

func TestPublisherSinkFailureRetainsSurfaceAndNilPortsFailClosed(t *testing.T) {
	service := newPublicationService()
	if _, err := service.NewPublisher(toolpublication.Options{Refresh: func(context.Context) ([]messages.ToolDefinition, error) { return nil, nil }}); !errors.Is(err, toolpublication.ErrInvalidOptions) {
		t.Fatalf("nil watch error = %v, want invalid options", err)
	}
	if _, err := service.NewPublisher(toolpublication.Options{Watch: func(context.Context) <-chan toolpublication.Event { return make(chan toolpublication.Event) }}); !errors.Is(err, toolpublication.ErrInvalidOptions) {
		t.Fatalf("nil refresh error = %v, want invalid options", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	base := []messages.ToolDefinition{publicationDefinition("stable", "stable")}
	pageA := []messages.ToolDefinition{publicationDefinition("page_a", "A")}
	pageB := []messages.ToolDefinition{publicationDefinition("page_b", "B")}
	events := make(chan toolpublication.Event, 2)
	timers := newManualTimerFactory()
	refreshCalls := 0
	publisher, err := service.NewPublisher(toolpublication.Options{
		StaticStableDefinitions: base,
		InitialDefinitions:      append(append([]messages.ToolDefinition(nil), base...), pageA...),
		Watch:                   func(context.Context) <-chan toolpublication.Event { return events },
		Refresh: func(context.Context) ([]messages.ToolDefinition, error) {
			refreshCalls++
			if refreshCalls == 1 {
				return pageA, nil
			}
			return pageB, nil
		},
		TimerFactory: timers,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	sinkErr := errors.New("provider send failed")
	sink := newPublicationSink()
	sink.err = sinkErr
	publisher.Start(ctx, sink)
	publisher.Ready()
	select {
	case <-time.After(50 * time.Millisecond):
	case <-publisher.Errors():
		t.Fatal("initial unchanged refresh failed unexpectedly")
	}
	events <- toolpublication.Event{Type: toolpublication.EventSelected, Sequence: 1}
	timer := <-timers.created
	timer.fire()
	publicationErr := <-publisher.Errors()
	if !errors.Is(publicationErr, sinkErr) || !errors.Is(publicationErr, toolpublication.ErrSessionDynamicToolPublication) {
		t.Fatalf("sink error = %v, want exact sink identity", publicationErr)
	}
	state := publisher.Snapshot()
	if !reflect.DeepEqual(state.LastSuccessfulDefinitions, mergedDefinitions(base, pageA)) {
		t.Fatalf("sink failure replaced last successful surface = %#v", state.LastSuccessfulDefinitions)
	}
	if reflect.DeepEqual(state.LastSuccessfulDefinitions, mergedDefinitions(base, pageB)) {
		t.Fatal("sink failure committed attempted surface")
	}
	stopPublisher(t, publisher)

	// A nil sink is a bounded failure, not a panic or a blocked controller.
	nilSinkPublisher, err := service.NewPublisher(toolpublication.Options{
		StaticStableDefinitions: base,
		InitialDefinitions:      base,
		Watch:                   func(context.Context) <-chan toolpublication.Event { return make(chan toolpublication.Event) },
		Refresh:                 func(context.Context) ([]messages.ToolDefinition, error) { return pageB, nil },
		TimerFactory:            newManualTimerFactory(),
	})
	if err != nil {
		t.Fatalf("nil sink NewPublisher: %v", err)
	}
	nilSinkPublisher.Start(ctx, nil)
	nilSinkPublisher.Ready()
	nilSinkErr := <-nilSinkPublisher.Errors()
	if !errors.Is(nilSinkErr, toolpublication.ErrSessionDynamicToolPublication) {
		t.Fatalf("nil sink error = %v", nilSinkErr)
	}
	stopPublisher(t, nilSinkPublisher)
}

func TestPublisherWatchCloseCancellationAndStopAreDeterministic(t *testing.T) {
	service := newPublicationService()
	watch := make(chan toolpublication.Event)
	publisher, err := service.NewPublisher(toolpublication.Options{
		Watch:        func(context.Context) <-chan toolpublication.Event { return watch },
		Refresh:      func(context.Context) ([]messages.ToolDefinition, error) { return nil, nil },
		TimerFactory: newManualTimerFactory(),
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	publisher.Start(ctx, toolpublication.SessionUpdateSinkFunc(func(context.Context, []messages.ToolDefinition) error { return nil }))
	close(watch)
	select {
	case <-publisher.Done():
	case <-time.After(time.Second):
		t.Fatalf("watch close did not terminate: %#v", publisher.Snapshot())
	}
	if publisher.Snapshot().Lifecycle != toolpublication.LifecycleWatchGone {
		t.Fatalf("watch close lifecycle = %#v", publisher.Snapshot())
	}
	stopPublisher(t, publisher)

	secondWatch := make(chan toolpublication.Event)
	second, err := service.NewPublisher(toolpublication.Options{
		Watch:        func(context.Context) <-chan toolpublication.Event { return secondWatch },
		Refresh:      func(context.Context) ([]messages.ToolDefinition, error) { return nil, nil },
		TimerFactory: newManualTimerFactory(),
	})
	if err != nil {
		t.Fatalf("second NewPublisher: %v", err)
	}
	second.Start(ctx, nil)
	cancel()
	stopPublisher(t, second)
	if second.Snapshot().Lifecycle != toolpublication.LifecycleStopped {
		t.Fatalf("cancellation lifecycle = %#v, want stopped", second.Snapshot().Lifecycle)
	}
}
