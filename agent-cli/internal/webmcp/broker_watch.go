package webmcp

import "context"

// brokerWatcher reserves one extra channel slot for its terminal failure
// event. The ordinary capacity remains bounded by capacity; the reserved slot
// means overload is observable even when a caller stops reading.
type brokerWatcher struct {
	events   chan BrokerEvent
	capacity int
}

// browserEventWatcher carries the adapter-owned semantic stream to an
// independent observer such as the session recording coordinator. It uses the
// same bounded-buffer policy as Broker.Watch, but preserves the richer event
// payload rather than converting it into broker lifecycle state.
type browserEventWatcher struct {
	events   chan BrowserEvent
	capacity int
}

func (b *StatefulBroker) flushSelected() {
	b.mu.Lock()
	selected := b.selected
	b.mu.Unlock()
	if selected != nil {
		b.flushSession(selected)
	}
}

func (b *StatefulBroker) flushSession(selected *brokerSession) {
	if selected == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case selected.flush <- ack:
		<-ack
	case <-selected.loopDone:
	}
}

func (b *StatefulBroker) runSession(selected *brokerSession) {
	defer b.wg.Done()
	defer close(selected.loopDone)
	events := selected.session.Events()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				b.drainSessionEvents(selected, events)
				b.markSessionEnded(selected, "events_closed")
				return
			}
			b.applyBrowserEvent(selected, event)
		case <-selected.session.Done():
			b.drainSessionEvents(selected, events)
			b.markSessionEnded(selected, "session_done")
			return
		case ack := <-selected.flush:
			b.drainSessionEvents(selected, events)
			close(ack)
		}
	}
}

func (b *StatefulBroker) drainSessionEvents(selected *brokerSession, events <-chan BrowserEvent) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			b.applyBrowserEvent(selected, event)
		default:
			return
		}
	}
}

// Watch subscribes to bounded broker lifecycle observations. If a caller does
// not consume quickly enough, the watcher receives a terminal session_closed
// event with BrokerWatchBufferFullReason and then closes; events are never
// silently discarded.
func (b *StatefulBroker) Watch(ctx context.Context) <-chan BrokerEvent {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan BrokerEvent, defaultBrokerWatchBuffer+1)
	if b == nil {
		close(out)
		return out
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(out)
		return out
	}
	out = make(chan BrokerEvent, b.watchBuffer+1)
	watcher := &brokerWatcher{events: out, capacity: b.watchBuffer}
	b.watchers[watcher] = struct{}{}
	closedCh := b.closedCh
	b.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			b.removeWatcher(watcher)
		case <-closedCh:
			b.removeWatcher(watcher)
		}
	}()
	return out
}

// WatchBrowserEvents subscribes to the semantic event stream consumed by the
// broker's target-session loop. The broker remains the sole consumer of the
// target session itself; this method only fans out copies to observers.
func (b *StatefulBroker) WatchBrowserEvents(ctx context.Context) <-chan BrowserEvent {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan BrowserEvent, defaultBrokerWatchBuffer+1)
	if b == nil {
		close(out)
		return out
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(out)
		return out
	}
	if b.browserWatchers == nil {
		b.browserWatchers = make(map[*browserEventWatcher]struct{})
	}
	out = make(chan BrowserEvent, b.watchBuffer+1)
	watcher := &browserEventWatcher{events: out, capacity: b.watchBuffer}
	b.browserWatchers[watcher] = struct{}{}
	closedCh := b.closedCh
	b.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			b.removeBrowserEventWatcher(watcher)
		case <-closedCh:
			b.removeBrowserEventWatcher(watcher)
		}
	}()
	return out
}

func (b *StatefulBroker) emitLocked(event BrokerEvent) {
	if b.closed {
		return
	}
	event = b.nextEventLocked(event)
	for watcher := range b.watchers {
		b.deliverWatcherEventLocked(watcher, event)
	}
}

func (b *StatefulBroker) emitBrowserEventLocked(event BrowserEvent) {
	if b == nil || b.closed {
		return
	}
	for watcher := range b.browserWatchers {
		if watcher == nil {
			continue
		}
		if len(watcher.events) < watcher.capacity {
			select {
			case watcher.events <- cloneBrowserEvent(event):
				continue
			default:
			}
		}
		// Reserve one slot for a terminal event so an observer can distinguish
		// an overloaded recording stream from a cleanly closed one.
		failure := BrowserEvent{
			Type:       EventSessionClosed,
			At:         b.clock.Now(),
			BrowserID:  event.BrowserID,
			TargetID:   event.TargetID,
			Generation: event.Generation,
			Reason:     BrowserEventBufferFullReason,
		}
		select {
		case watcher.events <- failure:
		default:
			select {
			case <-watcher.events:
			default:
			}
			select {
			case watcher.events <- failure:
			default:
			}
		}
		delete(b.browserWatchers, watcher)
		close(watcher.events)
	}
}

func cloneBrowserEvent(event BrowserEvent) BrowserEvent {
	event.Input = append([]byte(nil), event.Input...)
	event.Output = append([]byte(nil), event.Output...)
	event.RemovedToolNames = append([]string(nil), event.RemovedToolNames...)
	if len(event.Tools) > 0 {
		tools := event.Tools
		event.Tools = make([]ToolDescriptor, len(tools))
		for index, tool := range tools {
			event.Tools[index] = cloneToolDescriptor(tool)
		}
	}
	return event
}

func (b *StatefulBroker) nextEventLocked(event BrokerEvent) BrokerEvent {
	b.eventSequence++
	event.Version = BrowserEventsVersion
	event.Sequence = b.eventSequence
	if event.At.IsZero() {
		event.At = b.clock.Now()
	}
	return event
}

func (b *StatefulBroker) deliverWatcherEventLocked(watcher *brokerWatcher, event BrokerEvent) {
	if watcher == nil {
		return
	}
	// Keep one channel slot available for the bounded failure outcome.
	if len(watcher.events) < watcher.capacity {
		select {
		case watcher.events <- event:
			return
		default:
		}
	}

	failure := b.nextEventLocked(BrokerEvent{
		Type:       BrokerEventSessionClosed,
		At:         b.clock.Now(),
		BrowserID:  event.BrowserID,
		TargetID:   event.TargetID,
		Generation: event.Generation,
		Reason:     BrokerWatchBufferFullReason,
	})
	// The watcher channel has a reserved slot. The fallback only protects the
	// invariant if a future change bypasses the ordinary-capacity guard.
	select {
	case watcher.events <- failure:
	default:
		select {
		case <-watcher.events:
		default:
		}
		select {
		case watcher.events <- failure:
		default:
		}
	}
	delete(b.watchers, watcher)
	close(watcher.events)
}

func (b *StatefulBroker) removeWatcher(watcher *brokerWatcher) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.watchers[watcher]; !ok {
		return
	}
	delete(b.watchers, watcher)
	close(watcher.events)
}

func (b *StatefulBroker) removeBrowserEventWatcher(watcher *browserEventWatcher) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.browserWatchers[watcher]; !ok {
		return
	}
	delete(b.browserWatchers, watcher)
	close(watcher.events)
}
