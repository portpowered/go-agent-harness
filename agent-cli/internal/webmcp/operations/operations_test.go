package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

func TestBrowsersListsRedactedCandidates(t *testing.T) {
	broker := newFakeBroker()
	broker.candidates[0].HTTPURL = "http://user:pw@127.0.0.1:9222/json?token=secret"
	data, err := Browsers(context.Background(), broker, config.BrowserConfig{}, "")
	if err != nil || len(data.Browsers) != 1 {
		t.Fatalf("Browsers = %+v, %v", data, err)
	}
	row := data.Browsers[0]
	if row.ID != testBrowserID || row.Endpoint != "http://127.0.0.1:9222/json" || row.Scope != "loopback" || row.Product != "Chrome" {
		t.Fatalf("browser row = %+v, want redacted loopback endpoint", row)
	}
	if _, err := Browsers(context.Background(), broker, config.BrowserConfig{}, testOtherID); err == nil {
		t.Fatal("an unknown browser ID was listed")
	}
}

func TestTabsFiltersSortsAndMarksSelection(t *testing.T) {
	broker := newFakeBroker()
	ineligible := pageTarget("target-0", testOtherSite)
	ineligible.Eligible = false
	broker.targets = []webmcp.Target{pageTarget(testSecondID, testOrigin), ineligible, pageTarget(testTargetID, testOrigin), {ID: "worker", Type: "service_worker"}}
	broker.selected = webmcp.PageContext{Key: webmcp.PageKey{BrowserID: testBrowserID, TargetID: testTargetID}, Connected: true, Generation: testGeneration}
	data, err := Tabs(context.Background(), broker, config.BrowserConfig{}, TabFilter{})
	if err != nil || len(data.Tabs) != 3 || data.Tabs[0].TargetID != "target-0" || data.Tabs[1].TargetID != testTargetID {
		t.Fatalf("Tabs = %+v, %v, want sorted page targets only", data, err)
	}
	selected := data.Tabs[1]
	if !selected.Selected || !selected.Attached || selected.ToolCount == nil || *selected.ToolCount != 1 || selected.Generation != testGeneration {
		t.Fatalf("selected row = %+v, want selection with catalog size", selected)
	}
	filtered, err := Tabs(context.Background(), broker, config.BrowserConfig{}, TabFilter{OriginContains: "app.", EligibleOnly: true})
	if err != nil || len(filtered.Tabs) != 2 {
		t.Fatalf("filtered Tabs = %+v, %v", filtered, err)
	}
	broker.listTargetsErr = errTestBroker
	if _, err := Tabs(context.Background(), broker, config.BrowserConfig{}, TabFilter{}); !errors.Is(err, errTestBroker) {
		t.Fatalf("target list failure = %v", err)
	}
}

func TestToolsListsSortedCatalogWithValidSchemas(t *testing.T) {
	broker := newFakeBroker()
	readOnly := true
	broker.tools = []webmcp.ToolDescriptor{
		{Ref: "webmcp.tool-ref.v1:b", Name: "b", FrameID: "main", InputSchema: []byte(`{broken`), Annotations: webmcp.ToolAnnotations{ReadOnly: &readOnly, UntrustedContent: &readOnly, AutoSubmit: &readOnly}},
		{Ref: "webmcp.tool-ref.v1:a", Name: "a", FrameID: "main", InputSchema: []byte(`{"type":"object"}`)},
	}
	data, err := Tools(context.Background(), broker, singleSelector(), ToolQuery{IncludeSchemas: true})
	if err != nil || len(data.Tools) != 2 || data.Tools[0].Name != "a" || data.BrowserID != testBrowserID {
		t.Fatalf("Tools = %+v, %v", data, err)
	}
	if string(data.Tools[1].InputSchema) != jsonNull || data.Tools[1].Annotations["read_only"] != true || data.Tools[1].Annotations["autosubmit"] != true {
		t.Fatalf("tool b = %+v, want null schema and annotations", data.Tools[1])
	}
	withoutSchemas, err := Tools(context.Background(), broker, singleSelector(), ToolQuery{})
	if err != nil || withoutSchemas.Tools[0].InputSchema != nil {
		t.Fatalf("Tools without schemas = %+v, %v", withoutSchemas, err)
	}
	broker.listToolsErr = errTestBroker
	if _, err := Tools(context.Background(), broker, singleSelector(), ToolQuery{}); !errors.Is(err, errTestBroker) {
		t.Fatalf("catalog failure = %v", err)
	}
}

