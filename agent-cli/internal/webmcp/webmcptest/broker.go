package webmcptest

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// Broker is a scripted, recording webmcp.Broker for tests of the code that
// drives a broker (doctor, operations, session brokers, page tools). Every
// result comes from its fields, and every call is recorded by name in Calls
// and, for the calls that carry a request, in Selects, Invokes and Cancels.
//
// Tests that need an optional broker extension embed *Broker and add the
// extension methods, calling Record so the call order stays observable.
type Broker struct {
	sync.Mutex

	Candidates  []webmcp.BrowserCandidate
	DiscoverErr error
	// Targets are listed for a browser selector; a target with no BrowserID
	// is listed for every selector.
	Targets        []webmcp.Target
	ListTargetsErr error
	// Page is the selected page that Select and Selected report. SelectPage,
	// when set, replaces Page with the page a selector chooses.
	Page         webmcp.PageContext
	SelectPage   func(webmcp.TargetSelector) webmcp.PageContext
	SelectErr    error
	SelectedErr  error
	Catalog      webmcp.ToolCatalogSnapshot
	ListToolsErr error
	InvokeResult webmcp.InvokeResult
	InvokeErr    error
	CancelErr    error
	// Events is the Watch stream; a nil Events watches a closed stream.
	Events   chan webmcp.BrokerEvent
	CloseErr error

	Calls          []string
	DiscoverOption webmcp.DiscoverOptions
	Selects        []webmcp.TargetSelector
	Invokes        []webmcp.InvokeRequest
	Cancels        []webmcp.CancelRequest
}

var _ webmcp.Broker = (*Broker)(nil)

// Record appends one call name to Calls.
func (b *Broker) Record(name string) {
	b.Lock()
	defer b.Unlock()
	b.Calls = append(b.Calls, name)
}

// CallCount reports how many times the named call was recorded.
func (b *Broker) CallCount(name string) int {
	b.Lock()
	defer b.Unlock()
	count := 0
	for _, call := range b.Calls {
		if call == name {
			count++
		}
	}
	return count
}

// Discover returns Candidates and DiscoverErr.
func (b *Broker) Discover(_ context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	b.Record("discover")
	b.Lock()
	defer b.Unlock()
	b.DiscoverOption = options
	return append([]webmcp.BrowserCandidate(nil), b.Candidates...), b.DiscoverErr
}

// ListTargets returns the Targets of the selected browser.
func (b *Broker) ListTargets(_ context.Context, selector webmcp.BrowserSelector) ([]webmcp.Target, error) {
	b.Record("list_targets")
	b.Lock()
	defer b.Unlock()
	if b.ListTargetsErr != nil {
		return nil, b.ListTargetsErr
	}
	targets := make([]webmcp.Target, 0, len(b.Targets))
	for _, target := range b.Targets {
		if target.BrowserID == "" || selector.BrowserID == "" || target.BrowserID == selector.BrowserID {
			targets = append(targets, target)
		}
	}
	return targets, nil
}

// Select records selector and reports the selected page or SelectErr.
func (b *Broker) Select(_ context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	b.Record("select")
	b.Lock()
	defer b.Unlock()
	b.Selects = append(b.Selects, selector)
	if b.SelectErr != nil {
		return b.Page, b.SelectErr
	}
	if b.SelectPage != nil {
		b.Page = b.SelectPage(selector)
	}
	return b.Page, nil
}

// Selected reports Page and SelectedErr.
func (b *Broker) Selected(context.Context) (webmcp.PageContext, error) {
	b.Record("selected")
	b.Lock()
	defer b.Unlock()
	return b.Page, b.SelectedErr
}

// ListTools returns Catalog and ListToolsErr.
func (b *Broker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	b.Record("list_tools")
	b.Lock()
	defer b.Unlock()
	if b.ListToolsErr != nil {
		return webmcp.ToolCatalogSnapshot{}, b.ListToolsErr
	}
	catalog := b.Catalog
	catalog.Tools = append([]webmcp.ToolDescriptor(nil), b.Catalog.Tools...)
	return catalog, nil
}

// Invoke records request and returns InvokeResult and InvokeErr.
func (b *Broker) Invoke(_ context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	b.Record("invoke")
	b.Lock()
	defer b.Unlock()
	b.Invokes = append(b.Invokes, request)
	return b.InvokeResult, b.InvokeErr
}

// Cancel records request and returns CancelErr.
func (b *Broker) Cancel(_ context.Context, request webmcp.CancelRequest) error {
	b.Record("cancel")
	b.Lock()
	defer b.Unlock()
	b.Cancels = append(b.Cancels, request)
	return b.CancelErr
}

// Watch returns Events, or a closed stream when Events is nil.
func (b *Broker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	b.Record("watch")
	b.Lock()
	defer b.Unlock()
	if b.Events != nil {
		return b.Events
	}
	closed := make(chan webmcp.BrokerEvent)
	close(closed)
	return closed
}

// Close records the call and returns CloseErr.
func (b *Broker) Close() error {
	b.Record("close")
	b.Lock()
	defer b.Unlock()
	return b.CloseErr
}
