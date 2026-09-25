package wire

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type hostBrowserEvent struct {
	kind     string
	sequence uint64
}

func projectHostBrowserEvent(event hostBrowserEvent) rooms.BrowserEvent {
	return rooms.BrowserEvent{Type: event.kind, Sequence: event.sequence}
}

func TestBrowserEventStreamProjectsHostEventsInOrderAndCloses(t *testing.T) {
	input := make(chan hostBrowserEvent, 2)
	input <- hostBrowserEvent{kind: "tools_added", sequence: 1}
	input <- hostBrowserEvent{kind: "invocation", sequence: 2}
	close(input)
	output := BrowserEventStream(context.Background(), input, projectHostBrowserEvent)
	var got []rooms.BrowserEvent
	for event := range output {
		got = append(got, event)
	}
	if len(got) != 2 || got[0].Type != "tools_added" || got[1].Sequence != 2 {
		t.Fatalf("projected events = %+v, want both host events in order", got)
	}
}

// A room participant that stops reading must never block the host browser:
// events beyond the bounded queue are dropped instead of back-pressuring.
func TestBrowserEventStreamDropsInsteadOfBlockingTheHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan hostBrowserEvent)
	output := BrowserEventStream(ctx, input, projectHostBrowserEvent)
	const produced = 64
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for index := range produced {
			input <- hostBrowserEvent{kind: "tick", sequence: uint64(index)}
		}
		close(input)
	}()
	select {
	case <-sent:
	case <-time.After(contractWait):
		t.Fatal("host browser producer blocked on an unread room stream")
	}
	var delivered int
	for range output {
		delivered++
	}
	if delivered == 0 || delivered >= produced {
		t.Fatalf("delivered %d of %d events, want a bounded non-empty prefix", delivered, produced)
	}
}

func TestBrowserEventStreamStopsWithTheRoomContext(t *testing.T) {
	if BrowserEventStream[hostBrowserEvent](context.Background(), nil, projectHostBrowserEvent) != nil {
		t.Fatal("absent host stream produced a room stream")
	}
	ctx, cancel := context.WithCancel(context.Background())
	output := BrowserEventStream(ctx, make(chan hostBrowserEvent), projectHostBrowserEvent)
	cancel()
	select {
	case _, open := <-output:
		if open {
			t.Fatal("room stream delivered an event after cancellation")
		}
	case <-time.After(contractWait):
		t.Fatal("room stream stayed open after cancellation")
	}
}
