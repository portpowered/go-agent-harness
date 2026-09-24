package webmcp

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

func invokeBrokerCloseWithTimeout(timeout time.Duration, phase string, closeFn func() error) error {
	if closeFn == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultBrokerCloseTimeout
	}
	done := make(chan error, 1)
	go func() {
		done <- invokeBrokerClose(closeFn)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%s: %w after %s", phase, ErrCloseTimeout, timeout)
	}
}

func invokeBrokerClose(closeFn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", ErrClosePanic, recovered)
		}
	}()
	return closeFn()
}

func waitForBrokerWorkersWithTimeout(timeout time.Duration, workers *sync.WaitGroup) error {
	if workers == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultBrokerCloseTimeout
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("broker workers: %w after %s", ErrCloseTimeout, timeout)
	}
}

// Close retires every session-local reference and closes the broker-owned
// handles. It is idempotent; a later call returns the first aggregate error.
func (b *StatefulBroker) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		done := b.closeDone
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		err := b.closeErr
		b.mu.Unlock()
		return err
	}
	b.closed = true
	close(b.closedCh)
	selected := b.selected
	if selected != nil {
		b.retireSessionLocked(selected, "broker_close")
	}
	handles := make([]BrowserHandle, 0, len(b.browsers))
	seenHandles := make(map[BrowserHandle]struct{}, len(b.browsers))
	for _, state := range b.browsers {
		if state.handle == nil {
			continue
		}
		if _, seen := seenHandles[state.handle]; seen {
			continue
		}
		seenHandles[state.handle] = struct{}{}
		handles = append(handles, state.handle)
	}
	for watcher := range b.watchers {
		delete(b.watchers, watcher)
		close(watcher.events)
	}
	for watcher := range b.browserWatchers {
		delete(b.browserWatchers, watcher)
		close(watcher.events)
	}
	b.mu.Unlock()

	var joined error
	if selected != nil {
		joined = errors.Join(joined, invokeBrokerCloseWithTimeout(b.closeTimeout, "target session", selected.session.Close))
	}
	for _, handle := range handles {
		joined = errors.Join(joined, invokeBrokerCloseWithTimeout(b.closeTimeout, "browser handle", handle.Close))
	}
	joined = errors.Join(joined, waitForBrokerWorkersWithTimeout(b.closeTimeout, &b.wg))
	b.mu.Lock()
	b.closeErr = joined
	close(b.closeDone)
	b.mu.Unlock()
	return joined
}

// discardCloseError closes a resource on an abandon or cleanup path whose
// result is already decided. The resource is being dropped either way, and a
// close failure cannot change the selection outcome reported to the caller.
func discardCloseError(closeFn func() error) {
	if err := closeFn(); err != nil {
		return
	}
}
