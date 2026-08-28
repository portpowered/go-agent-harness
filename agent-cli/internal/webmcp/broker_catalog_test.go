package webmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerBindsCatalogRefsToTheCurrentDescriptor(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page", Title: "A", URL: "https://fixture.test/"},
					testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{"type":"object","properties":{},"additionalProperties":false}`)),
				),
			},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
		IDs:        ids,
		Clock:      clock,
	})
	defer func() {
		if err := broker.Close(); err != nil {
			t.Fatalf("close broker: %v", err)
		}
	}()

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("select target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if snapshot.Generation != 1 || len(snapshot.Tools) != 1 {
		t.Fatalf("snapshot = %#v, want generation one and one tool", snapshot)
	}
	first := snapshot.Tools[0]
	if !webmcp.IsValidToolRef(first.Ref) {
		t.Fatalf("ref %q does not match the C0 grammar", first.Ref)
	}
	for _, secret := range []string{"read_state", "fixture.test", first.SchemaDigest} {
		if secret != "" && strings.Contains(string(first.Ref), secret) {
			t.Fatalf("ref %q exposed descriptor value %q", first.Ref, secret)
		}
	}
	if first.BrowserID != candidate.ID || first.TargetID != "tab-a" || first.FrameID != "frame-1" || first.Generation != 1 || first.SchemaDigest == "" {
		t.Fatalf("descriptor binding fields = %#v", first)
	}

	refAgain, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{Refresh: true, IncludeSchemas: true})
	if err != nil {
		t.Fatalf("refresh tools: %v", err)
	}
	if len(refAgain.Tools) != 1 || refAgain.Tools[0].Ref != first.Ref {
		t.Fatalf("unchanged descriptor ref = %q, want stable %q", refAgain.Tools[0].Ref, first.Ref)
	}

	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open fixture handle: %v", err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession("tab-a")
	if session == nil {
		t.Fatal("fixture session is nil")
	}
	if err := session.EmitToolsAdded(pageTool("read_state", "frame-1", `{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"],"additionalProperties":false}`)); err != nil {
		t.Fatalf("replace descriptor: %v", err)
	}
	changed, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list changed tools: %v", err)
	}
	if len(changed.Tools) != 1 || changed.Tools[0].Ref == first.Ref || changed.Tools[0].SchemaDigest == first.SchemaDigest {
		t.Fatalf("changed descriptor = %#v, want a new ref and digest", changed.Tools)
	}
	assertBrokerError(t, func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: first.Ref, Input: json.RawMessage(`{}`)})
		return err
	}, webmcp.ErrorStaleToolRef, "ref changed by schema update")
	assertNoOperation(t, runtime, testkit.OperationInvoke)

	if err := session.EmitToolsRemoved("frame-1", "read_state"); err != nil {
		t.Fatalf("remove descriptor: %v", err)
	}
	removed, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list removed tools: %v", err)
	}
	if len(removed.Tools) != 0 {
		t.Fatalf("catalog after removal = %#v, want empty", removed.Tools)
	}
	assertBrokerError(t, func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: changed.Tools[0].Ref, Input: json.RawMessage(`{}`)})
		return err
	}, webmcp.ErrorStaleToolRef, "removed ref")
	assertNoOperation(t, runtime, testkit.OperationInvoke)

	if err := session.EmitToolsAdded(pageTool("read_state", "frame-1", `{"type":"object","properties":{},"additionalProperties":false}`)); err != nil {
		t.Fatalf("re-add descriptor: %v", err)
	}
	current, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list re-added tools: %v", err)
	}
	currentRef := current.Tools[0].Ref
	if err := session.EmitToolsRemoved("frame-1", "read_state"); err != nil {
		t.Fatalf("remove old frame descriptor: %v", err)
	}
	if err := session.EmitToolsAdded(pageTool("read_state", "frame-2", `{"type":"object","properties":{},"additionalProperties":false}`)); err != nil {
		t.Fatalf("add changed frame descriptor: %v", err)
	}
	frameChanged, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list changed frame tools: %v", err)
	}
	if len(frameChanged.Tools) != 1 || frameChanged.Tools[0].FrameID != "frame-2" || frameChanged.Tools[0].Ref == currentRef {
		t.Fatalf("frame-changed catalog = %#v, want one new frame-bound ref", frameChanged.Tools)
	}
	assertBrokerError(t, func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: currentRef, Input: json.RawMessage(`{}`)})
		return err
	}, webmcp.ErrorStaleToolRef, "frame-changed ref")
	assertNoOperation(t, runtime, testkit.OperationInvoke)
	currentRef = frameChanged.Tools[0].Ref
	if err := session.Navigate("https://fixture.test/next", "https://fixture.test"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	postNavigation, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list after navigation: %v", err)
	}
	if postNavigation.Generation != 2 || len(postNavigation.Tools) != 0 {
		t.Fatalf("catalog after navigation = generation %d tools %#v, want generation two and empty", postNavigation.Generation, postNavigation.Tools)
	}
	assertBrokerError(t, func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: currentRef, Input: json.RawMessage(`{}`)})
		return err
	}, webmcp.ErrorStaleToolRef, "navigation ref")
	assertNoOperation(t, runtime, testkit.OperationInvoke)

	assertBrokerError(t, func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: "webmcp.tool-ref.v0:AAECAwQFBgcICQoLDA0ODw", Input: json.RawMessage(`{}`)})
		return err
	}, webmcp.ErrorInvalidToolInput, "malformed ref")
	if err := session.EmitTargetDetached("fixture test"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	assertBrokerError(t, func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: currentRef, Input: json.RawMessage(`{}`)})
		return err
	}, webmcp.ErrorStaleSelection, "detached selected target")
	assertNoOperation(t, runtime, testkit.OperationInvoke)
}

