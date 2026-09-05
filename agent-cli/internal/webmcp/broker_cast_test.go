package webmcp_test

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerCastsTheExactSelectedWebMCPPage(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-cast", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-first", Type: "page"},
			testkit.WithInitialCatalog(pageTool("read_first", "frame-1", `{"type":"object","additionalProperties":false}`))),
		testkit.NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-second", Type: "page"},
			testkit.WithInitialCatalog(pageTool("read_second", "frame-2", `{"type":"object","additionalProperties":false}`)),
			testkit.WithCastDevices(webmcp.CastDevice{Name: "Living Room TV", ID: "sink-2"})),
	))
	defer func() { _ = runtime.Close() }()
	broker := webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: staticDiscoverer{candidate}})
	defer func() { _ = broker.Close() }()

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-second"}); err != nil {
		t.Fatalf("select second tab: %v", err)
	}
	devices, err := broker.ListCastDevices(context.Background())
	if err != nil {
		t.Fatalf("list cast devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "Living Room TV" || devices[0].ID != "sink-2" {
		t.Fatalf("devices = %+v", devices)
	}
	if err := broker.CastSelectedTab(context.Background(), devices[0].Name); err != nil {
		t.Fatalf("cast selected tab: %v", err)
	}
	if err := broker.CastSelectedMedia(context.Background(), devices[0].Name); err != nil {
		t.Fatalf("cast selected media: %v", err)
	}
	navigated, err := broker.NavigateSelectedTab(context.Background(), "https://www.google.com/")
	if err != nil {
		t.Fatalf("navigate cast tab: %v", err)
	}
	if navigated.Key.TargetID != "tab-second" || navigated.URL != "https://www.google.com/" {
		t.Fatalf("navigated cast page = %+v, want the same selected target on Google", navigated)
	}
	if err := broker.StopCasting(context.Background(), devices[0].Name); err != nil {
		t.Fatalf("stop casting: %v", err)
	}

	var castOperations []testkit.Operation
	for _, operation := range runtime.Operations() {
		switch operation.Kind {
		case testkit.OperationListCastDevices, testkit.OperationCastTab, testkit.OperationCastMedia, testkit.OperationNavigate, testkit.OperationStopCasting:
			castOperations = append(castOperations, operation)
		}
	}
	if len(castOperations) != 5 {
		t.Fatalf("cast operations = %+v", castOperations)
	}
	for _, operation := range castOperations {
		if operation.TargetID != "tab-second" {
			t.Fatalf("cast operation targeted %q, want tab-second", operation.TargetID)
		}
	}
	if castOperations[1].DeviceName != "Living Room TV" || castOperations[2].Kind != testkit.OperationCastMedia || castOperations[2].DeviceName != "Living Room TV" || castOperations[3].URL != "https://www.google.com/" || castOperations[4].DeviceName != "Living Room TV" {
		t.Fatalf("device routing = %+v", castOperations)
	}
}
