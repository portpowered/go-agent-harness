package cli

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
)

func TestWebMCPDirectDefaultSelectionDoesNotChooseAConvenientTab(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	page, target, candidate, _ := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	result := executeDirectCommand(t, configDir, NewFileWebMCPSelectionStore(configDir), directFactory(broker), "context", "--browser", "browser-a", "--json")
	if result.err == nil {
		t.Fatal("context unexpectedly auto-selected a tab with auto_select=off")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("missing selection envelope = %+v", envelope)
	}
	if len(broker.selectCalls) != 0 {
		t.Fatalf("context selected a tab without an explicit selector: %+v", broker.selectCalls)
	}
}

func TestWebMCPDirectSelectionPersistsRedactedOpaqueIDs(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, tool := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
	}
	result := executeDirectCommand(t, configDir, store, directFactory(broker), "select", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	if result.err != nil {
		t.Fatalf("select: %v\nstdout=%s", result.err, result.stdout)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if !envelope.OK {
		t.Fatalf("select envelope = %+v", envelope)
	}
	var data WebMCPDirectContext
	decodeDirectData(t, envelope.Data, &data)
	if data.BrowserID != string(candidate.ID) || data.TargetID != string(target.ID) || data.ToolCount != 1 {
		t.Fatalf("select data = %+v", data)
	}
	if strings.Contains(result.stdout, "secret") || strings.Contains(result.stdout, "#fragment") {
		t.Fatalf("select output exposed URL material: %s", result.stdout)
	}
	selection, err := store.Load()
	if err != nil {
		t.Fatalf("load selection: %v", err)
	}
	if selection.Version != WebMCPSelectionVersion || selection.EndpointID != string(candidate.ID) || selection.BrowserID != string(candidate.ID) || selection.TargetID != string(target.ID) || selection.Origin != string(targetOrigin(target)) {
		t.Fatalf("persisted selection = %+v", selection)
	}
	if len(broker.selectCalls) != 1 || broker.selectCalls[0] != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) {
		t.Fatalf("select calls = %+v", broker.selectCalls)
	}
	if len(broker.activateCalls) != 0 {
		t.Fatalf("select unexpectedly activated target: %+v", broker.activateCalls)
	}
	if broker.closeCalls != 1 {
		t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
	}
}

func TestWebMCPDirectFailedReplacementPreservesStalePersistedSelection(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, target, oldCandidate, _ := directFixture()
	oldCandidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	prior := WebMCPSelection{
		Version:           WebMCPSelectionVersion,
		EndpointID:        string(oldCandidate.ID),
		BrowserID:         string(oldCandidate.ID),
		BrowserInstanceID: oldCandidate.BrowserInstanceID,
		TargetID:          string(target.ID),
		Origin:            target.Origin,
		ContinuityMarker:  "old-document",
		Generation:        4,
		SelectedAt:        time.Unix(4, 0).UTC(),
	}
	if err := store.Save(prior); err != nil {
		t.Fatalf("save stale selection: %v", err)
	}

	_, replacementTarget, replacementCandidate, _ := directFixture()
	replacementCandidate.ID = webmcp.BrowserID(randomizedWebMCPTestID(t, "browser-new-"))
	replacementCandidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	replacementTarget.BrowserID = replacementCandidate.ID
	replacementTarget.ID = webmcp.TargetID(randomizedWebMCPTestID(t, "target-new-"))
	replacementTarget.Generation = 9
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{replacementCandidate},
		targets:    []webmcp.Target{replacementTarget},
		selectErr:  errors.New("replacement attach failed"),
	}
	result := executeDirectCommand(t, configDir, store, directFactory(broker),
		"select", "--auto-select", "single", "--json")
	if result.err == nil {
		t.Fatal("failed replacement unexpectedly succeeded")
	}
	if got, err := store.Load(); err != nil {
		t.Fatalf("load selection after failed replacement: %v", err)
	} else if !reflect.DeepEqual(got, prior) {
		t.Fatalf("failed replacement changed persisted selection: got=%+v want=%+v", got, prior)
	}
	if len(broker.selectCalls) != 0 {
		t.Fatalf("failed replacement recorded a successful selection: %+v", broker.selectCalls)
	}
}

