package operations

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

const (
	testBrowserID  = "browser-a"
	testOtherID    = "browser-b"
	testTargetID   = "target-1"
	testSecondID   = "target-2"
	testOrigin     = "https://app.example"
	testOtherSite  = "https://other.example"
	testInstanceID = "instance-1"
	testToolRef    = "webmcp.tool-ref.v1:search"
	testToolName   = "search"
	testGeneration = uint64(7)
)

// testError is a comparable fixture error, so errors.Is recognizes it.
type testError string

func (e testError) Error() string { return string(e) }

const errTestBroker = testError("broker failure")

// fakeBroker is an in-memory broker whose discovery, targets, catalog, and
// invocation results are fixed by the test. It records the calls that
// matter to the operations under test.
type fakeBroker struct {
	mu sync.Mutex

	candidates     []webmcp.BrowserCandidate
	discoverErr    error
	targets        []webmcp.Target
	listTargetsErr error
	selectErr      error
	selected       webmcp.PageContext
	tools          []webmcp.ToolDescriptor
	catalogContext webmcp.PageContext
	listToolsErr   error
	invokeResult   webmcp.InvokeResult
	invokeErr      error
	cancelErr      error
	events         chan webmcp.BrokerEvent

	selects []webmcp.TargetSelector
	invokes []webmcp.InvokeRequest
	cancels []webmcp.CancelRequest
}

func newFakeBroker() *fakeBroker {
	return &fakeBroker{
		candidates: []webmcp.BrowserCandidate{{ID: testBrowserID, Source: webmcp.DiscoverySourceConfigured, Product: "Chrome", BrowserInstanceID: testInstanceID, HTTPURL: "http://127.0.0.1:9222"}},
		targets:    []webmcp.Target{pageTarget(testTargetID, testOrigin)},
		tools:      []webmcp.ToolDescriptor{{Ref: testToolRef, Name: testToolName, FrameID: "main", Origin: testOrigin, Generation: testGeneration}},
	}
}

func pageTarget(id webmcp.TargetID, origin string) webmcp.Target {
	return webmcp.Target{ID: id, Type: targetTypePage, Title: "Page " + string(id), URL: origin + "/path?secret=1#frag", Origin: origin, Eligible: true, Generation: testGeneration}
}

func (b *fakeBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return append([]webmcp.BrowserCandidate(nil), b.candidates...), b.discoverErr
}

func (b *fakeBroker) ListTargets(_ context.Context, selector webmcp.BrowserSelector) ([]webmcp.Target, error) {
	if b.listTargetsErr != nil {
		return nil, b.listTargetsErr
	}
	targets := make([]webmcp.Target, 0, len(b.targets))
	for _, target := range b.targets {
		if target.BrowserID == "" || target.BrowserID == selector.BrowserID {
			targets = append(targets, target)
		}
	}
	return targets, nil
}

func (b *fakeBroker) Select(_ context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selects = append(b.selects, selector)
	if b.selectErr != nil {
		return webmcp.PageContext{}, b.selectErr
	}
	b.selected = webmcp.PageContext{Key: webmcp.PageKey(selector), Title: "Selected", URL: testOrigin + "/app", Origin: testOrigin, Connected: true, Ready: true, Generation: testGeneration}
	return b.selected, nil
}

func (b *fakeBroker) Selected(context.Context) (webmcp.PageContext, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.selected, nil
}

func (b *fakeBroker) ListTools(_ context.Context, options webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	if b.listToolsErr != nil {
		return webmcp.ToolCatalogSnapshot{}, b.listToolsErr
	}
	return webmcp.ToolCatalogSnapshot{Context: b.catalogContext, Generation: testGeneration, Tools: append([]webmcp.ToolDescriptor(nil), b.tools...)}, nil
}

func (b *fakeBroker) Invoke(_ context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.invokes = append(b.invokes, request)
	return b.invokeResult, b.invokeErr
}

func (b *fakeBroker) Cancel(_ context.Context, request webmcp.CancelRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancels = append(b.cancels, request)
	return b.cancelErr
}

func (b *fakeBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	if b.events == nil {
		closed := make(chan webmcp.BrokerEvent)
		close(closed)
		return closed
	}
	return b.events
}

func (b *fakeBroker) Close() error { return nil }

// optionsBroker adds selection options and context refresh.
type optionsBroker struct {
	*fakeBroker
	activations []bool
	refreshed   webmcp.PageContext
}

func (b *optionsBroker) SelectWithOptions(ctx context.Context, selector webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	b.activations = append(b.activations, options.Activate)
	return b.Select(ctx, selector)
}

func (b *optionsBroker) SelectedWithRefresh(ctx context.Context, refresh bool) (webmcp.PageContext, error) {
	if refresh {
		return b.refreshed, nil
	}
	return b.Selected(ctx)
}

// activatorBroker activates targets directly.
type activatorBroker struct {
	*fakeBroker
	activated []webmcp.TargetSelector
}

func (b *activatorBroker) Activate(_ context.Context, selector webmcp.TargetSelector) error {
	b.activated = append(b.activated, selector)
	return b.selectErr
}

// canceller adds direct cancellation and invocation waiting.
type canceller struct {
	*fakeBroker
	directCancels []webmcp.DirectCancelRequest
	waitResult    webmcp.InvokeResult
	waitErr       error
}

func (b *canceller) CancelDirect(_ context.Context, request webmcp.DirectCancelRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.directCancels = append(b.directCancels, request)
	return b.cancelErr
}

func (b *canceller) WaitInvocation(context.Context, webmcp.InvocationID) (webmcp.InvokeResult, error) {
	return b.waitResult, b.waitErr
}

// selectionLoader returns a loader for one persisted record.
func selectionLoader(selection selectionstore.Selection, err error) func() (selectionstore.Selection, error) {
	return func() (selectionstore.Selection, error) { return selection, err }
}

func storedSelection() selectionstore.Selection {
	return selectionstore.Selection{
		Version:           selectionstore.Version,
		EndpointID:        testBrowserID,
		BrowserID:         testBrowserID,
		BrowserInstanceID: testInstanceID,
		TargetID:          testTargetID,
		Origin:            testOrigin,
		Generation:        testGeneration,
	}
}

func singleSelector() Selector {
	return Selector{
		Browser:       config.BrowserConfig{Selection: config.BrowserSelectionConfig{AutoSelect: config.BrowserAutoSelectSingle}},
		LoadSelection: selectionLoader(selectionstore.Selection{}, nil),
	}
}

func classifiedCode(err error) (webmcp.ErrorCode, map[string]any) {
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified == nil {
		return "", nil
	}
	return classified.Code, classified.Details
}
