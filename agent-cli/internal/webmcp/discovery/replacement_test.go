package discovery

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type sharedTargetIDMapper struct{}

func (sharedTargetIDMapper) TargetID(TargetIdentity) string { return "target-shared" }

type replacementDetachCounter struct {
	calls int
}

func (d *replacementDetachCounter) Detach(context.Context) error {
	d.calls++
	return nil
}

func versionJSONWithBrowserInstance(ws, instance string) string {
	return fmt.Sprintf(`{"Browser":"Chrome/151","Protocol-Version":"1.3","webSocketDebuggerUrl":%q,"browserInstanceId":%q}`, ws, instance)
}

func TestSameAddressFreshBrowserIdentityRetiresLiveAndPersistedSelection(t *testing.T) {
	const (
		cdpURL        = "http://127.0.0.1:9222"
		browserWS     = "ws://127.0.0.1:9222/devtools/browser/reused"
		oldInstance   = "browser-instance-old"
		newInstance   = "browser-instance-new"
		rawTargetID   = "raw-reused"
		targetTitle   = "same target"
		targetPageURL = "https://same.test/page"
	)
	descriptor := targetDescriptor(rawTargetID, targetTitle, targetPageURL, 1)
	descriptors := []TargetDescriptor{descriptor}
	store := NewMemorySelectionStore()
	client := &targetHTTPClient{responses: []*cannedResponse{
		targetJSONResponse(versionJSONWithBrowserInstance(browserWS, oldInstance), http.StatusOK),
		targetJSONResponse(versionJSONWithBrowserInstance(browserWS, newInstance), http.StatusOK),
	}}
	var attached []replacementAttachment
	var listCalls []string
	service := New(Options{
		HTTPClient:     client,
		SelectionStore: store,
		TargetIDMapper: sharedTargetIDMapper{},
		TargetLister: TargetListerFunc(func(_ context.Context, browser BrowserCandidate) ([]TargetDescriptor, error) {
			listCalls = append(listCalls, browser.ID)
			return append([]TargetDescriptor(nil), descriptors...), nil
		}),
		TargetAttacher: TargetAttacherFunc(func(_ context.Context, browser BrowserCandidate, _ Target) (TargetDetacher, error) {
			detacher := &replacementDetachCounter{}
			attached = append(attached, replacementAttachment{browserID: browser.ID, detacher: detacher})
			return detacher, nil
		}),
	})
	t.Cleanup(func() { closeServiceForTest(t, service) })

	inputs := ConnectionInputs{CDPURL: cdpURL}
	oldBrowser, err := service.Discover(context.Background(), inputs)
	if err != nil {
		t.Fatalf("discover old browser: %v", err)
	}
	targetID := "target-shared"
	oldSelection, err := service.SelectTarget(context.Background(), oldBrowser, targetID)
	if err != nil {
		t.Fatalf("select old browser target: %v", err)
	}
	if oldSelection.BrowserID != oldBrowser.ID || oldSelection.TargetID != targetID || oldSelection.Generation != 1 {
		t.Fatalf("old selection = %#v, want exact initial identity", oldSelection)
	}
	if len(attached) != 1 || attached[0].browserID != oldBrowser.ID {
		t.Fatalf("old attachment ledger = %#v, want one old-browser attach", attached)
	}
	oldRecord, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load old persisted selection: %v", err)
	}
	if oldRecord.BrowserID != string(oldBrowser.ID) || oldRecord.TargetID != targetID || oldRecord.Generation != 1 || oldRecord.BrowserInstanceID != oldBrowser.BrowserInstanceID {
		t.Fatalf("old persisted selection = %#v, want browser incarnation and target identity", oldRecord)
	}

	newBrowser, err := service.Discover(context.Background(), inputs)
	if err != nil {
		t.Fatalf("discover replacement browser: %v", err)
	}
	assertBrowserReplacementRetiresOldBrowser(t, service, oldBrowser, newBrowser, targetID, attached[0].detacher.calls)
	if len(listCalls) != 1 || listCalls[0] != string(oldBrowser.ID) {
		t.Fatalf("target-list calls after replacement = %v, want only initial old-browser list", listCalls)
	}

	assertRetiredSelectionStale(t, service, oldBrowser, oldSelection, targetID)

	// A separate process/service must reject the old persisted browser before
	// listing, attaching, activating, or rewriting anything for the replacement.
	replacementClient := newReplacementVersionClient(t, browserWS, newInstance)
	fixture := replacementFixture{inputs: inputs, client: replacementClient, targetID: targetID, descriptors: descriptors, store: store}
	assertPersistedReplacementRejected(t, fixture, oldBrowser, oldRecord)
	for _, secret := range []string{oldInstance, newInstance, browserWS, "http://127.0.0.1:9222"} {
		if strings.Contains(string(store.Bytes()), secret) {
			t.Fatalf("persisted selection contains transport or raw incarnation %q: %s", secret, store.Bytes())
		}
	}

	// The replacement remains available for a new explicit selection; it does
	// not inherit the old session or its persisted target authorization.
	newSelection, err := service.SelectTarget(context.Background(), newBrowser, targetID)
	if err != nil {
		t.Fatalf("explicit replacement selection: %v", err)
	}
	if newSelection.BrowserID != newBrowser.ID || newSelection.TargetID != targetID || newSelection.Generation != 1 {
		t.Fatalf("new explicit selection = %#v, want fresh exact selection", newSelection)
	}
	if len(attached) != 2 || attached[1].browserID != newBrowser.ID {
		t.Fatalf("attachment ledger after explicit replacement selection = %#v", attached)
	}
	if got := store.Writes(); got != 2 {
		t.Fatalf("selection writes = %d, want initial plus explicit replacement selection only", got)
	}
}

