package sessionbroker

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// baseBroker implements only the frozen webmcp.Broker interface and counts
// every forwarded call.
type baseBroker struct {
	selected    webmcp.PageContext
	selectedErr error
	calls       map[string]int
	closeErr    error
	closeCalls  int
	watch       chan webmcp.BrokerEvent
}

func (b *baseBroker) record(name string) {
	if b.calls == nil {
		b.calls = map[string]int{}
	}
	b.calls[name]++
}

func (b *baseBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	b.record("discover")
	return nil, nil
}

func (b *baseBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	b.record("list_targets")
	return nil, nil
}

func (b *baseBroker) Select(context.Context, webmcp.TargetSelector) (webmcp.PageContext, error) {
	b.record("select")
	return b.selected, nil
}

func (b *baseBroker) Selected(context.Context) (webmcp.PageContext, error) {
	b.record("selected")
	return b.selected, b.selectedErr
}

func (b *baseBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	b.record("list_tools")
	return webmcp.ToolCatalogSnapshot{}, nil
}

func (b *baseBroker) Invoke(context.Context, webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	b.record("invoke")
	return webmcp.InvokeResult{}, nil
}

func (b *baseBroker) Cancel(context.Context, webmcp.CancelRequest) error {
	b.record("cancel")
	return nil
}

func (b *baseBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	b.record("watch")
	return b.watch
}

func (b *baseBroker) Close() error {
	b.closeCalls++
	return b.closeErr
}

// capabilityBroker adds every optional extension the session forwards.
type capabilityBroker struct {
	baseBroker
	openRequest    webmcp.OpenTabRequest
	navigateURL    string
	castDevices    []webmcp.CastDevice
	castDeviceName string
	events         chan webmcp.BrowserEvent
}

func (b *capabilityBroker) SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error) {
	b.record("select_with_options")
	return b.selected, nil
}

func (b *capabilityBroker) SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error) {
	b.record("selected_with_refresh")
	return b.selected, nil
}

func (b *capabilityBroker) OpenTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.PageContext, error) {
	b.record("open_tab")
	b.openRequest = request
	return b.selected, nil
}

func (b *capabilityBroker) CreateTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.Target, error) {
	b.record("create_tab")
	return webmcp.Target{URL: request.URL}, nil
}

func (b *capabilityBroker) NavigateSelectedTab(_ context.Context, targetURL string) (webmcp.PageContext, error) {
	b.record("navigate_tab")
	b.navigateURL = targetURL
	return b.selected, nil
}

func (b *capabilityBroker) ListCastDevices(context.Context) ([]webmcp.CastDevice, error) {
	b.record("list_cast_devices")
	return append([]webmcp.CastDevice(nil), b.castDevices...), nil
}

func (b *capabilityBroker) CastSelectedTab(_ context.Context, deviceName string) error {
	b.record("cast_tab")
	b.castDeviceName = deviceName
	return nil
}

func (b *capabilityBroker) CastSelectedMedia(_ context.Context, deviceName string) error {
	b.record("cast_media")
	b.castDeviceName = deviceName
	return nil
}

func (b *capabilityBroker) StopCasting(context.Context, string) error {
	b.record("stop_casting")
	return nil
}

func (b *capabilityBroker) WaitInvocation(context.Context, webmcp.InvocationID) (webmcp.InvokeResult, error) {
	b.record("wait_invocation")
	return webmcp.InvokeResult{State: webmcp.InvocationCompleted}, nil
}

func (b *capabilityBroker) CapturePageScreenshot(context.Context) (webmcp.PageScreenshot, error) {
	b.record("screenshot")
	return webmcp.PageScreenshot{}, nil
}

func (b *capabilityBroker) CancelDirect(context.Context, webmcp.DirectCancelRequest) error {
	b.record("cancel_direct")
	return nil
}

func (b *capabilityBroker) WatchBrowserEvents(context.Context) <-chan webmcp.BrowserEvent {
	return b.events
}

// readyBroker wraps delegate with a bootstrap that runs fn once.
func readyBroker(delegate webmcp.Broker, fn func(context.Context) error) *Broker {
	broker := newBroker(delegate, nil)
	broker.bootstrap = fn
	return broker
}

var (
	_ webmcp.BrokerCastController      = (*capabilityBroker)(nil)
	_ webmcp.BrokerMediaCastController = (*capabilityBroker)(nil)
	_ webmcp.BrokerTabOpener           = (*capabilityBroker)(nil)
	_ webmcp.BrokerTabCreator          = (*capabilityBroker)(nil)
	_ webmcp.BrokerTabNavigator        = (*capabilityBroker)(nil)
	_ webmcp.InvocationWaiter          = (*capabilityBroker)(nil)
	_ webmcp.PageScreenshotter         = (*capabilityBroker)(nil)
	_ webmcp.DirectCanceller           = (*capabilityBroker)(nil)
	_ webmcp.BrowserEventWatcher       = (*capabilityBroker)(nil)
)
