package direct

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// testError is a constant error value for scripted failures.
type testError string

func (e testError) Error() string { return string(e) }

const errTest testError = "scripted failure"

// discoverBroker scripts only discovery; the embedded nil Broker is never
// reached by the functions under test.
type discoverBroker struct {
	webmcp.Broker
	candidates []webmcp.BrowserCandidate
	err        error
	closeErr   error
	panics     bool
}

func (b *discoverBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return b.candidates, b.err
}

func (b *discoverBroker) Close() error {
	if b.panics {
		panic("close")
	}
	return b.closeErr
}

func TestDiscoverBrowsersNormalizesAndNarrows(t *testing.T) {
	broker := &discoverBroker{candidates: []webmcp.BrowserCandidate{{ID: "browser-b"}, {ID: "bad id"}, {ID: "browser-a"}, {ID: "browser-a"}}}
	candidates, err := DiscoverBrowsers(context.Background(), broker, config.BrowserConfig{}, "")
	if err != nil || len(candidates) != 2 || candidates[0].ID != "browser-a" {
		t.Fatalf("DiscoverBrowsers = %+v, %v", candidates, err)
	}
	narrowed, err := DiscoverBrowsers(context.Background(), broker, config.BrowserConfig{}, "browser-b")
	if err != nil || len(narrowed) != 1 || narrowed[0].ID != "browser-b" {
		t.Fatalf("narrowed = %+v, %v", narrowed, err)
	}
	_, err = DiscoverBrowsers(context.Background(), broker, config.BrowserConfig{}, "browser-z")
	requireClassified(t, err, webmcp.ErrorStaleSelection, detailReason, "browser_not_found")

	_, err = DiscoverBrowsers(context.Background(), &discoverBroker{}, config.BrowserConfig{}, "")
	requireClassified(t, err, webmcp.ErrorEndpointNotFound, "endpoint_kind", "discovery")
	if _, err := DiscoverCandidates(context.Background(), &discoverBroker{err: errTest}, config.BrowserConfig{}); !errors.Is(err, errTest) {
		t.Fatalf("discover error = %v", err)
	}
	_, err = DiscoverCandidates(context.Background(), nil, config.BrowserConfig{})
	requireClassified(t, err, webmcp.ErrorBrowserProtocol, "phase", "discovery")
}

func TestConstructRuntimeHonorsDeadlineAndClosesLateRuntime(t *testing.T) {
	if _, err := ConstructRuntime(context.Background(), nil, config.BrowserConfig{}); err == nil {
		t.Fatal("ConstructRuntime accepted a nil factory")
	}
	broker := &discoverBroker{}
	runtime, err := ConstructRuntime(context.Background(), func(config.BrowserConfig) (Runtime, error) { return Runtime{Broker: broker}, nil }, config.BrowserConfig{})
	if err != nil || runtime.Broker != broker || !runtime.Owned() {
		t.Fatalf("ConstructRuntime = %+v, %v", runtime, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release := make(chan struct{})
	closed := make(chan struct{})
	factory := func(config.BrowserConfig) (Runtime, error) {
		<-release
		return Runtime{Close: func() error { close(closed); return nil }}, nil
	}
	if _, err := ConstructRuntime(ctx, factory, config.BrowserConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled construct error = %v", err)
	}
	close(release)
	<-closed
}

func TestRunOperationPrefersCompletedResult(t *testing.T) {
	if _, err := RunOperation(context.Background(), nil, nil, config.BrowserConfig{}); err == nil {
		t.Fatal("RunOperation accepted a nil operation")
	}
	data, err := RunOperation(context.Background(), func(context.Context, webmcp.Broker, config.BrowserConfig) (any, error) { return "done", nil }, nil, config.BrowserConfig{})
	if err != nil || data != "done" {
		t.Fatalf("RunOperation = %v, %v", data, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err = RunOperation(ctx, func(opCtx context.Context, _ webmcp.Broker, _ config.BrowserConfig) (any, error) {
		<-opCtx.Done()
		return "late", opCtx.Err()
	}, nil, config.BrowserConfig{})
	if data != "late" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RunOperation = %v, %v", data, err)
	}
}

func TestCloseRuntimeJoinsAndRecoversFailures(t *testing.T) {
	if err := CloseRuntimeBounded(Runtime{}); err != nil {
		t.Fatalf("unowned close = %v", err)
	}
	hookErr := testError("hook")
	err := CloseRuntimeBounded(Runtime{Broker: &discoverBroker{closeErr: errTest}, Close: func() error { return hookErr }})
	if !errors.Is(err, errTest) || !errors.Is(err, hookErr) {
		t.Fatalf("joined close error = %v", err)
	}
	if err := CloseRuntime(Runtime{Broker: &discoverBroker{panics: true}}); err == nil {
		t.Fatal("panicking close was not reported")
	}
}

func TestRuntimeErrorClassification(t *testing.T) {
	details := requireDetails(t, InvalidInputError("bad", "/x"), webmcp.ErrorInvalidToolInput)
	if issues, ok := details["issues"].([]webmcp.ToolResultIssue); !ok || len(issues) != 1 || issues[0].Path != "/x" {
		t.Fatalf("invalid input details = %+v", details)
	}
	if RuntimeFactoryError(nil) != nil || RuntimeFactoryFailure(nil) != nil {
		t.Fatal("nil factory errors must stay nil")
	}
	requireClassified(t, RuntimeFactoryFailure(errTest), webmcp.ErrorBrowserProtocol, "phase", phaseRuntimeFactory)
	classified := webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "x", nil)
	if got := RuntimeFactoryFailure(classified); !errors.Is(got, classified) {
		t.Fatalf("classified factory error replaced: %v", got)
	}
	if got := RuntimeFactoryFailure(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("deadline factory error replaced: %v", got)
	}
}

func TestPreferBrowserDisconnectedFindsNestedCause(t *testing.T) {
	disconnected := webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, "gone", nil)
	wrapped := fmt.Errorf("attach: %w", errors.Join(errTest, disconnected))
	if got := PreferBrowserDisconnected(wrapped); !errors.Is(got, disconnected) || errors.Is(got, errTest) {
		t.Fatalf("PreferBrowserDisconnected = %v", got)
	}
	if got := PreferBrowserDisconnected(errors.Join(errTest)); !errors.Is(got, errTest) {
		t.Fatalf("PreferBrowserDisconnected without cause = %v", got)
	}
	if BrowserDisconnectedError(nil) != nil {
		t.Fatal("nil error has no disconnected cause")
	}
}
