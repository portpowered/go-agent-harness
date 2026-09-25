package sessionbroker

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestBrokerPreservesModelFacingTabOperations(t *testing.T) {
	delegate := &capabilityBroker{baseBroker: baseBroker{selected: webmcp.PageContext{
		Key:       webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-new"},
		Connected: true,
	}}}
	broker := readyBroker(delegate, func(context.Context) error { return nil })

	opened, err := broker.OpenTab(context.Background(), webmcp.OpenTabRequest{URL: "https://notes.example.test/", Activate: true})
	if err != nil || opened.Key.TargetID != "tab-new" || delegate.openRequest.URL != "https://notes.example.test/" || !delegate.openRequest.Activate {
		t.Fatalf("open tab = %+v err=%v request=%+v", opened, err, delegate.openRequest)
	}
	if _, err := broker.NavigateSelectedTab(context.Background(), "https://www.google.com/"); err != nil || delegate.navigateURL != "https://www.google.com/" {
		t.Fatalf("navigate = %v URL=%q", err, delegate.navigateURL)
	}
	if created, err := broker.CreateTab(context.Background(), webmcp.OpenTabRequest{URL: "about:blank"}); err != nil || created.URL != "about:blank" {
		t.Fatalf("create tab = %+v err=%v", created, err)
	}
}

func TestBrokerInitializesBeforeFirstCastCall(t *testing.T) {
	delegate := &capabilityBroker{castDevices: []webmcp.CastDevice{{Name: "Office TV", ID: "sink-office"}}}
	bootstrapCalls := 0
	broker := readyBroker(delegate, func(context.Context) error {
		bootstrapCalls++
		return nil
	})

	if err := broker.CastSelectedTab(context.Background(), "Office TV"); err != nil {
		t.Fatalf("first browser call cast selected tab: %v", err)
	}
	if err := broker.CastSelectedMedia(context.Background(), "Office TV"); err != nil {
		t.Fatalf("cast selected media: %v", err)
	}
	devices, err := broker.ListCastDevices(context.Background())
	if err != nil || len(devices) != 1 || devices[0].Name != "Office TV" {
		t.Fatalf("list Cast devices = %+v err=%v", devices, err)
	}
	if err := broker.StopCasting(context.Background(), "Office TV"); err != nil {
		t.Fatalf("stop Cast: %v", err)
	}
	calls := delegate.calls
	if bootstrapCalls != 1 || calls["cast_tab"] != 1 || calls["cast_media"] != 1 || calls["list_cast_devices"] != 1 || calls["stop_casting"] != 1 || delegate.castDeviceName != "Office TV" {
		t.Fatalf("bootstrap=%d calls=%v device=%q, want one bootstrap and one of each Cast call", bootstrapCalls, calls, delegate.castDeviceName)
	}
}

func TestBrokerForwardsEveryOperationAfterBootstrap(t *testing.T) {
	delegate := &capabilityBroker{events: make(chan webmcp.BrowserEvent)}
	broker := readyBroker(delegate, func(context.Context) error { return nil })
	ctx := context.Background()
	initializeAndResetCalls(t, broker, &delegate.baseBroker)

	results := []error{
		func() error { _, err := broker.Discover(ctx, webmcp.DiscoverOptions{}); return err }(),
		func() error { _, err := broker.ListTargets(ctx, webmcp.BrowserSelector{}); return err }(),
		func() error { _, err := broker.Select(ctx, webmcp.TargetSelector{}); return err }(),
		func() error { _, err := broker.Selected(ctx); return err }(),
		func() error { _, err := broker.ListTools(ctx, webmcp.ListToolsOptions{}); return err }(),
		func() error { _, err := broker.Invoke(ctx, webmcp.InvokeRequest{}); return err }(),
		broker.Cancel(ctx, webmcp.CancelRequest{}),
		func() error { _, err := broker.WaitInvocation(ctx, "invocation"); return err }(),
		func() error { _, err := broker.CapturePageScreenshot(ctx); return err }(),
		func() error { _, err := broker.SelectedWithRefresh(ctx, true); return err }(),
		func() error {
			_, err := broker.SelectWithOptions(ctx, webmcp.TargetSelector{}, webmcp.SelectOptions{})
			return err
		}(),
		broker.CancelDirect(ctx, webmcp.DirectCancelRequest{}),
	}
	_ = broker.Watch(ctx)
	for index, err := range results {
		if err != nil {
			t.Fatalf("forwarded operation %d: %v", index, err)
		}
	}
	for _, name := range []string{"discover", "list_targets", "select", "selected", "list_tools", "invoke", "cancel", "watch", "wait_invocation", "screenshot", "selected_with_refresh", "select_with_options", "cancel_direct"} {
		if delegate.calls[name] != 1 {
			t.Fatalf("delegate %s calls = %d, want 1 (all calls: %v)", name, delegate.calls[name], delegate.calls)
		}
	}
	if broker.WatchBrowserEvents(ctx) != (<-chan webmcp.BrowserEvent)(delegate.events) {
		t.Fatal("WatchBrowserEvents did not forward the delegate's semantic stream")
	}
}