func TestStatefulBrokerRetiresRefsWhenSelectionSwitches(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(webmcp.Target{ID: "tab-a", Type: "page"}, testkit.WithInitialCatalog(pageTool("read_a", "frame-a", `{}`))),
				testkit.NewTargetConfig(webmcp.Target{ID: "tab-b", Type: "page"}, testkit.WithInitialCatalog(pageTool("read_b", "frame-b", `{}`))),
			},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: staticDiscoverer{candidate}})
	defer func() { _ = broker.Close() }()
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("select tab-a: %v", err)
	}
	first, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list tab-a tools: %v", err)
	}
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-b"}); err != nil {
		t.Fatalf("select tab-b: %v", err)
	}
	assertBrokerError(t, func() error {
		_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: first.Tools[0].Ref, Input: json.RawMessage(`{}`)})
		return err
	}, webmcp.ErrorStaleToolRef, "ref from switched target")
	assertNoOperation(t, runtime, testkit.OperationInvoke)
}

func pageTool(name string, frame webmcp.FrameID, schema string) webmcp.ToolDescriptor {
	return webmcp.ToolDescriptor{Name: name, FrameID: frame, InputSchema: json.RawMessage(schema), Description: "fixture tool"}
}

type staticDiscoverer []webmcp.BrowserCandidate

func (d staticDiscoverer) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return append([]webmcp.BrowserCandidate(nil), d...), nil
}

func assertBrokerError(t *testing.T, operation func() error, want webmcp.ErrorCode, label string) {
	t.Helper()
	err := operation()
	if err == nil {
		t.Fatalf("%s: operation succeeded, want %s", label, want)
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != want {
		t.Fatalf("%s: error = %v (%T), want classified %s", label, err, err, want)
	}
	if want == webmcp.ErrorInvalidToolInput {
		issues, ok := classified.Details["issues"].([]webmcp.ToolResultIssue)
		if !ok || len(issues) != 1 || issues[0].Path != "/tool_ref" {
			t.Fatalf("%s: details = %#v, want /tool_ref issue", label, classified.Details)
		}
	}
}

func assertNoOperation(t *testing.T, runtime *testkit.ScriptedBrowserRuntime, kind testkit.OperationKind) {
	t.Helper()
	for _, operation := range runtime.Operations() {
		if operation.Kind == kind {
			t.Fatalf("found unexpected %s operation: %#v", kind, operation)
		}
	}
}