func TestDescribeContextRedactsURLAndPrefersCatalogContext(t *testing.T) {
	broker := &optionsBroker{fakeBroker: newFakeBroker()}
	data, err := DescribeContext(context.Background(), broker, singleSelector(), false)
	if err != nil || data.URL != testOrigin+"/app" || data.ToolCount != 1 || data.CatalogGeneration != testGeneration {
		t.Fatalf("DescribeContext = %+v, %v", data, err)
	}
	broker.refreshed = webmcp.PageContext{Key: webmcp.PageKey{BrowserID: testBrowserID, TargetID: testSecondID}, URL: "https://user:pw@app.example/p?q=1#f"}
	broker.catalogContext = webmcp.PageContext{Key: webmcp.PageKey{BrowserID: testBrowserID, TargetID: testTargetID}, Origin: testOrigin, Ready: true, Connected: true}
	data, err = DescribeContext(context.Background(), broker, singleSelector(), true)
	if err != nil || data.TargetID != testTargetID || !data.CatalogReady || data.Origin != testOrigin {
		t.Fatalf("refreshed DescribeContext = %+v, %v", data, err)
	}
	if got := redactedPageURL("https://user:pw@app.example/p?q=1#f"); got != "https://app.example/p" {
		t.Fatalf("redactedPageURL = %q", got)
	}
	if got := redactedPageURL("%zz?secret"); got != "%zz" {
		t.Fatalf("unparseable redactedPageURL = %q", got)
	}
}

func TestSelectPersistsRedactedRecordOnlyWhenConfigured(t *testing.T) {
	broker := &optionsBroker{fakeBroker: newFakeBroker()}
	var saved []selectionstore.Selection
	request := SelectRequest{
		Selector:      singleSelector(),
		Activate:      true,
		SaveSelection: func(selection selectionstore.Selection) error { saved = append(saved, selection); return nil },
	}
	if _, err := Select(context.Background(), broker, request); err != nil || len(saved) != 0 {
		t.Fatalf("Select without persistence = %v, saved %+v", err, saved)
	}
	request.Selector.Browser.Selection.Persist = true
	data, err := Select(context.Background(), broker, request)
	if err != nil || data.TargetID != testTargetID || len(saved) != 1 {
		t.Fatalf("Select = %+v, %v, saved %+v", data, err, saved)
	}
	record := saved[0]
	if record.BrowserID != testBrowserID || record.TargetID != testTargetID || record.BrowserInstanceID != testInstanceID || record.Origin != testOrigin || record.Generation != testGeneration || record.Version != selectionstore.Version {
		t.Fatalf("saved record = %+v", record)
	}
	if len(broker.activations) != 2 || !broker.activations[1] {
		t.Fatalf("activations = %v, want activated selections", broker.activations)
	}
	request.SaveSelection = func(selectionstore.Selection) error { return errTestBroker }
	if _, err := Select(context.Background(), broker, request); !errors.Is(err, errTestBroker) {
		t.Fatalf("save failure = %v", err)
	}
	request.SaveSelection = nil
	if _, err := Select(context.Background(), broker, request); err == nil {
		t.Fatal("persistence without a store succeeded")
	}
}

func TestSelectTargetRequiresOptionsToActivate(t *testing.T) {
	selector := webmcp.TargetSelector{BrowserID: testBrowserID, TargetID: testTargetID}
	if _, err := SelectTarget(context.Background(), newFakeBroker(), selector, true); err == nil {
		t.Fatal("activation succeeded without selection options")
	}
	page, err := SelectTarget(context.Background(), newFakeBroker(), selector, false)
	if err != nil || page.Key.TargetID != testTargetID {
		t.Fatalf("SelectTarget = %+v, %v", page, err)
	}
}

func TestActivateUsesActivatorThenSelectionOptions(t *testing.T) {
	activator := &activatorBroker{fakeBroker: newFakeBroker()}
	data, err := Activate(context.Background(), activator, singleSelector())
	if err != nil || len(activator.activated) != 1 || data.TargetID != testTargetID || data.URL != testOrigin+"/path" {
		t.Fatalf("Activate = %+v, %v", data, err)
	}
	activator.selectErr = errTestBroker
	if _, err := Activate(context.Background(), activator, singleSelector()); !errors.Is(err, errTestBroker) {
		t.Fatalf("activation failure = %v", err)
	}
	withOptions := &optionsBroker{fakeBroker: newFakeBroker()}
	if _, err := Activate(context.Background(), withOptions, singleSelector()); err != nil || len(withOptions.activations) != 1 || !withOptions.activations[0] {
		t.Fatalf("options activation = %v, %v", err, withOptions.activations)
	}
	if _, err := Activate(context.Background(), newFakeBroker(), singleSelector()); err == nil {
		t.Fatal("activation succeeded without activation support")
	}
}

func TestCancelRequiresExactTargetAndConfirmsTerminal(t *testing.T) {
	broker := &canceller{fakeBroker: newFakeBroker()}
	request := CancelRequest{Selector: Selector{LoadSelection: selectionLoader(storedSelection(), nil)}, Args: []string{"inv-1"}, Reason: "stop", Timeout: time.Second}
	data, err := Cancel(context.Background(), broker, request)
	if err != nil || data.InvocationID != "inv-1" || data.Outcome != "confirmed_canceled" {
		t.Fatalf("Cancel = %+v, %v", data, err)
	}
	if len(broker.directCancels) != 1 || broker.directCancels[0].Target.TargetID != testTargetID || broker.directCancels[0].Reason != "stop" {
		t.Fatalf("direct cancels = %+v", broker.directCancels)
	}
	plain := newFakeBroker()
	request.Selector = Selector{Browser: config.BrowserConfig{Selection: config.BrowserSelectionConfig{Tab: testTargetID}}}
	if _, err := Cancel(context.Background(), plain, request); err != nil || len(plain.cancels) != 1 {
		t.Fatalf("broker Cancel = %v, %+v", err, plain.cancels)
	}
	request.Selector = singleSelector()
	if _, err := Cancel(context.Background(), plain, request); err == nil {
		t.Fatal("cancellation fell back to an auto-selected target")
	}
}