func TestBrokerReportsUnsupportedExtensions(t *testing.T) {
	delegate := &baseBroker{}
	broker := readyBroker(delegate, func(context.Context) error { return nil })
	ctx := context.Background()
	initializeAndResetCalls(t, broker, delegate)

	unsupported := map[string]error{}
	_, unsupported["list_cast_devices"] = broker.ListCastDevices(ctx)
	unsupported["cast_tab"] = broker.CastSelectedTab(ctx, "TV")
	unsupported["cast_media"] = broker.CastSelectedMedia(ctx, "TV")
	unsupported["stop_casting"] = broker.StopCasting(ctx, "TV")
	_, unsupported["open_tab"] = broker.OpenTab(ctx, webmcp.OpenTabRequest{})
	_, unsupported["navigate_tab"] = broker.NavigateSelectedTab(ctx, "https://example.test/")
	_, unsupported["create_tab"] = broker.CreateTab(ctx, webmcp.OpenTabRequest{})
	for name, err := range unsupported {
		var classified *webmcp.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserProtocol {
			t.Fatalf("%s error = %v, want unsupported browser-protocol error", name, err)
		}
	}
	var classified *webmcp.ClassifiedError
	if _, err := broker.CapturePageScreenshot(ctx); !errors.As(err, &classified) || classified.Code != webmcp.ErrorUnsupportedWebMCP {
		t.Fatalf("screenshot error = %v, want unsupported WebMCP", err)
	}
	for name, err := range map[string]error{
		"wait":          func() error { _, err := broker.WaitInvocation(ctx, "id"); return err }(),
		"cancel_direct": broker.CancelDirect(ctx, webmcp.DirectCancelRequest{}),
	} {
		if err == nil {
			t.Fatalf("%s on a base broker must fail", name)
		}
	}
	if _, err := broker.SelectedWithRefresh(ctx, false); err != nil || delegate.calls["selected"] != 1 {
		t.Fatalf("refresh fallback = %v calls=%v", err, delegate.calls)
	}
	if _, err := broker.SelectWithOptions(ctx, webmcp.TargetSelector{}, webmcp.SelectOptions{}); err != nil || delegate.calls["select"] != 1 {
		t.Fatalf("select fallback = %v calls=%v", err, delegate.calls)
	}
	if broker.WatchBrowserEvents(ctx) != nil {
		t.Fatal("a delegate without semantic events must yield no stream")
	}
}

func TestBrokerWithoutDelegateIsUnavailable(t *testing.T) {
	var broker *Broker
	ctx := context.Background()
	if _, err := broker.OpenTab(ctx, webmcp.OpenTabRequest{}); err == nil {
		t.Fatal("nil broker OpenTab must fail")
	}
	if _, err := broker.WaitInvocation(ctx, "id"); err == nil {
		t.Fatal("nil broker WaitInvocation must fail")
	}
	if err := broker.InitializeSession(ctx); !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("nil broker initialize = %v, want ErrClosed", err)
	}
	if broker.WatchBrowserEvents(ctx) != nil || broker.Close() != nil {
		t.Fatal("nil broker must have no stream and close cleanly")
	}
}

func initializeAndResetCalls(t *testing.T, broker *Broker, delegate *baseBroker) {
	t.Helper()
	if err := broker.InitializeSession(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	delegate.calls = nil
}

func TestBrokerFailedBootstrapBlocksEveryOperation(t *testing.T) {
	bootstrapErr := webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "unreachable", nil)
	delegate := &capabilityBroker{}
	broker := readyBroker(delegate, func(context.Context) error { return bootstrapErr })
	ctx := context.Background()

	results := []error{
		func() error { _, err := broker.Discover(ctx, webmcp.DiscoverOptions{}); return err }(),
		func() error { _, err := broker.ListTargets(ctx, webmcp.BrowserSelector{}); return err }(),
		func() error { _, err := broker.Select(ctx, webmcp.TargetSelector{}); return err }(),
		func() error { _, err := broker.Selected(ctx); return err }(),
		func() error { _, err := broker.ListTools(ctx, webmcp.ListToolsOptions{}); return err }(),
		func() error { _, err := broker.Invoke(ctx, webmcp.InvokeRequest{}); return err }(),
		broker.Cancel(ctx, webmcp.CancelRequest{}),
		func() error { _, err := broker.ListCastDevices(ctx); return err }(),
		broker.CastSelectedTab(ctx, "TV"),
		broker.CastSelectedMedia(ctx, "TV"),
		broker.StopCasting(ctx, "TV"),
		func() error { _, err := broker.WaitInvocation(ctx, "id"); return err }(),
		func() error { _, err := broker.CapturePageScreenshot(ctx); return err }(),
		func() error { _, err := broker.SelectedWithRefresh(ctx, true); return err }(),
		func() error {
			_, err := broker.SelectWithOptions(ctx, webmcp.TargetSelector{}, webmcp.SelectOptions{})
			return err
		}(),
		func() error { _, err := broker.OpenTab(ctx, webmcp.OpenTabRequest{}); return err }(),
		func() error { _, err := broker.NavigateSelectedTab(ctx, "https://example.test/"); return err }(),
		func() error { _, err := broker.CreateTab(ctx, webmcp.OpenTabRequest{}); return err }(),
		broker.CancelDirect(ctx, webmcp.DirectCancelRequest{}),
	}
	for index, err := range results {
		if !errors.Is(err, bootstrapErr) {
			t.Fatalf("operation %d error = %v, want the retained bootstrap failure", index, err)
		}
	}
	if len(delegate.calls) != 0 {
		t.Fatalf("delegate received calls after a failed bootstrap: %v", delegate.calls)
	}
	if status := broker.SessionCapabilityStatus(); status.BrowserCapabilityState != webmcp.BrowserCapabilityUnavailable {
		t.Fatalf("failed browser state = %q, want unavailable", status.BrowserCapabilityState)
	}
}