func TestWebMCPDirectSeparateCommandsRejectStaleSelectionWithoutFallback(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	if err := store.Save(WebMCPSelection{
		Version:    WebMCPSelectionVersion,
		EndpointID: "browser-a",
		BrowserID:  "browser-a",
		TargetID:   "missing-tab",
		Origin:     "https://fixture.test",
		SelectedAt: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed selection: %v", err)
	}
	page, _, candidate, _ := directFixture()
	otherTarget := webmcp.Target{BrowserID: candidate.ID, ID: "other-tab", Type: "page", Title: "Fallback must not be used", URL: "https://fixture.test/other", Origin: "https://fixture.test", Eligible: true}
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{otherTarget},
		selected:   page,
	}
	result := executeDirectCommand(t, configDir, store, directFactory(broker), "context", "--json")
	if result.err == nil {
		t.Fatal("context unexpectedly succeeded with stale selection")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("stale context envelope = %+v", envelope)
	}
	if len(broker.selectCalls) != 0 {
		t.Fatalf("stale selection fell back to another target: %+v", broker.selectCalls)
	}
}

// forEachDirectOutputMode runs check once in JSON mode and once in human
// mode; both renderings must honor the same command contract.
func forEachDirectOutputMode(t *testing.T, check func(t *testing.T, jsonMode bool)) {
	t.Helper()
	for _, jsonMode := range []bool{true, false} {
		name := "human"
		if jsonMode {
			name = "json"
		}
		t.Run(name, func(t *testing.T) { check(t, jsonMode) })
	}
}

func withDirectOutputMode(args []string, jsonMode bool) []string {
	if jsonMode {
		return append(args, "--json")
	}
	return args
}

// requireDirectErrorCode decodes a failed JSON result with the given code.
func requireDirectErrorCode(t *testing.T, result directCommandResult, code webmcp.ErrorCode) *webmcp.ToolResultError {
	t.Helper()
	if result.err == nil {
		t.Fatalf("command unexpectedly succeeded: %s", result.stdout)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(code) {
		t.Fatalf("envelope = %+v, want %s", envelope, code)
	}
	return envelope.Error
}

func requireOutputContains(t *testing.T, output string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("output omitted %q: %q", want, output)
		}
	}
}

func requireOutputOmits(t *testing.T, output string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(output, value) {
			t.Fatalf("output exposed %q: %q", value, output)
		}
	}
}

func requireNoSelectionSideEffects(t *testing.T, broker *directCommandBroker) {
	t.Helper()
	if len(broker.selectCalls) != 0 || len(broker.activateCalls) != 0 {
		t.Fatalf("selection side effects: select=%+v activate=%+v", broker.selectCalls, broker.activateCalls)
	}
	if broker.closeCalls != 1 {
		t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
	}
}

func TestWebMCPDirectStaleSelectionRendersOnceAndOffersSelectRecovery(t *testing.T) {
	forEachDirectOutputMode(t, func(t *testing.T, jsonMode bool) {
		configDir := writeDirectConfig(t, "")
		store := NewFileWebMCPSelectionStore(configDir)
		if err := store.Save(WebMCPSelection{
			Version:    WebMCPSelectionVersion,
			EndpointID: "browser-a",
			BrowserID:  "browser-a",
			TargetID:   "missing-tab",
			Origin:     "https://fixture.test",
			SelectedAt: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("seed selection: %v", err)
		}
		page, _, candidate, _ := directFixture()
		otherTarget := webmcp.Target{
			BrowserID: candidate.ID,
			ID:        "other-tab",
			Type:      "page",
			Title:     "Fallback must not be used",
			URL:       "https://fixture.test/other",
			Origin:    "https://fixture.test",
			Eligible:  true,
		}
		broker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{candidate}, targets: []webmcp.Target{otherTarget}, selected: page}
		result := executeDirectCommandThroughAgentRoot(t, configDir, store, directFactory(broker), withDirectOutputMode([]string{"context"}, jsonMode)...)
		if result.err == nil {
			t.Fatal("context unexpectedly succeeded with stale selection")
		}
		if result.stderr != "" {
			t.Fatalf("Cobra added a second diagnostic: %q", result.stderr)
		}
		if jsonMode {
			requireStaleRecoveryJSON(t, result)
			return
		}
		if strings.Count(result.stdout, "Error:") != 1 {
			t.Fatalf("human diagnostic count = %d, output=%q", strings.Count(result.stdout, "Error:"), result.stdout)
		}
		requireOutputContains(t, result.stdout, "stale_selection", operations.SelectionRecoveryCommand)
	})
}

