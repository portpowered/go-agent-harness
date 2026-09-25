package publication

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	eventBuffer   = 8
	waitBound     = 5 * time.Second
	staticTool    = "static_tool"
	stableTool    = "stable_tool"
	pageToolName  = "page_tool"
	browserID     = "browser"
	tabID         = "tab"
	armSignalSize = 64
)

// fakeTimers is an explicitly driven timer factory. Every NewTimer and Reset
// is signaled on armed, so a test can wait until the publisher has armed the
// settle boundary for each consumed event before firing it.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
	armed  chan struct{}
}

func newFakeTimers() *fakeTimers { return &fakeTimers{armed: make(chan struct{}, armSignalSize)} }

func (f *fakeTimers) NewTimer(time.Duration) sessionturn.Timer {
	timer := &fakeTimer{c: make(chan time.Time, 1), active: true, parent: f}
	f.mu.Lock()
	f.timers = append(f.timers, timer)
	f.mu.Unlock()
	f.armed <- struct{}{}
	return timer
}

// waitArmed blocks until n more arm signals were observed.
func (f *fakeTimers) waitArmed(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-f.armed:
		case <-time.After(waitBound):
			t.Fatal("settle timer was not armed")
		}
	}
}

// fire expires the only settle timer.
func (f *fakeTimers) fire(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	timers := append([]*fakeTimer(nil), f.timers...)
	f.mu.Unlock()
	if len(timers) != 1 {
		t.Fatalf("settle timers = %d, want one reusable timer", len(timers))
	}
	if !timers[0].fire() {
		t.Fatal("settle timer was not active when fired")
	}
}

type fakeTimer struct {
	mu     sync.Mutex
	c      chan time.Time
	active bool
	parent *fakeTimers
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	was := t.active
	t.active = false
	return was
}

func (t *fakeTimer) Reset(time.Duration) bool {
	t.mu.Lock()
	was := t.active
	t.active = true
	t.mu.Unlock()
	t.parent.armed <- struct{}{}
	return was
}

func (t *fakeTimer) fire() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return false
	}
	t.active = false
	t.c <- time.Unix(0, 0)
	return true
}

func definition(name, description string) messages.ToolDefinition {
	return messages.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters: []messages.ToolParameter{{
			Name: "value", Type: "string", Description: "value for the test tool", Required: true,
		}},
		ParametersClosed: true,
	}
}

func concat(parts ...[]messages.ToolDefinition) []messages.ToolDefinition {
	var out []messages.ToolDefinition
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

// catalog is a mutable refresh source that signals every refresh.
type catalog struct {
	mu        sync.Mutex
	current   []messages.ToolDefinition
	calls     int
	refreshed chan int
}

func newCatalog(initial []messages.ToolDefinition) *catalog {
	return &catalog{current: initial, refreshed: make(chan int, armSignalSize)}
}

func (c *catalog) set(definitions []messages.ToolDefinition) {
	c.mu.Lock()
	c.current = append([]messages.ToolDefinition(nil), definitions...)
	c.mu.Unlock()
}

func (c *catalog) refresh(context.Context) ([]messages.ToolDefinition, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	current := append([]messages.ToolDefinition(nil), c.current...)
	c.mu.Unlock()
	c.refreshed <- call
	return current, nil
}

func (c *catalog) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func waitRefresh(t *testing.T, c *catalog, want int) {
	t.Helper()
	select {
	case got := <-c.refreshed:
		if got != want {
			t.Fatalf("refresh call = %d, want %d", got, want)
		}
	case <-time.After(waitBound):
		t.Fatalf("timed out waiting for refresh %d", want)
	}
}

// sink records provider definition replacements.
type sink struct {
	published chan []messages.ToolDefinition
}

func newSink() *sink { return &sink{published: make(chan []messages.ToolDefinition, armSignalSize)} }

func (s *sink) publish(_ context.Context, definitions []messages.ToolDefinition) error {
	s.published <- definitions
	return nil
}

func (s *sink) next(t *testing.T) []messages.ToolDefinition {
	t.Helper()
	select {
	case definitions := <-s.published:
		return definitions
	case <-time.After(waitBound):
		t.Fatal("timed out waiting for provider publication")
		return nil
	}
}

func eventWatch(events chan sessionturn.BrowserEvent) func(context.Context) <-chan sessionturn.BrowserEvent {
	return func(context.Context) <-chan sessionturn.BrowserEvent { return events }
}
