package sessionbroker

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/webmcptest"
)

// baseBroker is the shared scripted broker, which records every forwarded
// call by name.
type baseBroker = webmcptest.Broker

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
	b.Record("select_with_options")
	return b.Page, nil
}

func (b *capabilityBroker) SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error) {
	b.Record("selected_with_refresh")
	return b.Page, nil
}

func (b *capabilityBroker) OpenTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.PageContext, error) {
	b.Record("open_tab")
	b.openRequest = request
	return b.Page, nil
}

func (b *capabilityBroker) CreateTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.Target, error) {
	b.Record("create_tab")
	return webmcp.Target{URL: request.URL}, nil
}

func (b *capabilityBroker) NavigateSelectedTab(_ context.Context, targetURL string) (webmcp.PageContext, error) {
	b.Record("navigate_tab")
	b.navigateURL = targetURL
	return b.Page, nil
}

func (b *capabilityBroker) ListCastDevices(context.Context) ([]webmcp.CastDevice, error) {
	b.Record("list_cast_devices")
	return append([]webmcp.CastDevice(nil), b.castDevices...), nil
}

func (b *capabilityBroker) CastSelectedTab(_ context.Context, deviceName string) error {
	b.Record("cast_tab")
	b.castDeviceName = deviceName
	return nil
}

func (b *capabilityBroker) CastSelectedMedia(_ context.Context, deviceName string) error {
	b.Record("cast_media")
	b.castDeviceName = deviceName
	return nil
}

func (b *capabilityBroker) StopCasting(context.Context, string) error {
	b.Record("stop_casting")
	return nil
}

func (b *capabilityBroker) WaitInvocation(context.Context, webmcp.InvocationID) (webmcp.InvokeResult, error) {
	b.Record("wait_invocation")
	return webmcp.InvokeResult{State: webmcp.InvocationCompleted}, nil
}

func (b *capabilityBroker) CapturePageScreenshot(context.Context) (webmcp.PageScreenshot, error) {
	b.Record("screenshot")
	return webmcp.PageScreenshot{}, nil
}

func (b *capabilityBroker) CancelDirect(context.Context, webmcp.DirectCancelRequest) error {
	b.Record("cancel_direct")
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