func requireStaleRecoveryJSON(t *testing.T, result directCommandResult) {
	t.Helper()
	resultError := requireDirectErrorCode(t, result, webmcp.ErrorStaleSelection)
	recovery, ok := resultError.Details["recovery"].(map[string]any)
	if !ok || recovery["command"] != operations.SelectionRecoveryCommand {
		t.Fatalf("stale JSON recovery = %#v", resultError.Details["recovery"])
	}
	requireOutputOmits(t, result.stdout, "Error:")
}

func TestWebMCPDirectDiscoveryUsesOnlyExactPageTargets(t *testing.T) {
	page, target, candidate, _ := directFixture()
	uiTarget := target
	uiTarget.ID = "omnibox-popup"
	uiTarget.Type = "browser_ui"
	uiTarget.Title = "Omnibox Popup"
	uiTarget.URL = "chrome://omnibox-popup"
	nonExactPageTarget := target
	nonExactPageTarget.ID = "capitalized-page"
	nonExactPageTarget.Type = "Page"
	targets := []webmcp.Target{uiTarget, nonExactPageTarget, target}

	forEachDirectOutputMode(t, func(t *testing.T, jsonMode bool) {
		broker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{candidate}, targets: targets, selected: page}
		result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), withDirectOutputMode([]string{"tabs", "--browser", string(candidate.ID)}, jsonMode)...)
		if result.err != nil {
			t.Fatalf("tabs: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
		}
		if jsonMode {
			var data WebMCPDirectTabsData
			decodeDirectData(t, requireDirectSuccess(t, result).Data, &data)
			if len(data.Tabs) != 1 || data.Tabs[0].TargetID != string(target.ID) || data.Tabs[0].Type != "page" {
				t.Fatalf("page-only tabs = %+v", data.Tabs)
			}
			return
		}
		requireOutputContains(t, result.stdout, string(target.ID), "Tabs:")
		requireOutputOmits(t, result.stdout, string(uiTarget.ID), uiTarget.Title, string(nonExactPageTarget.ID))
	})

	requirePageOnlyAutoSelection(t, page, target, candidate, targets)

	secondPage := target
	secondPage.ID = "tab-b"
	ambiguousBroker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{candidate}, targets: []webmcp.Target{uiTarget, target, secondPage}}
	ambiguous := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(ambiguousBroker), "select", "--browser", string(candidate.ID), "--auto-select", "single", "--json")
	resultError := requireDirectErrorCode(t, ambiguous, webmcp.ErrorAmbiguousTab)
	if ids := direct.SafeIDList(resultError.Details["candidate_target_ids"]); !reflect.DeepEqual(ids, []string{"tab-a", "tab-b"}) {
		t.Fatalf("multi-page ambiguity candidates = %v", ids)
	}
	if len(ambiguousBroker.selectCalls) != 0 {
		t.Fatalf("ambiguous page selection caused side effects: %+v", ambiguousBroker.selectCalls)
	}
}