type replacementAttachment struct {
	browserID string
	detacher  *replacementDetachCounter
}

type replacementFixture struct {
	inputs      ConnectionInputs
	client      *targetHTTPClient
	targetID    string
	descriptors []TargetDescriptor
	store       *MemorySelectionStore
}

// newReplacementVersionClient serves one version response for the fresh
// browser incarnation at the reused address.
func newReplacementVersionClient(t *testing.T, browserWS, newInstance string) *targetHTTPClient {
	t.Helper()
	response := targetJSONResponse(versionJSONWithBrowserInstance(browserWS, newInstance), http.StatusOK)
	return &targetHTTPClient{responses: []*cannedResponse{response}}
}

func assertBrowserReplacementRetiresOldBrowser(t *testing.T, service *Service, oldBrowser, newBrowser BrowserCandidate, targetID string, oldDetachCalls int) {
	t.Helper()
	if newBrowser.ID == oldBrowser.ID || newBrowser.BrowserInstanceID == oldBrowser.BrowserInstanceID {
		t.Fatalf("replacement candidate = %#v, want a fresh opaque browser identity", newBrowser)
	}
	if newBrowser.BrowserInstanceID == "" || !incarnationIDPattern.MatchString(newBrowser.BrowserInstanceID) {
		t.Fatalf("replacement instance ID = %q, want normalized opaque incarnation", newBrowser.BrowserInstanceID)
	}
	if oldDetachCalls != 1 {
		t.Fatalf("old attachment detach calls = %d, want one", oldDetachCalls)
	}
	if _, ok := service.Selected(); ok {
		t.Fatal("same-address replacement left the old live selection active")
	}
	if _, ok := service.Browser(oldBrowser.ID); ok {
		t.Fatal("retired browser remained eligible in the browser catalog")
	}
	if _, err := service.SelectTarget(context.Background(), oldBrowser, targetID); err == nil {
		t.Fatal("stale browser candidate was accepted after replacement")
	}
	if _, ok := service.Browser(oldBrowser.ID); ok {
		t.Fatal("stale browser selection resurrected the retired browser")
	}
}

func assertRetiredSelectionStale(t *testing.T, service *Service, oldBrowser BrowserCandidate, oldSelection Selection, targetID string) {
	t.Helper()
	if _, err := service.ValidateSelection(context.Background(), oldSelection); err == nil {
		t.Fatal("old live selection validated after browser replacement")
	} else {
		stale := discoveryErrorWithCode(t, err, CodeStaleSelection)
		if stale.Details["browser_id"] != string(oldBrowser.ID) || stale.Details["target_id"] != targetID || stale.Details["selected_generation"] != uint64(1) || stale.Details["reason"] != staleReasonBrowserReplaced {
			t.Fatalf("old selection stale details = %#v", stale.Details)
		}
	}
	if _, err := service.ListTargetSnapshot(context.Background(), oldBrowser, TargetListOptions{BrowserID: oldBrowser.ID}); err == nil {
		t.Fatal("retired browser target catalog remained refreshable")
	} else {
		stale := discoveryErrorWithCode(t, err, CodeStaleSelection)
		if stale.Details["reason"] != staleReasonBrowserReplaced {
			t.Fatalf("retired catalog stale details = %#v", stale.Details)
		}
	}
}

func assertPersistedReplacementRejected(t *testing.T, fixture replacementFixture, oldBrowser BrowserCandidate, oldRecord PersistedSelection) {
	t.Helper()
	replacementClient := fixture.client
	replacementListCalls := 0
	replacementAttachCalls := 0
	replacementService := New(Options{
		HTTPClient:     replacementClient,
		SelectionStore: fixture.store,
		TargetIDMapper: sharedTargetIDMapper{},
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			replacementListCalls++
			return append([]TargetDescriptor(nil), fixture.descriptors...), nil
		}),
		TargetAttacher: TargetAttacherFunc(func(context.Context, BrowserCandidate, Target) (TargetDetacher, error) {
			replacementAttachCalls++
			return &replacementDetachCounter{}, nil
		}),
	})
	t.Cleanup(func() {
		if err := replacementService.Close(); err != nil {
			t.Errorf("close replacement service: %v", err)
		}
	})
	_, err := replacementService.Reconnect(context.Background(), fixture.inputs, ReconnectOptions{AutoSelect: AutoSelectPersisted})
	stale := discoveryErrorWithCode(t, err, CodeStaleSelection)
	if stale.Details["browser_id"] != string(oldBrowser.ID) || stale.Details["target_id"] != fixture.targetID || stale.Details["selected_generation"] != uint64(1) || stale.Details["reason"] != "browser_missing_after_reconnect" {
		t.Fatalf("persisted replacement stale details = %#v", stale.Details)
	}
	if replacementListCalls != 0 || replacementAttachCalls != 0 {
		t.Fatalf("stale reconnect reached replacement operations: list=%d attach=%d", replacementListCalls, replacementAttachCalls)
	}
	if got := len(replacementClient.requests); got != 1 {
		t.Fatalf("stale reconnect version calls = %d, want one discovery probe", got)
	}
	unchanged, err := fixture.store.Load(context.Background())
	if err != nil {
		t.Fatalf("load persisted selection after stale reconnect: %v", err)
	}
	if unchanged != oldRecord {
		t.Fatalf("stale reconnect rewrote persisted selection: before=%#v after=%#v", oldRecord, unchanged)
	}
}

// closeServiceForTest releases the service's selection at test end; a failed
// release is a real teardown defect.
func closeServiceForTest(t *testing.T, service *Service) {
	t.Helper()
	if err := service.Close(); err != nil {
		t.Errorf("close discovery service: %v", err)
	}
}
