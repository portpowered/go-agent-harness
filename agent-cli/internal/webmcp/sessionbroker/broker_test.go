package sessionbroker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
)

func TestBrokerCloseCancelsInitializationAndClosesOnce(t *testing.T) {
	base := &capabilityBroker{}
	started := make(chan struct{})
	broker := readyBroker(base, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	firstDone := make(chan error, 1)
	go func() { firstDone <- broker.InitializeSession(context.Background()) }()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- broker.Close() }()
	if err := <-firstDone; !errors.Is(err, context.Canceled) && !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("initialization cancellation error = %v, want cancellation", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close after initialization cancellation: %v", err)
	}
	if err := broker.Close(); err != nil || base.closeCalls != 1 {
		t.Fatalf("repeated close = %v, delegate close calls = %d, want one", err, base.closeCalls)
	}
	status := broker.SessionCapabilityStatus()
	if status.State != StateFailed || status.Err == nil || status.BrowserCapabilityState != webmcp.BrowserCapabilityDisconnected {
		t.Fatalf("canceled capability status = %+v, want failed and disconnected", status)
	}
	if _, err := broker.Selected(context.Background()); !errors.Is(err, context.Canceled) && !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("post-cancel selection error = %v, want no dispatch after failed bootstrap", err)
	}
}

func TestBrokerSuccessfulInitializationRetainsBrowserContext(t *testing.T) {
	contextCanceled := make(chan struct{})
	broker := readyBroker(&capabilityBroker{}, func(ctx context.Context) error {
		go func() {
			<-ctx.Done()
			close(contextCanceled)
		}()
		return nil
	})

	if err := broker.InitializeSession(context.Background()); err != nil {
		t.Fatalf("successful initialization: %v", err)
	}
	select {
	case <-contextCanceled:
		t.Fatal("successful initialization canceled the context used by browser resources")
	default:
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("close after successful initialization: %v", err)
	}
	select {
	case <-contextCanceled:
	case <-time.After(time.Second):
		t.Fatal("broker close did not cancel the retained initialization context")
	}
}

func TestBrokerCloseBeforeFirstUseFailsEveryOperation(t *testing.T) {
	runtimeClosed := 0
	delegate := &capabilityBroker{}
	broker := newBroker(delegate, func() error { runtimeClosed++; return errors.New("runtime close failed") })
	broker.bootstrap = func(context.Context) error { t.Fatal("bootstrap ran after close"); return nil }

	if err := broker.Close(); err == nil || runtimeClosed != 1 || delegate.closeCalls != 1 {
		t.Fatalf("close = %v runtime/delegate closes = %d/%d", err, runtimeClosed, delegate.closeCalls)
	}
	if err := broker.InitializeSession(context.Background()); !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("initialize after close = %v, want ErrClosed", err)
	}
	events := broker.Watch(context.Background())
	if _, open := <-events; open {
		t.Fatal("watch after a failed bootstrap must return a closed stream")
	}
}

func TestBrokerStatusDerivesBrowserStateAfterBootstrap(t *testing.T) {
	selected := webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "b", TargetID: "t"}, Connected: true}
	broker := readyBroker(&baseBroker{selected: selected}, func(context.Context) error { return nil })
	if status := broker.SessionCapabilityStatus(); status.State != StateInitializing || status.BrowserCapabilityState != webmcp.BrowserCapabilityInitializing {
		t.Fatalf("initial status = %+v", status)
	}
	if err := broker.InitializeSession(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if status := broker.SessionCapabilityStatus(); status.State != StateReady || status.BrowserCapabilityState != webmcp.BrowserCapabilitySelected {
		t.Fatalf("ready status = %+v, want selected", status)
	}

	disconnected := webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, "gone", nil)
	failed := readyBroker(&baseBroker{}, func(context.Context) error { return disconnected })
	if err := failed.InitializeSession(context.Background()); !errors.Is(err, disconnected) {
		t.Fatalf("failed initialize = %v", err)
	}
	if status := failed.SessionCapabilityStatus(); status.State != StateFailed || status.BrowserCapabilityState != webmcp.BrowserCapabilityDisconnected {
		t.Fatalf("failed status = %+v, want disconnected", status)
	}

	var nilBroker *Broker
	if status := nilBroker.SessionCapabilityStatus(); status.State != StateFailed || !errors.Is(status.Err, webmcp.ErrClosed) {
		t.Fatalf("nil broker status = %+v", status)
	}
}

func TestBrokerKeepsBootstrapRecordedBrowserState(t *testing.T) {
	broker := newBroker(&baseBroker{}, nil)
	broker.bootstrap = func(context.Context) error {
		broker.setBrowserCapabilityState(webmcp.BrowserCapabilityConnectedUnselected)
		return nil
	}
	if err := broker.InitializeSession(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if got := broker.SessionCapabilityStatus().BrowserCapabilityState; got != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("browser state = %q, want the bootstrap-recorded state", got)
	}
}

func TestBrokerInitializationHonorsCallerCancellation(t *testing.T) {
	release := make(chan struct{})
	broker := readyBroker(&baseBroker{}, func(ctx context.Context) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := broker.InitializeSession(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller = %v, want context.Canceled", err)
	}
}

func TestNewFromFactoryValidatesRuntime(t *testing.T) {
	browser := config.DefaultBrowserConfig()
	if _, err := NewFromFactory(browser, nil); err == nil {
		t.Fatal("nil factory must fail")
	}
	factoryErr := errors.New("factory failed")
	if broker, err := NewFromFactory(browser, func(config.BrowserConfig) (direct.Runtime, error) { return direct.Runtime{}, factoryErr }); !errors.Is(err, factoryErr) || broker != nil {
		t.Fatalf("factory error = %v broker=%v", err, broker)
	}
	closeErr := errors.New("close failed")
	broker, err := NewFromFactory(browser, func(config.BrowserConfig) (direct.Runtime, error) {
		return direct.Runtime{Close: func() error { return closeErr }}, nil
	})
	if broker != nil || !errors.Is(err, closeErr) {
		t.Fatalf("broker-less runtime = %v/%v, want unavailable joined with close error", broker, err)
	}
	if _, err := NewFromFactory(browser, func(config.BrowserConfig) (direct.Runtime, error) { return direct.Runtime{}, nil }); err == nil {
		t.Fatal("broker-less runtime must be unavailable")
	}
	delegate := &capabilityBroker{}
	broker, err = NewFromFactory(browser, func(config.BrowserConfig) (direct.Runtime, error) { return direct.Runtime{Broker: delegate}, nil })
	if err != nil {
		t.Fatalf("construct session broker: %v", err)
	}
	if err := broker.Close(); err != nil || delegate.closeCalls != 1 {
		t.Fatalf("close = %v calls=%d", err, delegate.closeCalls)
	}
}