func requirePageOnlyAutoSelection(t *testing.T, page webmcp.PageContext, target webmcp.Target, candidate webmcp.BrowserCandidate, targets []webmcp.Target) {
	t.Helper()
	broker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{candidate}, targets: targets, selected: page}
	selected := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), "select", "--browser", string(candidate.ID), "--auto-select", "single", "--json")
	var data WebMCPDirectContext
	decodeDirectData(t, requireDirectSuccess(t, selected).Data, &data)
	if data.TargetID != string(target.ID) {
		t.Fatalf("auto-selected target = %q, want %q", data.TargetID, target.ID)
	}
	if len(broker.selectCalls) != 1 || broker.selectCalls[0].TargetID != target.ID {
		t.Fatalf("auto-selection calls = %+v", broker.selectCalls)
	}
}

func TestWebMCPDirectNoEligibleTabUsesC0DetailsInHumanAndJSONModes(t *testing.T) {
	browserID := randomizedWebMCPTestID(t, "browser-")
	targetID := randomizedWebMCPTestID(t, "target-")
	candidate := webmcp.BrowserCandidate{ID: webmcp.BrowserID(browserID), Source: webmcp.DiscoverySourceExplicit, Product: "Chrome/Test", Protocol: "1.3", Loopback: true}
	ineligible := webmcp.Target{
		BrowserID:         candidate.ID,
		ID:                webmcp.TargetID(targetID),
		Type:              "page",
		Title:             "Blank page",
		URL:               "about:blank",
		EligibilityReason: "internal_url",
	}
	forEachDirectOutputMode(t, func(t *testing.T, jsonMode bool) {
		broker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{candidate}, targets: []webmcp.Target{ineligible}}
		result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), withDirectOutputMode([]string{"select", "--browser", browserID}, jsonMode)...)
		if result.err == nil {
			t.Fatal("select unexpectedly succeeded for an ineligible page")
		}
		requireNoSelectionSideEffects(t, broker)
		if jsonMode {
			requireNoEligibleJSON(t, result, browserID)
		} else {
			requireOutputContains(t, result.stdout, "Error: no_eligible_tab")
		}
		requireOutputOmits(t, result.stdout, "about:blank", targetID)
	})
}

func requireNoEligibleJSON(t *testing.T, result directCommandResult, browserID string) {
	t.Helper()
	resultError := requireDirectErrorCode(t, result, webmcp.ErrorNoEligibleTab)
	if resultError.Details["browser_id"] != browserID || resultError.Details["candidate_count"] != float64(1) {
		t.Fatalf("no-eligible details = %#v", resultError.Details)
	}
	filters, ok := resultError.Details["filters"].(map[string]any)
	if !ok || filters["eligible_only"] != true || filters["include_zero_tool_pages"] != true {
		t.Fatalf("effective filters = %#v", resultError.Details["filters"])
	}
}

func TestWebMCPDirectAmbiguousTabReturnsSortedCandidatesWithoutSelection(t *testing.T) {
	browserID := randomizedWebMCPTestID(t, "browser-")
	firstTargetID := randomizedWebMCPTestID(t, "target-")
	secondTargetID := randomizedWebMCPTestID(t, "target-")
	ineligibleTargetID := randomizedWebMCPTestID(t, "target-")
	filteredTargetID := randomizedWebMCPTestID(t, "target-")
	candidate := webmcp.BrowserCandidate{ID: webmcp.BrowserID(browserID), Source: webmcp.DiscoverySourceExplicit, Product: "Chrome/Test", Protocol: "1.3", Loopback: true}
	targets := []webmcp.Target{
		{BrowserID: candidate.ID, ID: webmcp.TargetID(secondTargetID), Type: "page", Title: "Billing", URL: "https://billing.example.test/private?secret=removed#fragment", Eligible: true},
		{BrowserID: candidate.ID, ID: webmcp.TargetID(ineligibleTargetID), Type: "page", Eligible: false},
		{BrowserID: candidate.ID, ID: webmcp.TargetID(firstTargetID), Type: "page", Title: "https://orders.example.test/private", URL: "https://user:pass@orders.example.test/private?token=secret", Eligible: true},
		{BrowserID: candidate.ID, ID: webmcp.TargetID(secondTargetID), Type: "page", Eligible: true},
		{BrowserID: candidate.ID, ID: webmcp.TargetID(filteredTargetID), Type: "iframe", Eligible: true},
	}
	wantIDs := []string{firstTargetID, secondTargetID}
	sort.Strings(wantIDs)

	forEachDirectOutputMode(t, func(t *testing.T, jsonMode bool) {
		broker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{candidate}, targets: targets}
		result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), withDirectOutputMode([]string{"select", "--browser", browserID}, jsonMode)...)
		if result.err == nil {
			t.Fatal("select unexpectedly chose an ambiguous target")
		}
		requireNoSelectionSideEffects(t, broker)
		if broker.listTargetCalls != 1 {
			t.Fatalf("target enumeration calls = %d, want one", broker.listTargetCalls)
		}
		if jsonMode {
			requireAmbiguousTabJSON(t, result, browserID, wantIDs)
		} else {
			requireOutputContains(t, result.stdout, append([]string{"Error: ambiguous_tab", browserID}, wantIDs...)...)
			requireOutputOmits(t, result.stdout, ineligibleTargetID, filteredTargetID)
		}
		requireOutputOmits(t, result.stdout, "user:pass", "token=secret", "/private")
	})
}

