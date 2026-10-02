package operations

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/webmcptest"
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

// fakeBroker is the shared scripted broker; newFakeBroker scripts one
// configured browser with one page and one tool, and selects the page a
// selector names.
type fakeBroker = webmcptest.Broker

func newFakeBroker() *fakeBroker {
	return &fakeBroker{
		Candidates: []webmcp.BrowserCandidate{{ID: testBrowserID, Source: webmcp.DiscoverySourceConfigured, Product: "Chrome", BrowserInstanceID: testInstanceID, HTTPURL: "http://127.0.0.1:9222"}},
		Targets:    []webmcp.Target{pageTarget(testTargetID, testOrigin)},
		Catalog:    webmcp.ToolCatalogSnapshot{Generation: testGeneration, Tools: []webmcp.ToolDescriptor{{Ref: testToolRef, Name: testToolName, FrameID: "main", Origin: testOrigin, Generation: testGeneration}}},
		SelectPage: func(selector webmcp.TargetSelector) webmcp.PageContext {
			return webmcp.PageContext{Key: webmcp.PageKey(selector), Title: "Selected", URL: testOrigin + "/app", Origin: testOrigin, Connected: true, Ready: true, Generation: testGeneration}
		},
	}
}

func pageTarget(id webmcp.TargetID, origin string) webmcp.Target {
	return webmcp.Target{ID: id, Type: targetTypePage, Title: "Page " + string(id), URL: origin + "/path?secret=1#frag", Origin: origin, Eligible: true, Generation: testGeneration}
}

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
	return b.SelectErr
}

// canceller adds direct cancellation and invocation waiting.
type canceller struct {
	*fakeBroker
	directCancels []webmcp.DirectCancelRequest
	waitResult    webmcp.InvokeResult
	waitErr       error
}

func (b *canceller) CancelDirect(_ context.Context, request webmcp.DirectCancelRequest) error {
	b.Lock()
	defer b.Unlock()
	b.directCancels = append(b.directCancels, request)
	return b.CancelErr
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
