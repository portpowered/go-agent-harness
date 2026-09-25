// Package sessionbroker owns the request-scoped browser broker used by
// browser-enabled sessions: one shared, cancellable capability bootstrap
// before the first browser operation, an explicit capability status, the
// optional broker extensions a session forwards, and the value adapters
// between the WebMCP broker and the session tool-capability contract.
package sessionbroker

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/sessionbroker/bootstrap"
)

// runtimePhase labels a request-scoped runtime that produced no broker.
const runtimePhase = "session_runtime"

// Broker wraps a production broker so every operation first completes the
// session's one capability bootstrap. Close retires the bootstrap, the
// delegate broker, and the runtime resources exactly once.
type Broker struct {
	webmcp.Broker
	closeRuntime func() error
	closeOnce    sync.Once
	closeErr     error

	bootstrap    func(context.Context) error
	initOnce     sync.Once
	initDone     chan struct{}
	initMu       sync.Mutex
	initStarted  bool
	initState    State
	initErr      error
	initCancel   context.CancelFunc
	browserState webmcp.BrowserCapabilityState
	closed       bool
}

// NewFromFactory constructs one request-scoped runtime with factory and wraps
// its broker. A runtime without a broker is closed and reported unavailable.
func NewFromFactory(browser config.BrowserConfig, factory direct.Factory) (webmcp.Broker, error) {
	if factory == nil {
		return nil, errors.New("construct WebMCP broker: doctor factory is nil")
	}
	runtime, err := factory(browser)
	if err != nil {
		return nil, err
	}
	if runtime.Broker == nil {
		if closeErr := direct.CloseRuntime(runtime); closeErr != nil {
			return nil, errors.Join(direct.RuntimeUnavailableError(runtimePhase), closeErr)
		}
		return nil, direct.RuntimeUnavailableError(runtimePhase)
	}
	broker := newBroker(runtime.Broker, runtime.Close)
	broker.bootstrap = bootstrap.New(browser, runtime.Discovery, runtime.Broker, broker.setBrowserCapabilityState)
	return broker, nil
}

func newBroker(delegate webmcp.Broker, closeRuntime func() error) *Broker {
	return &Broker{
		Broker:       delegate,
		closeRuntime: closeRuntime,
		initDone:     make(chan struct{}),
		initState:    StateInitializing,
		browserState: webmcp.BrowserCapabilityInitializing,
	}
}

// InitializeSession runs (or joins) the capability bootstrap.
func (b *Broker) InitializeSession(ctx context.Context) error {
	return b.ensureInitialized(ctx)
}

func (b *Broker) setBrowserCapabilityState(state webmcp.BrowserCapabilityState) {
	if b == nil || state == "" {
		return
	}
	b.initMu.Lock()
	b.browserState = state
	b.initMu.Unlock()
}

// SessionCapabilityStatus reports the current bootstrap lifecycle state.
func (b *Broker) SessionCapabilityStatus() Status {
	if b == nil {
		return Status{
			State:                  StateFailed,
			Err:                    webmcp.ErrClosed,
			BrowserCapabilityState: webmcp.BrowserCapabilityDisconnected,
		}
	}
	b.initMu.Lock()
	defer b.initMu.Unlock()
	if b.initState == "" {
		b.initState = StateInitializing
	}
	return Status{
		State:                  b.initState,
		Err:                    b.initErr,
		BrowserCapabilityState: b.browserState,
	}
}

// Close cancels an in-flight bootstrap, waits for it, and closes the delegate
// broker and runtime once.
func (b *Broker) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		b.initMu.Lock()
		done := b.doneLocked()
		b.closed = true
		cancel := b.initCancel
		started := b.initStarted
		if !started {
			b.initStarted = true
			b.initErr = webmcp.ErrClosed
			b.initState = StateFailed
			b.browserState = webmcp.BrowserCapabilityDisconnected
			close(done)
		}
		b.initMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if started {
			<-done
		}
		b.closeErr = b.Broker.Close()
		if b.closeRuntime != nil {
			b.closeErr = errors.Join(b.closeErr, b.closeRuntime())
		}
	})
	return b.closeErr
}

var (
	_ Initializer                      = (*Broker)(nil)
	_ webmcp.BrokerCastController      = (*Broker)(nil)
	_ webmcp.BrokerMediaCastController = (*Broker)(nil)
	_ webmcp.InvocationWaiter          = (*Broker)(nil)
	_ webmcp.DirectCanceller           = (*Broker)(nil)
	_ webmcp.BrokerTabOpener           = (*Broker)(nil)
	_ webmcp.BrokerTabCreator          = (*Broker)(nil)
)
