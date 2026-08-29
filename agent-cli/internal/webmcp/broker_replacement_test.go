package webmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

type replacementDiscoverer struct {
	mu         sync.Mutex
	candidates []webmcp.BrowserCandidate
}

func (d *replacementDiscoverer) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]webmcp.BrowserCandidate(nil), d.candidates...), nil
}

func (d *replacementDiscoverer) Replace(candidate webmcp.BrowserCandidate) {
	d.mu.Lock()
	d.candidates = []webmcp.BrowserCandidate{candidate}
	d.mu.Unlock()
}

func countRuntimeOperationsAfter(operations []testkit.Operation, after uint64, kind testkit.OperationKind) int {
	count := 0
	for _, operation := range operations {
		if operation.Sequence > after && operation.Kind == kind {
			count++
		}
	}
	return count
}

func TestStatefulBrokerRetiresSameAddressReplacementBeforeExplicitSelection(t *testing.T) {
	oldCandidate := webmcp.BrowserCandidate{
		ID:                "browser-old",
		HTTPURL:           "http://127.0.0.1:9222",
		BrowserWSURL:      "ws://127.0.0.1:9222/devtools/browser/old",
		BrowserInstanceID: "incarnation-" + strings.Repeat("a", 24),
		Product:           "Chrome/old",
	}
	newCandidate := webmcp.BrowserCandidate{
		ID:                "browser-new",
		HTTPURL:           oldCandidate.HTTPURL,
		BrowserWSURL:      "ws://127.0.0.1:9222/devtools/browser/new",
		BrowserInstanceID: "incarnation-" + strings.Repeat("b", 24),
		Product:           "Chrome/new",
	}
	targetID := webmcp.TargetID("tab-reused")
	oldTarget := webmcp.Target{BrowserID: oldCandidate.ID, ID: targetID, Type: "page", Title: "same target", URL: "https://same.test/page", Origin: "https://same.test", Eligible: true}
	newTarget := oldTarget
	newTarget.BrowserID = newCandidate.ID
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.BrowserConfig{
			Candidate: oldCandidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(oldTarget, testkit.WithInitialCatalog(pageTool("old_tool", "frame-1", `{}`))),
			},
		},
	)
	defer func() { _ = runtime.Close() }()

	discoverer := &replacementDiscoverer{candidates: []webmcp.BrowserCandidate{oldCandidate}}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: discoverer})
	defer func() { _ = broker.Close() }()
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: oldCandidate.ID, TargetID: targetID}); err != nil {
		t.Fatalf("select old target: %v", err)
	}
	oldCatalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list old catalog: %v", err)
	}
	if len(oldCatalog.Tools) != 1 || oldCatalog.Tools[0].Name != "old_tool" {
		t.Fatalf("old catalog = %#v, want old tool", oldCatalog.Tools)
	}
	oldRef := oldCatalog.Tools[0].Ref
	operationCursor := runtime.OperationCursor()
	oldHandle := runtime.Browser(oldCandidate.ID)
	if oldHandle == nil {
		t.Fatal("old runtime handle is nil")
	}
	if _, err := runtime.ReplaceEndpoint(oldCandidate, newCandidate, testkit.NewTargetConfig(newTarget, testkit.WithInitialCatalog(pageTool("new_tool", "frame-1", `{}`)))); err != nil {
		t.Fatalf("replace endpoint after selection: %v", err)
	}
	discoverer.Replace(newCandidate)
	if _, err := broker.Discover(context.Background(), webmcp.DiscoverOptions{}); err != nil {
		t.Fatalf("discover replacement: %v", err)
	}
	if _, err := broker.Selected(context.Background()); err == nil {
		t.Fatal("same-address replacement left the old broker selection active")
	} else {
		var classified *webmcp.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != webmcp.ErrorStaleSelection {
			t.Fatalf("selected after replacement = %v, want stale_selection", err)
		}
	}
	if _, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{}); err == nil {
		t.Fatal("old catalog remained readable after replacement")
	}
	assertBrokerError(t, func() error {
		_, invokeErr := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: oldRef, Input: json.RawMessage(`{}`)})
		return invokeErr
	}, webmcp.ErrorStaleSelection, "old replacement ref")
	if got := countRuntimeOperationsAfter(runtime.Operations(), operationCursor, testkit.OperationAttach); got != 0 {
		t.Fatalf("replacement discovery attached a target %d times", got)
	}
	if got := countRuntimeOperationsAfter(runtime.Operations(), operationCursor, testkit.OperationActivate); got != 0 {
		t.Fatalf("replacement discovery activated a target %d times", got)
	}
	if got := countRuntimeOperationsAfter(runtime.Operations(), operationCursor, testkit.OperationInvoke); got != 0 {
		t.Fatalf("stale replacement path invoked a tool %d times", got)
	}

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: newCandidate.ID, TargetID: targetID}); err != nil {
		t.Fatalf("explicitly select replacement: %v", err)
	}
	newCatalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list replacement catalog: %v", err)
	}
	if len(newCatalog.Tools) != 1 || newCatalog.Tools[0].Name != "new_tool" || newCatalog.Tools[0].Ref == oldRef {
		t.Fatalf("replacement catalog = %#v, want fresh new tool/ref", newCatalog.Tools)
	}
	if got := countRuntimeOperationsAfter(runtime.Operations(), operationCursor, testkit.OperationAttach); got != 1 {
		t.Fatalf("explicit replacement attach count = %d, want one after discovery", got)
	}
}