func requireAmbiguousTabJSON(t *testing.T, result directCommandResult, browserID string, wantIDs []string) {
	t.Helper()
	resultError := requireDirectErrorCode(t, result, webmcp.ErrorAmbiguousTab)
	if resultError.Details["browser_id"] != browserID {
		t.Fatalf("ambiguous target browser ID = %#v", resultError.Details["browser_id"])
	}
	if ids := direct.SafeIDList(resultError.Details["candidate_target_ids"]); !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("ambiguous target IDs = %v, want %v", ids, wantIDs)
	}
	choices, ok := resultError.Details["candidate_choices"].([]any)
	if !ok || len(choices) != len(wantIDs) {
		t.Fatalf("ambiguous target choices = %#v", resultError.Details["candidate_choices"])
	}
	for index, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok || choice["target_id"] != wantIDs[index] || choice["browser_id"] != browserID {
			t.Fatalf("ambiguous target choice %d = %#v", index, item)
		}
	}
	requireOutputContains(t, result.stdout, `"action":"ask_customer"`, `"retry_after":"customer_input"`)
}

func TestWebMCPDirectAmbiguousBrowserReturnsSortedCandidatesWithoutFallback(t *testing.T) {
	firstBrowserID := randomizedWebMCPTestID(t, "browser-")
	secondBrowserID := randomizedWebMCPTestID(t, "browser-")
	first := webmcp.BrowserCandidate{ID: webmcp.BrowserID(firstBrowserID), Source: webmcp.DiscoverySourceExplicit, Product: "Chrome/Test", Protocol: "1.3", Loopback: true}
	second := webmcp.BrowserCandidate{ID: webmcp.BrowserID(secondBrowserID), Source: webmcp.DiscoverySourceExplicit, Product: "Chrome/Test", Protocol: "1.3", Loopback: true}
	wantIDs := []string{firstBrowserID, secondBrowserID}
	sort.Strings(wantIDs)

	forEachDirectOutputMode(t, func(t *testing.T, jsonMode bool) {
		broker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{second, first, first}}
		result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), withDirectOutputMode([]string{"select"}, jsonMode)...)
		if result.err == nil {
			t.Fatal("select unexpectedly chose an ambiguous browser")
		}
		requireNoSelectionSideEffects(t, broker)
		if broker.listTargetCalls != 0 {
			t.Fatalf("ambiguous browser listed targets before exact selection: %d calls", broker.listTargetCalls)
		}
		if !jsonMode {
			requireOutputContains(t, result.stdout, append([]string{"Error: ambiguous_browser"}, wantIDs...)...)
			return
		}
		resultError := requireDirectErrorCode(t, result, webmcp.ErrorAmbiguousBrowser)
		if ids := direct.SafeIDList(resultError.Details["candidate_browser_ids"]); !reflect.DeepEqual(ids, wantIDs) {
			t.Fatalf("ambiguous browser IDs = %v, want %v", ids, wantIDs)
		}
	})
}
