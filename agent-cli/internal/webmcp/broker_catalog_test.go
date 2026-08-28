package webmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestStatefulBrokerIgnoresDuplicateAndOutOfOrderNavigation(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-navigation", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-navigation", Type: "page", URL: "https://fixture.test/one"},
				testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{}`)),
			)},
		},
	)
	defer func() { _ = runtime.Close() }()

	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
		IDs:        ids,
		Clock:      clock,
	})
	defer func() { _ = broker.Close() }()
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-navigation"}); err != nil {
		t.Fatalf("select target: %v", err)
	}

	value, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open fixture browser: %v", err)
	}
	session := value.(*testkit.ScriptedBrowserHandle).TargetSession("tab-navigation")
	if session == nil {
		t.Fatal("fixture session is nil")
	}
	if _, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{}); err != nil {
		t.Fatalf("list initial tools: %v", err)
	}

	if err := session.Navigate("https://fixture.test/two", "https://fixture.test"); err != nil {
		t.Fatalf("navigate to second document: %v", err)
	}
	selected, err := broker.Selected(context.Background())
	if err != nil {
		t.Fatalf("select after first navigation: %v", err)
	}
	if selected.Generation != 2 {
		t.Fatalf("generation after first navigation = %d, want 2", selected.Generation)
	}

	// These events are deliberately delivered after generation two. They are
	// observations of the already-applied transition, not new documents.
	for _, event := range []webmcp.BrowserEvent{
		{Type: webmcp.EventPageNavigated, PreviousGeneration: 1, Generation: 2, Reason: "duplicate"},
		{Type: webmcp.EventPageNavigated, PreviousGeneration: 0, Generation: 1, Reason: "late"},
		{Type: webmcp.EventPageNavigated, PreviousGeneration: 2, Generation: 3, Sequence: 1, Reason: "late_sequence"},
		{Type: webmcp.EventPageNavigated, PreviousGeneration: 1, Generation: 3, Reason: "out_of_order"},
	} {
		if err := session.Emit(event); err != nil {
			t.Fatalf("emit stale navigation %#v: %v", event, err)
		}
	}
	selected, err = broker.Selected(context.Background())
	if err != nil {
		t.Fatalf("select after stale navigations: %v", err)
	}
	if selected.Generation != 2 {
		t.Fatalf("stale navigation advanced generation to %d, want 2", selected.Generation)
	}

	if err := session.Navigate("https://fixture.test/three", "https://fixture.test"); err != nil {
		t.Fatalf("navigate to third document: %v", err)
	}
	if _, err := broker.Selected(context.Background()); err != nil {
		t.Fatalf("select after third document: %v", err)
	}
	if err := session.EmitToolsAdded(pageTool("read_three", "frame-3", `{}`)); err != nil {
		t.Fatalf("add current-generation tool: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{})
	if err != nil {
		t.Fatalf("list third-document tools: %v", err)
	}
	if snapshot.Generation != 3 || len(snapshot.Tools) != 1 || snapshot.Tools[0].Generation != 3 {
		t.Fatalf("third-document catalog = %#v, want one generation-three tool", snapshot)
	}
}

func TestStatefulBrokerNavigationStormRetiresRefsAndLateResponses(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-storm", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-storm", Type: "page"},
				testkit.WithInitialCatalog(pageTool("tool-0", "frame-0", `{}`)),
			)},
		},
	)
	defer func() { _ = runtime.Close() }()

	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           runtime,
		Discoverer:        staticDiscoverer{candidate},
		IDs:               ids,
		Clock:             clock,
		InvocationTimeout: time.Minute,
	})
	defer func() { _ = broker.Close() }()
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-storm"}); err != nil {
		t.Fatalf("select target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{})
	if err != nil {
		t.Fatalf("list initial tools: %v", err)
	}
	if len(snapshot.Tools) != 1 {
		t.Fatalf("initial tools = %#v, want one tool", snapshot.Tools)
	}
	value, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open fixture browser: %v", err)
	}
	session := value.(*testkit.ScriptedBrowserHandle).TargetSession("tab-storm")
	if session == nil {
		t.Fatal("fixture session is nil")
	}
	session.BlockInvocations()

	oldRefs := make([]webmcp.ToolRef, 0, 6)
	currentRef := snapshot.Tools[0].Ref
	for step := 1; step <= 6; step++ {
		oldRefs = append(oldRefs, currentRef)
		dispatched, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: currentRef, Input: []byte(`{}`)})
		if err != nil {
			t.Fatalf("step %d invoke: %v", step, err)
		}
		observed, err := session.WaitForInvocation(testContext(t))
		if err != nil {
			t.Fatalf("step %d wait for admitted invocation: %v", step, err)
		}
		if observed.ID != dispatched.InvocationID || observed.Generation != uint64(step) {
			t.Fatalf("step %d invocation = %#v, want ID %q at generation %d", step, observed, dispatched.InvocationID, step)
		}

		if err := session.Navigate("https://fixture.test/storm/"+fmt.Sprint(step), "https://fixture.test"); err != nil {
			t.Fatalf("step %d navigate: %v", step, err)
		}
		terminal, err := broker.WaitInvocation(testContext(t), dispatched.InvocationID)
		if err != nil {
			t.Fatalf("step %d wait navigation terminal: %v", step, err)
		}
		if terminal.State != webmcp.InvocationError || terminal.ErrorCode != string(webmcp.ErrorPageNavigated) || terminal.ErrorDetails["previous_generation"] != uint64(step) || terminal.ErrorDetails["current_generation"] != uint64(step+1) {
			t.Fatalf("step %d navigation terminal = %#v, want exact generation transition", step, terminal)
		}

		// The target may still publish the response for the old document. It is
		// reconciled against the retired browser invocation and cannot reopen the
		// broker's registry or affect the next catalog.
		if err := session.ReleaseInvocation(dispatched.InvocationID, []byte(`{"late":true}`)); err != nil {
			t.Fatalf("step %d late response: %v", step, err)
		}
		if _, err := broker.Selected(context.Background()); err != nil {
			t.Fatalf("step %d flush late response: %v", step, err)
		}

		if err := session.EmitToolsAdded(
			pageTool(fmt.Sprintf("tool-%d", step), webmcp.FrameID(fmt.Sprintf("frame-%d", step)), `{}`),
			pageTool(fmt.Sprintf("transient-%d", step), webmcp.FrameID(fmt.Sprintf("transient-frame-%d", step)), `{}`),
		); err != nil {
			t.Fatalf("step %d add current catalog: %v", step, err)
		}
		if err := session.EmitToolsRemoved(webmcp.FrameID(fmt.Sprintf("transient-frame-%d", step)), fmt.Sprintf("transient-%d", step)); err != nil {
			t.Fatalf("step %d remove current catalog entry: %v", step, err)
		}
		snapshot, err = broker.ListTools(context.Background(), webmcp.ListToolsOptions{})
		if err != nil {
			t.Fatalf("step %d list current catalog: %v", step, err)
		}
		if snapshot.Generation != uint64(step+1) || len(snapshot.Tools) != 1 || snapshot.Tools[0].Name != fmt.Sprintf("tool-%d", step) || snapshot.Tools[0].Generation != uint64(step+1) {
			t.Fatalf("step %d catalog = %#v, want one causal fresh tool", step, snapshot)
		}
		currentRef = snapshot.Tools[0].Ref
	}

	for _, ref := range oldRefs {
		assertBrokerError(t, func() error {
			_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: []byte(`{}`)})
			return err
		}, webmcp.ErrorStaleToolRef, "retired storm ref")
	}
	selected, err := broker.Selected(context.Background())
	if err != nil {
		t.Fatalf("selected after storm: %v", err)
	}
	if selected.Generation != 7 || len(broker.PendingInvocations()) != 0 {
		t.Fatalf("post-storm selection = %#v pending=%#v, want generation seven and no pending work", selected, broker.PendingInvocations())
	}
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
