// Command consumer proves that the public toolpublication contract embeds from
// a separate module without importing agent-cli or private runtime packages.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
	toolpublicationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication/wire"
)

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
	return &manualTimerFactory{created: make(chan *manualTimer, 4)}
}

func (f *manualTimerFactory) NewTimer(time.Duration) toolpublication.Timer {
	timer := &manualTimer{
		ch:      make(chan time.Time, 1),
		resetCh: make(chan struct{}, 4),
		open:    true,
	}
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

type publicationSink struct {
	mu      sync.Mutex
	updates [][]messages.ToolDefinition
	update  chan []messages.ToolDefinition
	returnE error
}

func newPublicationSink() *publicationSink {
	return &publicationSink{update: make(chan []messages.ToolDefinition, 4)}
}

func (s *publicationSink) SendSessionUpdate(ctx context.Context, definitions []messages.ToolDefinition) error {
	if s.returnE != nil {
		return s.returnE
	}
	copyDefinitions := messages.CanonicalToolDefinitions(definitions)
	s.mu.Lock()
	s.updates = append(s.updates, copyDefinitions)
	s.mu.Unlock()
	select {
	case s.update <- copyDefinitions:
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

func definition(name, description string) messages.ToolDefinition {
	return messages.ToolDefinition{
		Name:             name,
		Description:      description,
		Parameters:       []messages.ToolParameter{{Name: "value", Type: "string", Required: true}},
		ParametersClosed: true,
	}
}

func merge(base, dynamic []messages.ToolDefinition) []messages.ToolDefinition {
	base = messages.CanonicalToolDefinitions(base)
	dynamic = messages.CanonicalToolDefinitions(dynamic)
	merged := append([]messages.ToolDefinition(nil), base...)
	baseNames := make(map[string]struct{}, len(base))
	for _, item := range base {
		baseNames[item.Name] = struct{}{}
	}
	for _, item := range dynamic {
		if _, exists := baseNames[item.Name]; !exists {
			merged = append(merged, item)
		}
	}
	return messages.CanonicalToolDefinitions(merged)
}

func waitRefresh(ctx context.Context, calls <-chan int, want int) error {
	select {
	case got := <-calls:
		if got != want {
			return fmt.Errorf("refresh call=%d want=%d", got, want)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitUpdate(ctx context.Context, sink *publicationSink) ([]messages.ToolDefinition, error) {
	select {
	case update := <-sink.update:
		return update, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func waitReset(ctx context.Context, timer *manualTimer) error {
	select {
	case <-timer.resetCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	base := []messages.ToolDefinition{definition("stable_tool", "stable")}
	pageA := []messages.ToolDefinition{definition("page_tool", "page A")}
	pageB := []messages.ToolDefinition{definition("page_tool_b", "page B")}
	current := append([]messages.ToolDefinition(nil), pageA...)
	var currentMu sync.Mutex
	refreshCalls := 0
	refreshCall := make(chan int, 4)
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
	service := toolpublicationwire.NewService(toolpublicationwire.Dependencies{})
	publisher, err := service.NewPublisher(toolpublication.Options{
		StaticStableDefinitions: base,
		InitialDefinitions:      append(append([]messages.ToolDefinition(nil), base...), pageA...),
		Watch:                   func(context.Context) <-chan toolpublication.Event { return events },
		Refresh:                 refresh,
		TimerFactory:            timers,
	})
	if err != nil {
		return fmt.Errorf("construct publisher: %w", err)
	}
	publisher.Start(ctx, sink)
	events <- toolpublication.Event{Type: toolpublication.EventSelected, Sequence: 1}
	publisher.Ready()
	if err := waitRefresh(ctx, refreshCall, 1); err != nil {
		return fmt.Errorf("initial refresh: %w", err)
	}
	if sink.count() != 0 {
		return fmt.Errorf("initial no-op emitted %d updates", sink.count())
	}

	currentMu.Lock()
	current = append([]messages.ToolDefinition(nil), pageB...)
	currentMu.Unlock()
	events <- toolpublication.Event{Type: toolpublication.EventCatalogChanged, BrowserID: "browser", TargetID: "tab", Generation: 2, Sequence: 2}
	timer := <-timers.created
	timer.fire()
	if err := waitRefresh(ctx, refreshCall, 2); err != nil {
		return fmt.Errorf("changed refresh: %w", err)
	}
	update, err := waitUpdate(ctx, sink)
	if err != nil {
		return fmt.Errorf("changed update: %w", err)
	}
	if want := merge(base, pageB); !reflect.DeepEqual(update, want) {
		return fmt.Errorf("changed surface=%#v want=%#v", update, want)
	}
	state := publisher.Snapshot()
	if state.PublicationCount != 1 || state.LastSuccessfulGeneration != 2 || state.LastSuccessfulEventSequence != 2 {
		return fmt.Errorf("changed state=%#v", state)
	}

	refreshFailure := errors.New("credential-free refresh failure")
	return runFailureProof(ctx, refreshFailure)
}

func runFailureProof(ctx context.Context, refreshFailure error) error {
	base := []messages.ToolDefinition{definition("stable_tool", "stable")}
	pageA := []messages.ToolDefinition{definition("page_tool", "page A")}
	failureEvents := make(chan toolpublication.Event)
	failureTimers := newManualTimerFactory()
	failureCalls := make(chan int, 4)
	refreshCalls := 0
	failurePublisher, err := toolpublicationwire.NewService(toolpublicationwire.Dependencies{}).NewPublisher(toolpublication.Options{
		StaticStableDefinitions: base,
		InitialDefinitions:      append(append([]messages.ToolDefinition(nil), base...), pageA...),
		Watch:                   func(context.Context) <-chan toolpublication.Event { return failureEvents },
		Refresh: func(context.Context) ([]messages.ToolDefinition, error) {
			refreshCalls++
			failureCalls <- refreshCalls
			if refreshCalls == 1 {
				return pageA, nil
			}
			return nil, refreshFailure
		},
		TimerFactory: failureTimers,
	})
	if err != nil {
		return fmt.Errorf("failure publisher: %w", err)
	}
	failurePublisher.Start(ctx, nil)
	failurePublisher.Ready()
	if err := waitRefresh(ctx, failureCalls, 1); err != nil {
		return fmt.Errorf("failure initial refresh: %w", err)
	}
	failureEvents <- toolpublication.Event{Type: toolpublication.EventSelected, Sequence: 9}
	failureTimer := <-failureTimers.created
	failureTimer.fire()
	select {
	case got := <-failurePublisher.Errors():
		var typed *toolpublication.PublicationError
		if !errors.As(got, &typed) || !errors.Is(got, toolpublication.ErrSessionDynamicToolPublication) || !errors.Is(got, refreshFailure) {
			return fmt.Errorf("failure identity=%v", got)
		}
		if typed.Phase == "" || len(got.Error()) > 420 {
			return fmt.Errorf("failure bounds=%q", got)
		}
		state := failurePublisher.Snapshot()
		if !reflect.DeepEqual(state.LastSuccessfulDefinitions, merge(base, pageA)) || state.Lifecycle != toolpublication.LifecycleFailed {
			return fmt.Errorf("failure state=%#v", state)
		}
	case <-ctx.Done():
		return fmt.Errorf("failure proof: %w", ctx.Err())
	}
	failurePublisher.Stop()
	failurePublisher.Stop()
	return nil
}

func main() {
	result := map[string]any{"status": "accepted", "public_only": true, "credential_free": true}
	if err := run(); err != nil {
		result["status"] = "rejected"
		result["error"] = err.Error()
		encoded, _ := json.Marshal(result)
		fmt.Println(string(encoded))
		os.Exit(1)
	}
	encoded, _ := json.Marshal(result)
	fmt.Println(string(encoded))
}