func TestCancelValidatesInvocationID(t *testing.T) {
	for name, request := range map[string]CancelRequest{
		"missing": {},
		"both":    {InvocationID: "inv-1", Args: []string{"inv-2"}},
	} {
		_, err := Cancel(context.Background(), newFakeBroker(), request)
		if code, _ := classifiedCode(err); code != webmcp.ErrorInvalidToolInput {
			t.Fatalf("%s: error = %v, want invalid input", name, err)
		}
	}
	broker := newFakeBroker()
	broker.cancelErr = errTestBroker
	request := CancelRequest{InvocationID: "inv-1", Selector: Selector{Browser: config.BrowserConfig{Selection: config.BrowserSelectionConfig{Tab: testTargetID}}}}
	if _, err := Cancel(context.Background(), broker, request); !errors.Is(err, errTestBroker) {
		t.Fatalf("browser rejection = %v", err)
	}
}

func TestRunWatchStreamReportsTerminalStatus(t *testing.T) {
	cases := []struct {
		name   string
		events []webmcp.BrokerEvent
		once   bool
		want   string
	}{
		{name: "ended", events: []webmcp.BrokerEvent{{Type: webmcp.BrokerEventSelected}}, want: WatchStatusEnded},
		{name: "once", events: []webmcp.BrokerEvent{{Type: webmcp.BrokerEventSelected}, {Type: webmcp.BrokerEventCatalogChanged}}, once: true, want: WatchStatusOnce},
		{name: "failed", events: []webmcp.BrokerEvent{{Type: webmcp.BrokerEventSessionClosed, Reason: webmcp.BrokerWatchBufferFullReason}}, want: WatchStatusFailed},
	}
	for _, testCase := range cases {
		stream := make(chan webmcp.BrokerEvent, len(testCase.events))
		for _, event := range testCase.events {
			stream <- event
		}
		close(stream)
		data, err := RunWatchStream(context.Background(), stream, testCase.once)
		if err != nil || data.Status != testCase.want || len(data.Events) == 0 {
			t.Fatalf("%s: RunWatchStream = %+v, %v", testCase.name, data, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if data, err := RunWatchStream(canceled, nil, false); err != nil || data.Status != WatchStatusCanceled {
		t.Fatalf("canceled watch = %+v, %v", data, err)
	}
}

func TestWatchSubscribesBeforeSelection(t *testing.T) {
	broker := newFakeBroker()
	broker.events = make(chan webmcp.BrokerEvent, 1)
	broker.events <- webmcp.BrokerEvent{Type: webmcp.BrokerEventSelected, BrowserID: testBrowserID}
	data, err := Watch(context.Background(), broker, WatchRequest{Selector: singleSelector(), Timeout: time.Minute, Once: true})
	if err != nil || data.Status != WatchStatusOnce || data.Events[0].BrowserID != testBrowserID {
		t.Fatalf("Watch = %+v, %v", data, err)
	}
	broker.discoverErr = errTestBroker
	if _, err := Watch(context.Background(), broker, WatchRequest{Selector: singleSelector()}); !errors.Is(err, errTestBroker) {
		t.Fatalf("selection failure = %v", err)
	}
}

func TestExecuteValidatesBoundsAndPrefersBrowserLoss(t *testing.T) {
	run := func(context.Context) (any, error) { return "done", nil }
	for name, execution := range map[string]Execution{"command": {CommandTimeout: -1}, "operation": {Timeout: -1}} {
		if _, err := Execute(context.Background(), execution, run); err == nil {
			t.Fatalf("%s: negative timeout accepted", name)
		}
	}
	if data, err := Execute(context.Background(), Execution{}, run); err != nil || data != "done" {
		t.Fatalf("Execute = %v, %v", data, err)
	}
	loss := webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, "gone", nil)
	if _, err := Execute(context.Background(), Execution{}, func(context.Context) (any, error) { return nil, errors.Join(errTestBroker, loss) }); !errors.Is(err, loss) || errors.Is(err, errTestBroker) {
		t.Fatalf("Execute error = %v, want only the browser loss", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := Execute(canceled, Execution{Watch: true}, func(context.Context) (any, error) { return nil, errTestBroker })
	if watch, ok := data.(WatchData); err != nil || !ok || watch.Status != WatchStatusCanceled {
		t.Fatalf("canceled watch Execute = %+v, %v", data, err)
	}
}
