package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const eventQueueCapacity = 64

type eventDrain struct {
	queue chan session.LiveEvent
	stop  chan struct{}
	done  chan struct{}
	on    func(session.LiveEvent)
	mu    sync.Mutex
	full  bool
	close sync.Once
}

func newEventDrain(ctx context.Context, events <-chan session.LiveEvent, participantID string, diagnosticCallback func(string, rooms.RoomDiagnosticRecord), sink rooms.EventSink, onTurn func(), now func() time.Time, onTerminal func(session.LiveEvent), onError func(error)) *eventDrain {
	drain := &eventDrain{queue: make(chan session.LiveEvent, eventQueueCapacity), stop: make(chan struct{}), done: make(chan struct{})}
	drain.on = eventObserver(ctx, participantID, diagnosticCallback, sink, onTurn, now, onTerminal, onError)
	go drain.produce(events)
	go drain.consume()
	return drain
}

func eventObserver(ctx context.Context, participantID string, diagnosticCallback func(string, rooms.RoomDiagnosticRecord), sink rooms.EventSink, onTurn func(), now func() time.Time, onTerminal func(session.LiveEvent), onError func(error)) func(session.LiveEvent) {
	return func(event session.LiveEvent) {
		// Record the terminal cause before invoking the host sink. Sinks may
		// synchronously tear down a peer; that teardown can race this
		// participant's media-close notification. Latching the participant's
		// typed liveness cause first keeps transport cleanup from replacing it.
		if onTerminal != nil && isTerminalEvent(event) {
			onTerminal(event)
		}
		publishEvent(ctx, participantID, event, sink, onError)
		notifyTurnComplete(event, onTurn)
		if diagnosticCallback == nil {
			return
		}
		diagnosticCallback(participantID, liveDiagnostic(event, now))
	}
}

func publishEvent(ctx context.Context, participantID string, event session.LiveEvent, sink rooms.EventSink, onError func(error)) {
	if sink == nil {
		return
	}
	err := sink.Publish(ctx, participantID, event)
	if err != nil && onError != nil {
		onError(fmt.Errorf("publish live event for %q: %w", participantID, err))
	}
}

func notifyTurnComplete(event session.LiveEvent, onTurn func()) {
	if onTurn == nil {
		return
	}
	kind := normalizeEventKind(event.Kind)
	if strings.Contains(kind, "turn") && strings.Contains(kind, "complete") {
		onTurn()
	}
}

func liveDiagnostic(event session.LiveEvent, now func() time.Time) rooms.RoomDiagnosticRecord {
	kind := normalizeEventKind(event.Kind)
	return rooms.RoomDiagnosticRecord{Event: "live_" + kind, Fields: liveDiagnosticFields(event), At: eventTime(now)}
}

func liveDiagnosticFields(event session.LiveEvent) map[string]string {
	fields := map[string]string{"kind": event.Kind}
	addLiveEventFields(fields, event)
	addTerminalDiagnosticFields(fields, event.Terminal)
	addLivenessDiagnosticFields(fields, event.Liveness)
	return fields
}

func addLiveEventFields(fields map[string]string, event session.LiveEvent) {
	if event.SessionID != "" {
		fields["session_id"] = event.SessionID
	}
	if event.Text != "" {
		fields["text"] = event.Text
	}
	if event.Error != nil {
		fields["error"] = event.Error.Error()
	}
}

func addTerminalDiagnosticFields(fields map[string]string, terminal *messages.SessionCloseValue) {
	if terminal == nil {
		return
	}
	if terminal.Classification != "" {
		fields["classification"] = terminal.Classification
	}
	if terminal.TerminalReason != "" {
		fields["terminal_reason"] = string(terminal.TerminalReason)
	}
	if terminal.TerminalProvenance != "" {
		fields["terminal_provenance"] = string(terminal.TerminalProvenance)
	}
	if terminal.OutputState != "" {
		fields["output_state"] = string(terminal.OutputState)
	}
}

func addLivenessDiagnosticFields(fields map[string]string, liveness *session.LiveLivenessFailure) {
	if liveness == nil {
		return
	}
	if liveness.Classification != "" {
		fields["classification"] = liveness.Classification
	}
	if liveness.ResponseID != "" {
		fields["response_id"] = liveness.ResponseID
	}
	if liveness.TerminalReason != "" {
		fields["terminal_reason"] = string(liveness.TerminalReason)
	}
	if liveness.TerminalProvenance != "" {
		fields["terminal_provenance"] = string(liveness.TerminalProvenance)
	}
	if liveness.OutputState != "" {
		fields["output_state"] = string(liveness.OutputState)
	}
}

func eventTime(now func() time.Time) time.Time {
	if now == nil {
		return time.Time{}
	}
	return now()
}

func (d *eventDrain) produce(events <-chan session.LiveEvent) {
	defer close(d.queue)
	if events == nil {
		return
	}
	for {
		select {
		case <-d.stop:
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if !d.enqueue(event) {
				return
			}
		}
	}
}

func (d *eventDrain) enqueue(event session.LiveEvent) bool {
	if isTerminalEvent(event) {
		select {
		case d.queue <- event:
			return true
		case <-d.stop:
			return false
		}
	}
	select {
	case d.queue <- event:
	default:
		d.mu.Lock()
		d.full = true
		d.mu.Unlock()
	}
	return true
}

func (d *eventDrain) consume() {
	defer close(d.done)
	for event := range d.queue {
		d.on(event)
	}
}

func (d *eventDrain) Stop() {
	if d == nil {
		return
	}
	d.close.Do(func() { close(d.stop) })
}

func (d *eventDrain) Wait() error {
	if d == nil {
		return nil
	}
	<-d.done
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.full {
		return errors.New("room diagnostic event queue overflow")
	}
	return nil
}

func normalizeEventKind(kind string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(kind), ".", "_"), "-", "_"))
}

func isTerminalEvent(event session.LiveEvent) bool {
	if event.Liveness != nil {
		return true
	}
	kind := normalizeEventKind(event.Kind)
	return strings.Contains(kind, "terminal") || strings.Contains(kind, "close") || strings.Contains(kind, "error") || strings.Contains(kind, "failed") || strings.Contains(kind, "done")
}
