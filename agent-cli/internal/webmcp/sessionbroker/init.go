package sessionbroker

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// doneLocked returns the bootstrap completion channel, creating it for a
// zero-value broker. The caller holds initMu.
func (b *Broker) doneLocked() chan struct{} {
	if b.initDone == nil {
		b.initDone = make(chan struct{})
		b.initState = StateInitializing
	}
	return b.initDone
}

// ensureInitialized starts the bootstrap on first use and waits for it, or
// for the caller's cancellation, whichever comes first.
func (b *Broker) ensureInitialized(ctx context.Context) error {
	if b == nil || b.Broker == nil {
		return webmcp.ErrClosed
	}
	b.initMu.Lock()
	done := b.doneLocked()
	b.initMu.Unlock()

	b.initOnce.Do(func() { b.startInitialization(ctx, done) })

	select {
	case <-done:
		return b.initializationError()
	default:
	}
	select {
	case <-done:
		return b.initializationError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Broker) startInitialization(ctx context.Context, done chan struct{}) {
	b.initMu.Lock()
	if b.initStarted {
		b.initMu.Unlock()
		return
	}
	b.initStarted = true
	if b.closed {
		b.initErr = webmcp.ErrClosed
		b.initState = StateFailed
		close(done)
		b.initMu.Unlock()
		return
	}
	// The bootstrap context is also the parent of any request-scoped browser
	// handles opened while restoring the selection. Keep its values, but do
	// not make those long-lived handles children of the caller's cancellation
	// once initialization succeeds. A small bridge still cancels an in-flight
	// bootstrap when its caller goes away; broker Close owns the successful
	// browser lifetime afterward.
	initContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	b.initCancel = cancel
	bootstrap := b.bootstrap
	b.initMu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-done:
		}
	}()
	go b.runInitialization(initContext, cancel, bootstrap)
}

func (b *Broker) runInitialization(ctx context.Context, cancel context.CancelFunc, bootstrap func(context.Context) error) {
	var err error
	b.initMu.Lock()
	closed := b.closed
	b.initMu.Unlock()
	if !closed && bootstrap != nil {
		err = bootstrap(ctx)
	}
	// Do not cancel a successful initialization context here: the production
	// browser handle and selected target session may still be using it. Close
	// cancels this context after the provider/session lifecycle has finished.
	// Status derivation reads the delegate without the bootstrap's
	// cancellation so a late cancellation cannot rewrite the settled state.
	if err = b.finishInitialization(ctx, err); err != nil {
		cancel()
	}
}

// finishInitialization records the bootstrap outcome and releases waiters.
// It returns the recorded error, which is ErrClosed when the broker closed
// during a successful bootstrap.
func (b *Broker) finishInitialization(ctx context.Context, err error) error {
	b.initMu.Lock()
	defer b.initMu.Unlock()
	if b.closed && err == nil {
		err = webmcp.ErrClosed
	}
	b.initErr = err
	b.browserState = b.settledBrowserStateLocked(ctx, err)
	if err == nil {
		b.initState = StateReady
	} else {
		b.initState = StateFailed
	}
	close(b.initDone)
	return err
}

func (b *Broker) settledBrowserStateLocked(ctx context.Context, err error) webmcp.BrowserCapabilityState {
	switch {
	case b.closed:
		return webmcp.BrowserCapabilityDisconnected
	case b.browserState != "" && b.browserState != webmcp.BrowserCapabilityInitializing:
		return b.browserState
	case err == nil:
		return initialBrowserState(ctx, b.Broker)
	default:
		return browserStateForError(err)
	}
}

func (b *Broker) initializationError() error {
	b.initMu.Lock()
	defer b.initMu.Unlock()
	return b.initErr
}
