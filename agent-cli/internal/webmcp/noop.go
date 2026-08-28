package webmcp

import "context"

// NoopBroker is the disabled-mode implementation. It never dials a browser
// and returns the stable disabled error for operations that would need one.
type NoopBroker struct{}

var _ Broker = NoopBroker{}

func (NoopBroker) Discover(context.Context, DiscoverOptions) ([]BrowserCandidate, error) {
	return nil, NewBrokerError(ErrorCodeWebMCPDisabled, "WebMCP browser tools are not activated", true, jsonObject(`{"activation":"browser-tools"}`))
}

func (NoopBroker) ListTargets(context.Context, BrowserSelector) ([]Target, error) {
	return nil, NewBrokerError(ErrorCodeWebMCPDisabled, "WebMCP browser tools are not activated", true, jsonObject(`{"activation":"browser-tools"}`))
}

func (NoopBroker) Select(context.Context, TargetSelector) (PageContext, error) {
	return PageContext{}, NewBrokerError(ErrorCodeWebMCPDisabled, "WebMCP browser tools are not activated", true, jsonObject(`{"activation":"browser-tools"}`))
}

func (NoopBroker) Selected(context.Context) (PageContext, error) {
	return PageContext{}, NewBrokerError(ErrorCodeWebMCPDisabled, "WebMCP browser tools are not activated", true, jsonObject(`{"activation":"browser-tools"}`))
}

func (NoopBroker) ListTools(context.Context, ListToolsOptions) (ToolCatalogSnapshot, error) {
	return ToolCatalogSnapshot{}, NewBrokerError(ErrorCodeWebMCPDisabled, "WebMCP browser tools are not activated", true, jsonObject(`{"activation":"browser-tools"}`))
}

func (NoopBroker) Invoke(context.Context, InvokeRequest) (InvokeResult, error) {
	return InvokeResult{}, NewBrokerError(ErrorCodeWebMCPDisabled, "WebMCP browser tools are not activated", true, jsonObject(`{"activation":"browser-tools"}`))
}

func (NoopBroker) Cancel(context.Context, CancelRequest) error {
	return NewBrokerError(ErrorCodeWebMCPDisabled, "WebMCP browser tools are not activated", true, jsonObject(`{"activation":"browser-tools"}`))
}

func (NoopBroker) Watch(context.Context) <-chan BrokerEvent { return nil }

func (NoopBroker) Close() error { return nil }

// DisabledBroker is a descriptive alias for callers composing the disabled
// session path.
type DisabledBroker = NoopBroker

func jsonObject(value string) []byte { return []byte(value) }
