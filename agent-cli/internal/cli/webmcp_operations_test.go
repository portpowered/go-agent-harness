package cli

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/spf13/cobra"
)

func TestWebMCPDirectCommandTreeIsFrozen(t *testing.T) {
	command := NewWebMCPCommand(flags.NewGlobalFlags()).Generate()
	got := make([]string, 0, len(command.Commands()))
	for _, child := range command.Commands() {
		got = append(got, child.Name())
	}
	sort.Strings(got)
	want := []string{"activate", "browsers", "cancel", "context", "doctor", "invoke", "select", "tabs", "tools", "watch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WebMCP command names = %v, want %v", got, want)
	}
	for _, forbidden := range []string{"launch", "browser"} {
		for _, name := range got {
			if name == forbidden {
				t.Fatalf("unexpected forbidden command %q", forbidden)
			}
		}
	}
}

func TestDirectInvocationResultErrorPropagatesFreshnessRetryability(t *testing.T) {
	err := directInvocationResultError(webmcp.InvokeResult{
		InvocationID:        "broker-invocation",
		BrowserInvocationID: "browser-invocation",
		State:               webmcp.InvocationError,
		ErrorCode:           string(webmcp.ErrorInvocationFailed),
		ErrorDetails: map[string]any{
			"phase":          "result_freshness",
			"safe_retryable": true,
			"recovery":       "refresh and retry",
		},
	}, "webmcp.tool-ref.v1:test")
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified == nil {
		t.Fatalf("freshness error = %v, want classified error", err)
	}
	if !classified.Retryable || classified.Details["phase"] != "result_freshness" || classified.Details["invocation_id"] != "browser-invocation" {
		t.Fatalf("freshness classified error = %#v, want retryable browser-correlated failure", classified)
	}
	if _, leaked := classified.Details["safe_retryable"]; leaked {
		t.Fatalf("internal retry marker leaked into direct error details: %#v", classified.Details)
	}
}

func TestWebMCPWatchHelpDocumentsCrossProcessObservationBoundary(t *testing.T) {
	command := NewWebMCPCommand(flags.NewGlobalFlags()).Generate()
	watch, _, err := command.Find([]string{"watch"})
	if err != nil {
		t.Fatalf("find webmcp watch command: %v", err)
	}
	tools, _, err := command.Find([]string{"tools"})
	if err != nil {
		t.Fatalf("find webmcp tools command: %v", err)
	}

	for _, test := range []struct {
		name string
		text string
	}{
		{name: "watch", text: watch.Long},
		{name: "tools --watch", text: tools.Long},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, want := range []string{
				"toolsAdded/toolsRemoved -> catalog_changed",
				"toolInvoked             -> invocation_created",
				"toolResponded           -> invocation_terminal",
				"selected and session_closed are watcher-local lifecycle events",
				"broker admission, approval, and cancellation-request history remains",
				"process-local; no cross-process visibility",
				"failed session_closed event",
			} {
				if !strings.Contains(test.text, want) {
					t.Errorf("help text does not contain %q:\n%s", want, test.text)
				}
			}
		})
	}
}

func TestWebMCPDirectInvokeAndCancelHelpDocumentHandoff(t *testing.T) {
	for _, test := range []struct {
		name string
		want []string
	}{
		{name: "invoke", want: []string{"stderr", "invocation_id", "dispatched", "Stdout", "SIGINT"}},
		{name: "cancel", want: []string{"Two-process flow", "receipt", "exact", "falls back", "stdout"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			operations := NewWebMCPOperationsCommand(flags.NewGlobalFlags())
			root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
			operations.AddCommands(root)
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs([]string{test.name, "--help"})
			if err := root.Execute(); err != nil {
				t.Fatalf("execute %s help: %v", test.name, err)
			}
			description := stdout.String() + stderr.String()
			for _, want := range test.want {
				if !strings.Contains(description, want) {
					t.Fatalf("help omitted %q:\n%s", want, description)
				}
			}
		})
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

func TestWebMCPDirectSelectReplacesStalePersistedSelectionAndActivateRestoresIt(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, oldTarget, oldCandidate, _ := directFixture()
	oldCandidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	oldSelection := WebMCPSelection{
		Version:           WebMCPSelectionVersion,
		EndpointID:        string(oldCandidate.ID),
		BrowserID:         string(oldCandidate.ID),
		BrowserInstanceID: oldCandidate.BrowserInstanceID,
		TargetID:          string(oldTarget.ID),
		Origin:            oldTarget.Origin,
		ContinuityMarker:  "old-document",
		Generation:        4,
		SelectedAt:        time.Unix(4, 0).UTC(),
	}
	if err := store.Save(oldSelection); err != nil {
		t.Fatalf("save stale selection: %v", err)
	}

	page, target, candidate, tool := directFixture()
	candidate.ID = webmcp.BrowserID(randomizedWebMCPTestID(t, "browser-new-"))
	candidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	target.BrowserID = candidate.ID
	target.ID = webmcp.TargetID(randomizedWebMCPTestID(t, "target-new-"))
	target.ContinuityMarker = "new-document"
	target.Generation = 9
	page.Key = webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID}
	page.Generation = target.Generation
	page.Origin = target.Origin
	catalog := webmcp.ToolCatalogSnapshot{
		Context:    page,
		Generation: page.Generation,
		Tools:      []webmcp.ToolDescriptor{tool},
	}
	replacement := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    catalog,
	}

	// Supplying the endpoint and single-selection policy is the documented
	// recovery shape after a browser restart. The stale file must not be
	// loaded as a prerequisite for this explicit select operation.
	selected := executeDirectCommand(t, configDir, store, directFactory(replacement),
		"select", "--cdp-url", "http://127.0.0.1:9222", "--auto-select", "single", "--json")
	requireDirectSuccess(t, selected)
	updated, err := store.Load()
	if err != nil {
		t.Fatalf("load replacement selection: %v", err)
	}
	if updated.BrowserID != string(candidate.ID) || updated.BrowserInstanceID != candidate.BrowserInstanceID ||
		updated.TargetID != string(target.ID) || updated.Generation != target.Generation || updated.ContinuityMarker != target.ContinuityMarker {
		t.Fatalf("replacement selection = %+v, want live identity over stale=%+v", updated, oldSelection)
	}
	if len(replacement.selectCalls) != 1 || replacement.selectCalls[0] != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) {
		t.Fatalf("replacement select calls = %+v", replacement.selectCalls)
	}

	// A subsequent command with no IDs must consume the newly persisted exact
	// target, just as it would in a fresh process after the recovery command.
	activation := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	activated := executeDirectCommand(t, configDir, store, directFactory(activation), "activate", "--json")
	requireDirectSuccess(t, activated)
	if len(activation.activateCalls) != 1 || activation.activateCalls[0] != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) {
		t.Fatalf("restored activation calls = %+v", activation.activateCalls)
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

func TestWebMCPDirectStaleSelectionRendersOnceAndOffersSelectRecovery(t *testing.T) {
	for _, testCase := range []struct {
		name string
		json bool
	}{
		{name: "human"},
		{name: "json", json: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
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
			broker := &directCommandBroker{
				candidates: []webmcp.BrowserCandidate{candidate},
				targets:    []webmcp.Target{otherTarget},
				selected:   page,
			}
			args := []string{"context"}
			if testCase.json {
				args = append(args, "--json")
			}
			result := executeDirectCommandThroughAgentRoot(t, configDir, store, directFactory(broker), args...)
			if result.err == nil {
				t.Fatal("context unexpectedly succeeded with stale selection")
			}
			if result.stderr != "" {
				t.Fatalf("Cobra added a second diagnostic: %q", result.stderr)
			}

			if testCase.json {
				envelope := decodeDirectEnvelope(t, result.stdout)
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
					t.Fatalf("stale JSON envelope = %+v", envelope)
				}
				recovery, ok := envelope.Error.Details["recovery"].(map[string]any)
				if !ok || recovery["command"] != directSelectionRecoveryCommand {
					t.Fatalf("stale JSON recovery = %#v", envelope.Error.Details["recovery"])
				}
				if strings.Contains(result.stdout, "Error:") {
					t.Fatalf("JSON output included a human diagnostic: %q", result.stdout)
				}
				return
			}

			if strings.Count(result.stdout, "Error:") != 1 {
				t.Fatalf("human diagnostic count = %d, output=%q", strings.Count(result.stdout, "Error:"), result.stdout)
			}
			for _, want := range []string{"stale_selection", directSelectionRecoveryCommand} {
				if !strings.Contains(result.stdout, want) {
					t.Fatalf("human output omitted %q: %q", want, result.stdout)
				}
			}
		})
	}
}

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

	for _, testCase := range []struct {
		name string
		json bool
	}{
		{name: "json", json: true},
		{name: "human"},
	} {
		t.Run("tabs_"+testCase.name, func(t *testing.T) {
			broker := &directCommandBroker{
				candidates: []webmcp.BrowserCandidate{candidate},
				targets:    targets,
				selected:   page,
			}
			args := []string{"tabs", "--browser", string(candidate.ID)}
			if testCase.json {
				args = append(args, "--json")
			}
			result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), args...)
			if result.err != nil {
				t.Fatalf("tabs: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
			}
			if testCase.json {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectTabsData
				decodeDirectData(t, envelope.Data, &data)
				if len(data.Tabs) != 1 || data.Tabs[0].TargetID != string(target.ID) || data.Tabs[0].Type != "page" {
					t.Fatalf("page-only tabs = %+v", data.Tabs)
				}
			} else {
				if !strings.Contains(result.stdout, string(target.ID)) || !strings.Contains(result.stdout, "Tabs:") {
					t.Fatalf("human page-only tabs omitted the page: %q", result.stdout)
				}
				for _, forbidden := range []string{string(uiTarget.ID), uiTarget.Title, string(nonExactPageTarget.ID)} {
					if strings.Contains(result.stdout, forbidden) {
						t.Fatalf("human page-only tabs exposed %q: %q", forbidden, result.stdout)
					}
				}
			}
		})
	}

	selectBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    targets,
		selected:   page,
	}
	selected := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(selectBroker), "select", "--browser", string(candidate.ID), "--auto-select", "single", "--json")
	selectionEnvelope := requireDirectSuccess(t, selected)
	var selectionData WebMCPDirectContext
	decodeDirectData(t, selectionEnvelope.Data, &selectionData)
	if selectionData.TargetID != string(target.ID) {
		t.Fatalf("auto-selected target = %q, want %q", selectionData.TargetID, target.ID)
	}
	if len(selectBroker.selectCalls) != 1 || selectBroker.selectCalls[0].TargetID != target.ID {
		t.Fatalf("auto-selection calls = %+v", selectBroker.selectCalls)
	}

	secondPage := target
	secondPage.ID = "tab-b"
	ambiguousBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{uiTarget, target, secondPage},
	}
	ambiguous := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(ambiguousBroker), "select", "--browser", string(candidate.ID), "--auto-select", "single", "--json")
	if ambiguous.err == nil {
		t.Fatal("auto-selection unexpectedly chose one of two page targets")
	}
	ambiguousEnvelope := decodeDirectEnvelope(t, ambiguous.stdout)
	if ambiguousEnvelope.OK || ambiguousEnvelope.Error == nil || ambiguousEnvelope.Error.Code != string(webmcp.ErrorAmbiguousTab) {
		t.Fatalf("multi-page ambiguity envelope = %+v", ambiguousEnvelope)
	}
	if ids := directSafeIDList(ambiguousEnvelope.Error.Details["candidate_target_ids"]); !reflect.DeepEqual(ids, []string{"tab-a", "tab-b"}) {
		t.Fatalf("multi-page ambiguity candidates = %v", ids)
	}
	if len(ambiguousBroker.selectCalls) != 0 {
		t.Fatalf("ambiguous page selection caused side effects: %+v", ambiguousBroker.selectCalls)
	}
}

func TestWebMCPDirectNoEligibleTabUsesC0DetailsInHumanAndJSONModes(t *testing.T) {
	browserID := randomizedWebMCPTestID(t, "browser-")
	targetID := randomizedWebMCPTestID(t, "target-")
	candidate := webmcp.BrowserCandidate{
		ID:       webmcp.BrowserID(browserID),
		Source:   webmcp.DiscoverySourceExplicit,
		Product:  "Chrome/Test",
		Protocol: "1.3",
		Loopback: true,
	}
	ineligible := webmcp.Target{
		BrowserID:         candidate.ID,
		ID:                webmcp.TargetID(targetID),
		Type:              "page",
		Title:             "Blank page",
		URL:               "about:blank",
		EligibilityReason: "internal_url",
	}

	for _, testCase := range []struct {
		name string
		json bool
	}{
		{name: "json", json: true},
		{name: "human"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			broker := &directCommandBroker{
				candidates: []webmcp.BrowserCandidate{candidate},
				targets:    []webmcp.Target{ineligible},
			}
			args := []string{"select", "--browser", browserID}
			if testCase.json {
				args = append(args, "--json")
			}
			result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), args...)
			if result.err == nil {
				t.Fatal("select unexpectedly succeeded for an ineligible page")
			}
			if len(broker.selectCalls) != 0 {
				t.Fatalf("ineligible page was selected: %+v", broker.selectCalls)
			}

			if testCase.json {
				envelope := decodeDirectEnvelope(t, result.stdout)
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorNoEligibleTab) {
					t.Fatalf("no-eligible envelope = %+v", envelope)
				}
				if envelope.Error.Details["browser_id"] != browserID || envelope.Error.Details["candidate_count"] != float64(1) {
					t.Fatalf("no-eligible details = %#v", envelope.Error.Details)
				}
				filters, ok := envelope.Error.Details["filters"].(map[string]any)
				if !ok || filters["eligible_only"] != true || filters["include_zero_tool_pages"] != true {
					t.Fatalf("effective filters = %#v", envelope.Error.Details["filters"])
				}
			} else if !strings.Contains(result.stdout, "Error: no_eligible_tab") {
				t.Fatalf("human no-eligible output = %q", result.stdout)
			}
			if strings.Contains(result.stdout, "about:blank") || strings.Contains(result.stdout, targetID) {
				t.Fatalf("no-eligible output exposed page data: %q", result.stdout)
			}
			if broker.closeCalls != 1 {
				t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
			}
		})
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

	for _, testCase := range []struct {
		name string
		json bool
	}{
		{name: "json", json: true},
		{name: "human"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			broker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{candidate}, targets: targets}
			args := []string{"select", "--browser", browserID}
			if testCase.json {
				args = append(args, "--json")
			}
			result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), args...)
			if result.err == nil {
				t.Fatal("select unexpectedly chose an ambiguous target")
			}
			if len(broker.selectCalls) != 0 || len(broker.activateCalls) != 0 {
				t.Fatalf("ambiguous selection caused side effects: select=%+v activate=%+v", broker.selectCalls, broker.activateCalls)
			}
			if broker.listTargetCalls != 1 {
				t.Fatalf("target enumeration calls = %d, want one", broker.listTargetCalls)
			}
			if testCase.json {
				envelope := decodeDirectEnvelope(t, result.stdout)
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorAmbiguousTab) {
					t.Fatalf("ambiguous target envelope = %+v", envelope)
				}
				if envelope.Error.Details["browser_id"] != browserID {
					t.Fatalf("ambiguous target browser ID = %#v", envelope.Error.Details["browser_id"])
				}
				ids := directSafeIDList(envelope.Error.Details["candidate_target_ids"])
				if !reflect.DeepEqual(ids, wantIDs) {
					t.Fatalf("ambiguous target IDs = %v, want %v", ids, wantIDs)
				}
				choices, ok := envelope.Error.Details["candidate_choices"].([]any)
				if !ok || len(choices) != len(wantIDs) {
					t.Fatalf("ambiguous target choices = %#v", envelope.Error.Details["candidate_choices"])
				}
				for index, item := range choices {
					choice, ok := item.(map[string]any)
					if !ok || choice["target_id"] != wantIDs[index] || choice["browser_id"] != browserID {
						t.Fatalf("ambiguous target choice %d = %#v", index, item)
					}
				}
				if !strings.Contains(result.stdout, `"action":"ask_customer"`) || !strings.Contains(result.stdout, `"retry_after":"customer_input"`) {
					t.Fatalf("ambiguity recovery missing: %s", result.stdout)
				}
			} else {
				for _, want := range append([]string{"Error: ambiguous_tab", browserID}, wantIDs...) {
					if !strings.Contains(result.stdout, want) {
						t.Fatalf("human ambiguity output omitted %q: %q", want, result.stdout)
					}
				}
				if strings.Contains(result.stdout, ineligibleTargetID) {
					t.Fatalf("human ambiguity output exposed ineligible target: %q", result.stdout)
				}
				if strings.Contains(result.stdout, filteredTargetID) {
					t.Fatalf("human ambiguity output exposed filtered target: %q", result.stdout)
				}
			}
			if strings.Contains(result.stdout, "user:pass") || strings.Contains(result.stdout, "token=secret") || strings.Contains(result.stdout, "/private") {
				t.Fatalf("ambiguity output exposed unsafe page metadata: %q", result.stdout)
			}
			if broker.closeCalls != 1 {
				t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
			}
		})
	}
}

func TestWebMCPDirectAmbiguousBrowserReturnsSortedCandidatesWithoutFallback(t *testing.T) {
	firstBrowserID := randomizedWebMCPTestID(t, "browser-")
	secondBrowserID := randomizedWebMCPTestID(t, "browser-")
	first := webmcp.BrowserCandidate{ID: webmcp.BrowserID(firstBrowserID), Source: webmcp.DiscoverySourceExplicit, Product: "Chrome/Test", Protocol: "1.3", Loopback: true}
	second := webmcp.BrowserCandidate{ID: webmcp.BrowserID(secondBrowserID), Source: webmcp.DiscoverySourceExplicit, Product: "Chrome/Test", Protocol: "1.3", Loopback: true}
	wantIDs := []string{firstBrowserID, secondBrowserID}
	sort.Strings(wantIDs)

	for _, testCase := range []struct {
		name string
		json bool
	}{
		{name: "json", json: true},
		{name: "human"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			broker := &directCommandBroker{candidates: []webmcp.BrowserCandidate{second, first, first}}
			args := []string{"select"}
			if testCase.json {
				args = append(args, "--json")
			}
			result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), args...)
			if result.err == nil {
				t.Fatal("select unexpectedly chose an ambiguous browser")
			}
			if len(broker.selectCalls) != 0 || len(broker.activateCalls) != 0 {
				t.Fatalf("ambiguous browser caused selection side effects: select=%+v activate=%+v", broker.selectCalls, broker.activateCalls)
			}
			if broker.listTargetCalls != 0 {
				t.Fatalf("ambiguous browser listed targets before exact selection: %d calls", broker.listTargetCalls)
			}
			if testCase.json {
				envelope := decodeDirectEnvelope(t, result.stdout)
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorAmbiguousBrowser) {
					t.Fatalf("ambiguous browser envelope = %+v", envelope)
				}
				ids := directSafeIDList(envelope.Error.Details["candidate_browser_ids"])
				if !reflect.DeepEqual(ids, wantIDs) {
					t.Fatalf("ambiguous browser IDs = %v, want %v", ids, wantIDs)
				}
			} else {
				if !strings.Contains(result.stdout, "Error: ambiguous_browser") {
					t.Fatalf("human ambiguity output = %q", result.stdout)
				}
				for _, want := range wantIDs {
					if !strings.Contains(result.stdout, want) {
						t.Fatalf("human ambiguity output omitted %q: %q", want, result.stdout)
					}
				}
			}
			if broker.closeCalls != 1 {
				t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
			}
		})
	}
}

func randomizedWebMCPTestID(t *testing.T, prefix string) string {
	t.Helper()
	value := make([]byte, 6)
	if _, err := cryptorand.Read(value); err != nil {
		t.Fatalf("randomize WebMCP test ID: %v", err)
	}
	return prefix + hex.EncodeToString(value)
}

func randomizedWebMCPInstanceID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 12)
	if _, err := cryptorand.Read(value); err != nil {
		t.Fatalf("randomize WebMCP instance ID: %v", err)
	}
	return "incarnation-" + hex.EncodeToString(value)
}

func TestWebMCPDirectOperationsUseBrokerIDsRefsAndInvocations(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, tool := directFixture()
	newBroker := func() *directCommandBroker {
		return &directCommandBroker{
			candidates: []webmcp.BrowserCandidate{candidate},
			targets:    []webmcp.Target{target},
			selected:   page,
			catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
			invokeResult: webmcp.InvokeResult{
				InvocationID: "inv-23",
				State:        webmcp.InvocationCompleted,
				Output:       json.RawMessage(`{"ok":true}`),
			},
		}
	}

	tests := []struct {
		name  string
		args  []string
		check func(*testing.T, directCommandResult, *directCommandBroker)
	}{
		{
			name: "browsers",
			args: []string{"browsers", "--json"},
			check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectBrowsersData
				decodeDirectData(t, envelope.Data, &data)
				if len(data.Browsers) != 1 || data.Browsers[0].ID != "browser-a" || strings.Contains(result.stdout, "secret") {
					t.Fatalf("browsers result = %+v output=%s", data, result.stdout)
				}
			},
		},
		{
			name: "tabs",
			args: []string{"tabs", "--browser", "browser-a", "--eligible", "--json"},
			check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectTabsData
				decodeDirectData(t, envelope.Data, &data)
				if len(data.Tabs) != 1 || data.Tabs[0].TargetID != "tab-a" || data.Tabs[0].Origin != "https://fixture.test" {
					t.Fatalf("tabs result = %+v", data)
				}
			},
		},
		{
			name: "activate",
			args: []string{"activate", "--browser", "browser-a", "--tab", "tab-a", "--json"},
			check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
				requireDirectSuccess(t, result)
				if len(broker.activateCalls) != 1 || broker.activateCalls[0].TargetID != "tab-a" {
					t.Fatalf("activate calls = %+v", broker.activateCalls)
				}
			},
		},
		{
			name: "context",
			args: []string{"context", "--browser", "browser-a", "--tab", "tab-a", "--json"},
			check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectContext
				decodeDirectData(t, envelope.Data, &data)
				if data.Generation != 7 || data.CatalogGeneration != 7 || data.ToolCount != 1 || data.URL != "https://fixture.test/page" {
					t.Fatalf("context result = %+v", data)
				}
			},
		},
		{
			name: "tools",
			args: []string{"tools", "--browser", "browser-a", "--tab", "tab-a", "--json"},
			check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectToolsData
				decodeDirectData(t, envelope.Data, &data)
				if len(data.Tools) != 1 || data.Tools[0].Ref != string(tool.Ref) || data.Tools[0].Generation != 7 {
					t.Fatalf("tools result = %+v", data)
				}
			},
		},
		{
			name: "invoke",
			args: []string{"invoke", "--browser", "browser-a", "--tab", "tab-a", "--tool-ref", string(tool.Ref), "--input-json", `{"value":1}`, "--reason", "test reason", "--json"},
			check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectInvocation
				decodeDirectData(t, envelope.Data, &data)
				if data.InvocationID != "inv-23" || data.ToolRef != string(tool.Ref) || data.Status != string(webmcp.InvocationCompleted) {
					t.Fatalf("invoke result = %+v", data)
				}
				if broker.invokeRequest.ToolRef != tool.Ref || string(broker.invokeRequest.Input) != `{"value":1}` || broker.invokeRequest.Reason != "test reason" {
					t.Fatalf("invoke request = %+v", broker.invokeRequest)
				}
			},
		},
		{
			name: "cancel",
			args: []string{"cancel", "inv-23", "--browser", "browser-a", "--tab", "tab-a", "--json"},
			check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectCancelData
				decodeDirectData(t, envelope.Data, &data)
				if data.InvocationID != "inv-23" || broker.cancelRequest.InvocationID != "inv-23" {
					t.Fatalf("cancel result/request = %+v/%+v", data, broker.cancelRequest)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := newBroker()
			result := executeDirectCommand(t, configDir, store, directFactory(broker), test.args...)
			test.check(t, result, broker)
			if test.name == "invoke" {
				var receipt WebMCPDirectInvocationReceipt
				decoder := json.NewDecoder(strings.NewReader(result.stderr))
				if err := decoder.Decode(&receipt); err != nil {
					t.Fatalf("decode dispatch receipt: %v; stderr=%q", err, result.stderr)
				}
				if receipt.Version != webmcpDirectInvocationReceiptVersion || receipt.InvocationID != "inv-23" || receipt.ToolRef != string(tool.Ref) || receipt.State != string(webmcp.InvocationDispatched) {
					t.Fatalf("dispatch receipt = %+v", receipt)
				}
				var extra any
				if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
					t.Fatalf("dispatch stderr contains more than one receipt: err=%v extra=%#v", err, extra)
				}
			} else if result.stderr != "" {
				t.Fatalf("stderr = %q", result.stderr)
			}
		})
	}
}

func TestWebMCPDirectInvokeReceiptUsesBrowserIDAndOnlyHandoffFields(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	page, target, candidate, tool := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
		invokeResult: webmcp.InvokeResult{
			InvocationID:        "broker-invocation-1",
			BrowserInvocationID: "browser-invocation-9",
			State:               webmcp.InvocationCompleted,
			Output:              json.RawMessage(`{"page_output":"do-not-put-in-receipt"}`),
		},
	}

	result := executeDirectCommand(t, configDir, NewFileWebMCPSelectionStore(configDir), directFactory(broker),
		"invoke", "--browser", "browser-a", "--tab", "tab-a", "--tool-ref", string(tool.Ref),
		"--input-json", `{"input_secret":"do-not-put-in-receipt"}`, "--json")
	if result.err != nil {
		t.Fatalf("invoke: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
	}
	if len(result.stderr) > webmcpDirectInvocationReceiptMaxBytes {
		t.Fatalf("dispatch receipt is %d bytes, want <= %d: %q", len(result.stderr), webmcpDirectInvocationReceiptMaxBytes, result.stderr)
	}
	decoder := json.NewDecoder(strings.NewReader(result.stderr))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		t.Fatalf("decode dispatch receipt: %v; stderr=%q", err, result.stderr)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("dispatch receipt has more than one JSON value: err=%v extra=%#v", err, extra)
	}
	wantFields := map[string]struct{}{"version": {}, "invocation_id": {}, "tool_ref": {}, "state": {}}
	if len(fields) != len(wantFields) {
		t.Fatalf("dispatch receipt fields = %#v, want exactly %#v", fields, wantFields)
	}
	for field := range wantFields {
		if _, ok := fields[field]; !ok {
			t.Fatalf("dispatch receipt omitted %q: %#v", field, fields)
		}
	}
	var receipt WebMCPDirectInvocationReceipt
	if err := json.Unmarshal([]byte(result.stderr), &receipt); err != nil {
		t.Fatalf("decode typed dispatch receipt: %v", err)
	}
	if receipt.Version != webmcpDirectInvocationReceiptVersion || receipt.InvocationID != "browser-invocation-9" || receipt.ToolRef != string(tool.Ref) || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("dispatch receipt = %+v", receipt)
	}
	for _, secret := range []string{"broker-invocation-1", "input_secret", "do-not-put-in-receipt", "page_output", "127.0.0.1", "password", "fragment"} {
		if strings.Contains(result.stderr, secret) {
			t.Fatalf("dispatch receipt exposed %q: %q", secret, result.stderr)
		}
	}
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectInvocation
	decodeDirectData(t, envelope.Data, &data)
	if data.InvocationID != "browser-invocation-9" {
		t.Fatalf("final invocation ID = %q, want browser protocol ID", data.InvocationID)
	}
}

func TestWebMCPDirectHumanCancellationReportsIDAndUnknownSideEffect(t *testing.T) {
	var output bytes.Buffer
	err := writeWebMCPDirectHuman(&output, "invoke", nil, webmcp.NewClassifiedError(webmcp.ErrorInvocationCanceled, webmcp.DefaultErrorMessage(webmcp.ErrorInvocationCanceled), map[string]any{
		"invocation_id":       "browser-invocation-9",
		"cancel_source":       "interrupt",
		"side_effect_unknown": true,
	}), webmcp.ErrorInvocationFailed)
	if err != nil {
		t.Fatalf("human cancellation output: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"Error: invocation_canceled",
		"invocation_id=browser-invocation-9",
		"cancel_source=interrupt",
		"side_effect_unknown=true",
		"rollback and retry safety are unknown",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("human cancellation output omitted %q: %q", want, got)
		}
	}
}

func TestWebMCPDirectInterruptBeforeDispatchDoesNotFabricateInvocationID(t *testing.T) {
	result := webmcp.ResultErrorFor(directInvocationCanceledBeforeDispatch("webmcp.tool-ref.v1:fixture-ref"), webmcp.ErrorInvocationFailed, nil)
	if result.Code != string(webmcp.ErrorInvocationCanceled) || result.Retryable {
		t.Fatalf("pre-dispatch cancellation result = %+v", result)
	}
	if _, ok := result.Details["invocation_id"]; ok {
		t.Fatalf("pre-dispatch cancellation fabricated an invocation ID: %#v", result.Details)
	}
	if result.Details["cancel_source"] != "interrupt" || result.Details["phase"] != "before_dispatch" {
		t.Fatalf("pre-dispatch cancellation details = %#v", result.Details)
	}
}

func TestWebMCPDirectInterruptCleanupContextIsIndependent(t *testing.T) {
	status := boundedInterruptCancellationStatus(func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	})
	if status != "requested" {
		t.Fatalf("interrupt cleanup status = %q, want requested", status)
	}
}

func TestWebMCPDirectInvokeSIGINTChildProcess(t *testing.T) {
	if os.Getenv("WEBMCP_DIRECT_SIGINT_CHILD") == "1" {
		runWebMCPDirectInvokeSIGINTChild(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestWebMCPDirectInvokeSIGINTChildProcess$", "-test.v=false")
	command.Env = append(os.Environ(), "WEBMCP_DIRECT_SIGINT_CHILD=1")
	stdout := &childProcessOutputBuffer{}
	stderr := newChildProcessStderrBuffer()
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start SIGINT child: %v", err)
	}
	childAlive := true
	defer func() {
		if childAlive {
			_ = command.Process.Kill()
		}
	}()

	var firstValue string
	select {
	case firstValue = <-stderr.firstLine:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT child did not emit a dispatch receipt")
	}
	var receipt WebMCPDirectInvocationReceipt
	if err := json.Unmarshal([]byte(firstValue), &receipt); err != nil {
		t.Fatalf("decode child dispatch receipt: %v; stderr=%q", err, firstValue)
	}
	if receipt.InvocationID != "browser-child-1" || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("child dispatch receipt = %+v", receipt)
	}

	signalAt := time.Now()
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send SIGINT to child: %v", err)
	}
	exitErr := command.Wait()
	childAlive = false
	if exitErr == nil || command.ProcessState.ExitCode() == 0 {
		t.Fatalf("SIGINT child exited successfully: err=%v exit=%d", exitErr, command.ProcessState.ExitCode())
	}
	if elapsed := time.Since(signalAt); elapsed > webmcpDirectInterruptReconciliationTimeout+time.Second {
		t.Fatalf("SIGINT child completion took %s, want <= %s", elapsed, webmcpDirectInterruptReconciliationTimeout+time.Second)
	}

	stderrValue := stderr.String()
	stdoutValue := stdout.String()
	envelope := decodeDirectEnvelope(t, stdoutValue)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationCanceled) || envelope.Error.Retryable {
		t.Fatalf("SIGINT child envelope = %+v", envelope)
	}
	if envelope.Error.Details["invocation_id"] != "browser-child-1" || envelope.Error.Details["cancel_source"] != "interrupt" || envelope.Error.Details["side_effect_unknown"] != true {
		t.Fatalf("SIGINT child cancellation details = %#v", envelope.Error.Details)
	}
	if strings.Contains(stdoutValue, "input_secret") || strings.Contains(stdoutValue, "page_output") || strings.Contains(stdoutValue, "credential") {
		t.Fatalf("SIGINT child output leaked sensitive data: %q", stdoutValue)
	}
	if strings.TrimSpace(strings.TrimPrefix(stderrValue, firstValue)) != "" {
		t.Fatalf("SIGINT child wrote unexpected stderr after receipt: %q", stderrValue)
	}
}

type childProcessOutputBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *childProcessOutputBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(value)
}

func (b *childProcessOutputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type childProcessStderrBuffer struct {
	childProcessOutputBuffer
	firstLine chan string
	notified  bool
}

func newChildProcessStderrBuffer() *childProcessStderrBuffer {
	return &childProcessStderrBuffer{firstLine: make(chan string, 1)}
}

func (b *childProcessStderrBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	written, err := b.data.Write(value)
	if !b.notified {
		if newline := bytes.IndexByte(b.data.Bytes(), '\n'); newline >= 0 {
			b.notified = true
			line := append([]byte(nil), b.data.Bytes()[:newline+1]...)
			b.mu.Unlock()
			b.firstLine <- string(line)
			return written, err
		}
	}
	b.mu.Unlock()
	return written, err
}

func runWebMCPDirectInvokeSIGINTChild(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, tool := directFixture()
	broker := &sigintChildBroker{directCommandBroker: &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
		invokeResult: webmcp.InvokeResult{
			InvocationID:        "broker-child-1",
			BrowserInvocationID: "browser-child-1",
			State:               webmcp.InvocationDispatched,
		},
	}}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	operations := NewWebMCPOperationsCommand(globalFlags, directFactory(broker))
	operations.SelectionStore = store
	root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
	operations.AddCommands(root)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.SetArgs([]string{"invoke", "--browser", string(candidate.ID), "--tab", string(target.ID), "--tool-ref", string(tool.Ref), "--input-json", `{"input_secret":"do-not-echo"}`, "--json"})
	if err := root.Execute(); err == nil {
		os.Exit(43)
	}
	if broker.cancelRequest.InvocationID != "broker-child-1" {
		os.Exit(44)
	}
	if broker.cancelContextErr != nil {
		os.Exit(45)
	}
	os.Exit(42)
}

func TestWebMCPDirectCancelRehydratesExactSelectionWithoutLocalRegistry(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	if err := store.Save(WebMCPSelection{
		Version:          WebMCPSelectionVersion,
		EndpointID:       string(candidate.ID),
		BrowserID:        string(candidate.ID),
		TargetID:         string(target.ID),
		Origin:           target.Origin,
		ContinuityMarker: target.ContinuityMarker,
		Generation:       page.Generation,
	}); err != nil {
		t.Fatalf("seed persisted selection: %v", err)
	}
	base := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	broker := &directCancelCommandBroker{directCommandBroker: base}

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "cancel", "--invocation", "browser-invocation-9", "--json")
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectCancelData
	decodeDirectData(t, envelope.Data, &data)
	if data.InvocationID != "browser-invocation-9" || data.Status != "canceled" || data.Phase != "terminal" || data.Outcome != "confirmed_canceled" {
		t.Fatalf("cancel data = %+v", data)
	}
	if got := broker.directCancelRequest; got.Target != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) || got.InvocationID != "browser-invocation-9" {
		t.Fatalf("direct cancel request = %+v", got)
	}
	if base.cancelRequest.InvocationID != "" {
		t.Fatalf("fresh direct cancel consulted local broker registry: %+v", base.cancelRequest)
	}
	if len(base.selectCalls) != 1 || base.selectCalls[0] != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) {
		t.Fatalf("exact selection calls = %+v", base.selectCalls)
	}
}

func TestWebMCPDirectCancelRejectsConvenientFallbackTarget(t *testing.T) {
	configDir := writeDirectConfig(t, "  selection:\n    auto_select: single\n")
	page, target, candidate, _ := directFixture()
	base := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	broker := &directCancelCommandBroker{directCommandBroker: base}

	result := executeDirectCommand(t, configDir, nil, directFactory(broker), "cancel", "--browser", "browser-a", "--invocation", "browser-invocation-9", "--json")
	if result.err == nil {
		t.Fatal("cancel unexpectedly selected a convenient fallback target")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("fallback cancellation envelope = %+v", envelope)
	}
	if len(base.selectCalls) != 0 || broker.directCancelRequest.InvocationID != "" {
		t.Fatalf("fallback cancellation touched target/cancel path: selections=%+v request=%+v", base.selectCalls, broker.directCancelRequest)
	}
}

func TestWebMCPDirectCancelClassifiesBrowserRejection(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	if err := store.Save(WebMCPSelection{
		Version:    WebMCPSelectionVersion,
		EndpointID: string(candidate.ID),
		BrowserID:  string(candidate.ID),
		TargetID:   string(target.ID),
		Origin:     target.Origin,
	}); err != nil {
		t.Fatalf("seed persisted selection: %v", err)
	}
	base := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	broker := &directCancelCommandBroker{
		directCommandBroker: base,
		directCancelErr:     errors.New("browser response leaked credential=secret"),
	}

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "cancel", "--invocation", "browser-invocation-9", "--json")
	if result.err == nil {
		t.Fatal("browser rejection unexpectedly succeeded")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationFailed) {
		t.Fatalf("browser rejection envelope = %+v", envelope)
	}
	if strings.Contains(result.stdout, "credential=secret") || strings.Contains(result.stderr, "credential=secret") {
		t.Fatalf("browser rejection leaked raw error: stdout=%q stderr=%q", result.stdout, result.stderr)
	}
}

func TestWebMCPDirectCancelWritesBoundedTerminalOutcome(t *testing.T) {
	tests := []struct {
		name    string
		message string
		details map[string]any
	}{
		{
			name:    `completed_anyway`,
			message: `the browser invocation completed despite the cancellation request`,
			details: map[string]any{
				`browser_id`:          `browser-a`,
				`target_id`:           `tab-a`,
				`invocation_id`:       `browser-invocation-9`,
				`phase`:               `cancel`,
				`cancel_phase`:        `cancel_dispatched`,
				`outcome`:             `completed_anyway`,
				`terminal_observed`:   true,
				`side_effect_unknown`: true,
				`terminal_event`:      `tool_responded`,
			},
		},
		{
			name:    `cancellation_unconfirmed`,
			message: `the browser did not provide a correlated terminal cancellation result`,
			details: map[string]any{
				`browser_id`:          `browser-a`,
				`target_id`:           `tab-a`,
				`invocation_id`:       `browser-invocation-9`,
				`phase`:               `cancel`,
				`cancel_phase`:        `cancel_dispatched`,
				`outcome`:             `cancellation_unconfirmed`,
				`terminal_observed`:   false,
				`side_effect_unknown`: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := writeDirectConfig(t, ``)
			store := NewFileWebMCPSelectionStore(configDir)
			page, target, candidate, _ := directFixture()
			if err := store.Save(WebMCPSelection{
				Version:    WebMCPSelectionVersion,
				EndpointID: string(candidate.ID),
				BrowserID:  string(candidate.ID),
				TargetID:   string(target.ID),
				Origin:     target.Origin,
			}); err != nil {
				t.Fatalf(`seed persisted selection: %v`, err)
			}
			base := &directCommandBroker{
				candidates: []webmcp.BrowserCandidate{candidate},
				targets:    []webmcp.Target{target},
				selected:   page,
			}
			broker := &directCancelCommandBroker{
				directCommandBroker: base,
				directCancelErr: webmcp.NewClassifiedError(
					webmcp.ErrorInvocationFailed,
					test.message,
					test.details,
				),
			}

			result := executeDirectCommand(t, configDir, store, directFactory(broker), `cancel`, `--invocation`, `browser-invocation-9`, `--json`)
			if result.err == nil {
				t.Fatal(`bounded terminal failure unexpectedly succeeded`)
			}
			envelope := decodeDirectEnvelope(t, result.stdout)
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationFailed) {
				t.Fatalf(`bounded terminal failure envelope = %+v`, envelope)
			}
			if envelope.Error.Message != test.message {
				t.Fatalf(`bounded terminal failure message = %q, want %q`, envelope.Error.Message, test.message)
			}
			if envelope.Error.Details[`outcome`] != test.details[`outcome`] ||
				envelope.Error.Details[`terminal_observed`] != test.details[`terminal_observed`] ||
				envelope.Error.Details[`side_effect_unknown`] != true {
				t.Fatalf(`bounded terminal failure details = %#v`, envelope.Error.Details)
			}
			if strings.Contains(result.stdout, `page-output`) || strings.Contains(result.stdout, `page-error-output`) {
				t.Fatalf(`bounded terminal failure exposed page output: %q`, result.stdout)
			}
		})
	}
}

func TestWebMCPDirectHumanOutputIsStableAndRedacted(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	page, target, candidate, tool := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
	}
	result := executeDirectCommand(t, configDir, NewFileWebMCPSelectionStore(configDir), directFactory(broker), "browsers")
	if result.err != nil {
		t.Fatalf("browsers: %v", result.err)
	}
	want := "Browsers:\n  browser-a  Chrome/Test  source=explicit scope=loopback endpoint=http://127.0.0.1:9222/json/version\n"
	if result.stdout != want {
		t.Fatalf("human output = %q, want %q", result.stdout, want)
	}
	if strings.Contains(result.stdout, "secret") || strings.Contains(result.stdout, "token=") {
		t.Fatalf("human output exposed endpoint secret: %q", result.stdout)
	}
}

func TestWebMCPDirectWatchReportsTerminationAndCancellation(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	closedBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		watch:      closedEventChannel(),
	}
	ended := executeDirectCommand(t, configDir, store, directFactory(closedBroker), "watch", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	envelope := requireDirectSuccess(t, ended)
	var endedData WebMCPDirectWatchData
	decodeDirectData(t, envelope.Data, &endedData)
	if endedData.Status != webmcpDirectWatchStatusEnded || len(endedData.Events) != 0 {
		t.Fatalf("terminated watch = %+v", endedData)
	}

	blockedBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		watch:      make(chan webmcp.BrokerEvent),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := executeDirectCommandContext(t, ctx, configDir, store, directFactory(blockedBroker), "watch", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	if canceled.err != nil {
		t.Fatalf("canceled watch: %v", canceled.err)
	}
	envelope = decodeDirectEnvelope(t, canceled.stdout)
	var canceledData WebMCPDirectWatchData
	decodeDirectData(t, envelope.Data, &canceledData)
	if canceledData.Status != webmcpDirectWatchStatusCanceled {
		t.Fatalf("canceled watch = %+v", canceledData)
	}
}

func TestWebMCPDirectDefaultRuntimeReturnsClassifiedDiscoveryError(t *testing.T) {
	configDir := t.TempDir()
	store := NewFileWebMCPSelectionStore(configDir)
	result := executeDirectCommand(t, configDir, store, nil, "browsers", "--json")
	if result.err == nil {
		t.Fatal("default operation unexpectedly succeeded without a browser endpoint")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorEndpointNotFound) {
		t.Fatalf("default envelope = %+v, want endpoint_not_found", envelope)
	}
	if strings.Contains(result.stdout, "Lane B") || strings.Contains(result.stdout, "Lane D") {
		t.Fatalf("default operation output exposed internal implementation names: %s", result.stdout)
	}
}

func TestWebMCPDirectWatchReportsBoundedFailure(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	stream := make(chan webmcp.BrokerEvent, 1)
	stream <- webmcp.BrokerEvent{
		Version:   webmcp.BrowserEventsVersion,
		Type:      webmcp.BrokerEventSessionClosed,
		Sequence:  2,
		BrowserID: candidate.ID,
		TargetID:  target.ID,
		Reason:    webmcp.BrokerWatchBufferFullReason,
	}
	close(stream)
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		watch:      stream,
	}

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "watch", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	if result.err != nil {
		t.Fatalf("bounded watch failure: %v", result.err)
	}
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectWatchData
	decodeDirectData(t, envelope.Data, &data)
	if data.Status != webmcpDirectWatchStatusFailed || len(data.Events) != 1 || data.Events[0].Type != string(webmcp.BrokerEventSessionClosed) || data.Events[0].Reason != webmcp.BrokerWatchBufferFullReason {
		t.Fatalf("bounded watch result = %+v, want explicit failed status", data)
	}
}

func TestWebMCPDirectToolsWatchSubscribesBeforeSelection(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	stream := make(chan webmcp.BrokerEvent, 2)
	broker := &selectionOrderingWatchBroker{
		directCommandBroker: &directCommandBroker{
			candidates: []webmcp.BrowserCandidate{candidate},
			targets:    []webmcp.Target{target},
			selected:   page,
		},
		stream: stream,
	}

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "tools", "--browser", string(candidate.ID), "--tab", string(target.ID), "--watch", "--json")
	if result.err != nil {
		t.Fatalf("tools --watch: %v\nstdout=%s", result.err, result.stdout)
	}
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectWatchData
	decodeDirectData(t, envelope.Data, &data)
	if data.Status != webmcpDirectWatchStatusEnded || len(data.Events) != 2 || data.Events[0].Type != string(webmcp.BrokerEventSelected) || data.Events[1].Type != string(webmcp.BrokerEventCatalogChanged) {
		t.Fatalf("tools --watch result = %+v, want selection and initial catalog events", data)
	}
}

func TestWebMCPDirectPreservesExternallyOwnedTarget(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, target, candidate, tool := directFixture()
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(target, testkit.WithInitialCatalog(tool)),
	))
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
		Ownership:  webmcp.TargetOwnershipExternal,
	})
	result := executeDirectCommand(t, configDir, store, directFactory(broker), "select", "--browser", "browser-a", "--tab", "tab-a", "--json")
	if result.err != nil {
		t.Fatalf("select through real broker: %v\nstdout=%s", result.err, result.stdout)
	}
	ops := runtime.Operations()
	if !hasTestkitOperation(ops, testkit.OperationDetach) {
		t.Fatalf("external target was not detached: %+v", ops)
	}
	if hasTestkitOperation(ops, testkit.OperationCloseTarget) {
		t.Fatalf("external target was closed: %+v", ops)
	}
}

func TestWebMCPDirectActivateClassifiesLiveOperationFailure(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	_, target, candidate, _ := directFixture()
	runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate:     candidate,
		ActivateError: errors.New("foreground activation rejected by headless Chrome"),
		Targets: []testkit.TargetConfig{
			testkit.NewTargetConfig(target),
		},
	})
	browser := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
	})

	result := executeDirectCommand(t, configDir, nil, directFactory(browser), "activate", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	if result.err == nil {
		t.Fatal("activate unexpectedly succeeded")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorTargetAttachFailed) {
		t.Fatalf("live activation failure envelope = %+v, error = %v", envelope, result.err)
	}
	if envelope.Error.Details["browser_id"] != string(candidate.ID) || envelope.Error.Details["target_id"] != string(target.ID) || envelope.Error.Details["phase"] != "activate" {
		t.Fatalf("live activation failure details = %#v, want exact activation identity", envelope.Error.Details)
	}
	if _, exists := envelope.Error.Details["reconnect_required"]; exists {
		t.Fatalf("live activation failure requested reconnect: %#v", envelope.Error.Details)
	}
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationAttach || operation.Kind == testkit.OperationEnableWebMCP || operation.Kind == testkit.OperationEnableAcknowledged {
			t.Fatalf("activation-only command initialized WebMCP: %#v", runtime.Operations())
		}
	}
}

func TestWebMCPDirectClassifiesBrokerFailures(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, target, candidate, tool := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   webmcp.PageContext{Key: webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID}},
		catalog:    webmcp.ToolCatalogSnapshot{Context: webmcp.PageContext{Key: webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID}}, Tools: []webmcp.ToolDescriptor{tool}},
		invokeErr:  webmcp.NewClassifiedError(webmcp.ErrorStaleToolRef, "tool ref is stale", map[string]any{"tool_ref": string(tool.Ref)}),
	}
	result := executeDirectCommand(t, configDir, store, directFactory(broker), "invoke", "--browser", "browser-a", "--tab", "tab-a", "--tool-ref", string(tool.Ref), "--input-json", `{}`, "--json")
	if result.err == nil {
		t.Fatal("stale invocation unexpectedly succeeded")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleToolRef) {
		t.Fatalf("stale invocation envelope = %+v", envelope)
	}
}

func TestWebMCPDirectClassifiesPersistedBrowserLossAsDisconnected(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	selected := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	if result := executeDirectCommand(t, configDir, store, directFactory(selected), "select", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json"); result.err != nil {
		t.Fatalf("seed persisted selection: %v\nstdout=%s", result.err, result.stdout)
	}

	lost := &directCommandBroker{
		discoverErr: webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "browser endpoint could not be reached", map[string]any{
			"phase": "discovery",
		}),
	}
	result := executeDirectCommand(t, configDir, store, directFactory(lost), "context", "--json")
	if result.err == nil {
		t.Fatal("context unexpectedly succeeded after the persisted browser disappeared")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("disconnected context envelope = %+v", envelope)
	}
	if envelope.Error.Details["browser_id"] != string(candidate.ID) || envelope.Error.Details["target_id"] != string(target.ID) || envelope.Error.Details["phase"] != "discovery" || envelope.Error.Details["reconnect_required"] != true {
		t.Fatalf("disconnected context details = %#v", envelope.Error.Details)
	}
}

func TestWebMCPDirectRetainedSelectionDistinguishesFreshReplacementFromEndpointLoss(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, oldCandidate, _ := directFixture()
	oldCandidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	selected := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{oldCandidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	seed := executeDirectCommand(t, configDir, store, directFactory(selected), "select", "--browser", string(oldCandidate.ID), "--tab", string(target.ID), "--json")
	if seed.err != nil {
		t.Fatalf("seed persisted selection: %v\nstdout=%s", seed.err, seed.stdout)
	}
	oldRecord, err := store.Load()
	if err != nil {
		t.Fatalf("load old selection: %v", err)
	}
	if oldRecord.BrowserInstanceID != oldCandidate.BrowserInstanceID || oldRecord.Generation != page.Generation {
		t.Fatalf("persisted identity = %+v, want instance=%q generation=%d", oldRecord, oldCandidate.BrowserInstanceID, page.Generation)
	}

	replacement := oldCandidate
	replacement.ID = webmcp.BrowserID(randomizedWebMCPTestID(t, "browser-"))
	replacement.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	replacementTarget := target
	replacementTarget.BrowserID = replacement.ID
	replacementBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{replacement},
		targets:    []webmcp.Target{replacementTarget},
	}
	replaced := executeDirectCommand(t, configDir, store, directFactory(replacementBroker), "context", "--json")
	if replaced.err == nil {
		t.Fatal("context unexpectedly selected a reachable fresh browser replacement")
	}
	replacedEnvelope := decodeDirectEnvelope(t, replaced.stdout)
	if replacedEnvelope.OK || replacedEnvelope.Error == nil || replacedEnvelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("replacement envelope = %+v", replacedEnvelope)
	}
	if details := replacedEnvelope.Error.Details; details["browser_id"] != oldRecord.BrowserID || details["target_id"] != oldRecord.TargetID || details["selected_generation"] != float64(oldRecord.Generation) || details["reason"] != "browser_instance_changed" {
		t.Fatalf("replacement details = %#v", details)
	}
	if len(replacementBroker.selectCalls) != 0 || len(replacementBroker.activateCalls) != 0 {
		t.Fatalf("replacement received selection work: select=%+v activate=%+v", replacementBroker.selectCalls, replacementBroker.activateCalls)
	}
	if oldAfterReplacement, loadErr := store.Load(); loadErr != nil || oldAfterReplacement != oldRecord {
		t.Fatalf("replacement changed persisted selection: before=%+v after=%+v err=%v", oldRecord, oldAfterReplacement, loadErr)
	}

	lostBroker := &directCommandBroker{
		discoverErr: webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "browser endpoint could not be reached", map[string]any{
			"phase": "discovery",
		}),
	}
	lost := executeDirectCommand(t, configDir, store, directFactory(lostBroker), "context", "--json")
	if lost.err == nil {
		t.Fatal("context unexpectedly succeeded after endpoint loss")
	}
	lostEnvelope := decodeDirectEnvelope(t, lost.stdout)
	if lostEnvelope.OK || lostEnvelope.Error == nil || lostEnvelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("lost endpoint envelope = %+v", lostEnvelope)
	}
	if details := lostEnvelope.Error.Details; details["browser_id"] != oldRecord.BrowserID || details["target_id"] != oldRecord.TargetID || details["phase"] != "discovery" || details["reconnect_required"] != true {
		t.Fatalf("lost endpoint details = %#v", details)
	}
}

func TestWebMCPDirectBrowserAndTabListingsRemainBoundedOnChurn(t *testing.T) {
	t.Run("browsers discovery timeout", func(t *testing.T) {
		configDir := writeDirectConfig(t, "")
		broker := &blockingDirectDiscoveryBroker{directCommandBroker: &directCommandBroker{}}
		started := time.Now()
		result := executeDirectCommand(t, configDir, nil, directFactory(broker), "browsers", "--command-timeout", "40ms", "--json")
		if result.err == nil {
			t.Fatal("browsers unexpectedly succeeded after discovery timeout")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("browsers discovery took %s after its 40ms deadline", elapsed)
		}
		envelope := decodeDirectEnvelope(t, result.stdout)
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationTimedOut) {
			t.Fatalf("browsers timeout envelope = %+v", envelope)
		}
	})

	t.Run("tabs browser disconnect", func(t *testing.T) {
		configDir := writeDirectConfig(t, "")
		_, target, candidate, _ := directFixture()
		runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
			Candidate: candidate,
			Targets:   []testkit.TargetConfig{testkit.NewTargetConfig(target)},
		})
		defer func() { _ = runtime.Close() }()
		handle := runtime.Browser(candidate.ID)
		if handle == nil {
			t.Fatal("scripted browser handle is nil")
		}
		handle.BlockOpen()
		broker := webmcp.NewBroker(webmcp.BrokerOptions{
			Runtime:    runtime,
			Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
		})
		resultDone := make(chan directCommandResult, 1)
		started := time.Now()
		go func() {
			resultDone <- executeDirectCommand(t, configDir, nil, directFactory(broker),
				"tabs", "--browser", string(candidate.ID), "--command-timeout", "250ms", "--json")
		}()
		waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
		_, waitErr := runtime.WaitForOperationAdmitted(waitCtx, testkit.OperationOpen)
		cancelWait()
		if waitErr != nil {
			t.Fatalf("wait for tabs open admission: %v", waitErr)
		}
		if err := runtime.Disconnect(candidate.ID, "transport_lost"); err != nil {
			var classified *webmcp.ClassifiedError
			if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserDisconnected {
				t.Fatalf("disconnect: %v", err)
			}
		}
		var result directCommandResult
		select {
		case result = <-resultDone:
		case <-time.After(2 * time.Second):
			t.Fatal("tabs remained blocked after browser disconnect")
		}
		if result.err == nil {
			t.Fatalf("tabs unexpectedly succeeded after browser death: %s", result.stdout)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("tabs took %s after browser death", elapsed)
		}
		envelope := decodeDirectEnvelope(t, result.stdout)
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
			t.Fatalf("tabs disconnect envelope = %+v", envelope)
		}
		if envelope.Error.Details["browser_id"] != string(candidate.ID) {
			t.Fatalf("tabs disconnect browser_id = %#v", envelope.Error.Details["browser_id"])
		}
	})
}

func TestWebMCPDirectMalformedInputReturnsSelectedSchema(t *testing.T) {
	schema := `{"type":"object","properties":{"profile":{"type":"object","properties":{"count":{"type":"integer","minimum":1},"mode":{"enum":["fast","safe"]}},"required":["count"],"additionalProperties":false},"tags":{"type":"array","items":{"type":"string"}}},"required":["profile","tags"],"additionalProperties":false}`
	const toolRef = webmcp.ToolRef("webmcp.tool-ref.v1:AAAAAAAAAAAAAAAAAAAAAA")
	wantGolden, err := os.ReadFile(filepath.Join("testdata", "webmcp-invoke-invalid-input.golden.json"))
	if err != nil {
		t.Fatalf("read malformed-input golden: %v", err)
	}

	for _, testCase := range []struct {
		name       string
		positional bool
	}{
		{name: "exact ref"},
		{name: "unique positional name", positional: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configDir := writeDirectConfig(t, "")
			store := NewFileWebMCPSelectionStore(configDir)
			_, target, candidate, tool := directFixture()
			tool.InputSchema = json.RawMessage(schema)
			runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
				testkit.NewTargetConfig(target, testkit.WithInitialCatalog(tool)),
			))
			broker := webmcp.NewBroker(webmcp.BrokerOptions{
				Runtime:    runtime,
				Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
				ToolRefFactory: func(webmcp.ToolDescriptor) (webmcp.ToolRef, error) {
					return toolRef, nil
				},
			})

			args := []string{"invoke", "--browser", string(candidate.ID), "--tab", string(target.ID)}
			if testCase.positional {
				args = append(args, "read_state")
			} else {
				args = append(args, "--tool-ref", string(toolRef))
			}
			args = append(args, "--input-json", `{"profile":{"mode":"fast","secret":"do-not-echo"}`, "--json")

			result := executeDirectCommand(t, configDir, store, directFactory(broker), args...)
			if result.err == nil {
				t.Fatal("malformed invocation unexpectedly succeeded")
			}
			if got := strings.TrimSpace(result.stdout); got != strings.TrimSpace(string(wantGolden)) {
				t.Fatalf("malformed-input envelope = %s, want golden %s", got, strings.TrimSpace(string(wantGolden)))
			}
			if strings.Contains(result.stdout, "do-not-echo") {
				t.Fatalf("malformed input leaked into result: %s", result.stdout)
			}
			envelope := decodeDirectEnvelope(t, result.stdout)
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvalidToolInput) || !envelope.Error.Retryable {
				t.Fatalf("malformed-input envelope = %+v", envelope)
			}
			operations := runtime.Operations()
			if hasTestkitOperation(operations, testkit.OperationInvoke) {
				t.Fatalf("malformed input was dispatched: %+v", operations)
			}
		})
	}
}

func TestWebMCPDirectSelectBrowserDeathAtEveryStage(t *testing.T) {
	tests := []struct {
		name        string
		operation   testkit.OperationKind
		phase       string
		targetKnown bool
		activate    bool
		block       func(*testkit.ScriptedBrowserHandle)
		blockEnable bool
	}{
		{name: "discovery_dial", operation: testkit.OperationOpen, phase: "open", block: func(handle *testkit.ScriptedBrowserHandle) { handle.BlockOpen() }},
		{name: "target_resolution", operation: testkit.OperationListTargets, phase: "list_targets", block: func(handle *testkit.ScriptedBrowserHandle) { handle.BlockListTargets() }},
		{name: "attach", operation: testkit.OperationAttach, phase: "attach", targetKnown: true, block: func(handle *testkit.ScriptedBrowserHandle) { handle.BlockAttach() }},
		{name: "activation", operation: testkit.OperationActivate, phase: "activate", targetKnown: true, activate: true, block: func(handle *testkit.ScriptedBrowserHandle) { handle.BlockActivate() }},
		{name: "enable_acknowledgement", operation: testkit.OperationEnableWebMCP, phase: "enable_webmcp", targetKnown: true, blockEnable: true},
		{name: "catalog_ready", operation: testkit.OperationEnableAcknowledged, phase: "catalog", targetKnown: true},
	}

	for _, testCase := range tests {
		for _, jsonMode := range []bool{true, false} {
			name := testCase.name + "/"
			if jsonMode {
				name += "json"
			} else {
				name += "human"
			}
			t.Run(name, func(t *testing.T) {
				configDir := writeDirectConfig(t, "")
				store := NewFileWebMCPSelectionStore(configDir)
				page, target, candidate, tool := directFixture()
				sessionOptions := []testkit.ScriptedTargetSessionOption{testkit.WithContext(page)}
				if testCase.blockEnable {
					sessionOptions = append(sessionOptions, testkit.WithBlockedEnable())
				}
				if testCase.activate {
					sessionOptions = append(sessionOptions, testkit.WithInitialCatalog(tool))
				}
				runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
					Candidate: candidate,
					Targets:   []testkit.TargetConfig{testkit.NewTargetConfig(target, sessionOptions...)},
				})
				defer func() { _ = runtime.Close() }()
				broker := webmcp.NewBroker(webmcp.BrokerOptions{
					Runtime:    runtime,
					Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
				})
				handle := runtime.Browser(candidate.ID)
				if handle == nil {
					t.Fatal("scripted browser handle is nil")
				}
				if testCase.block != nil {
					testCase.block(handle)
				}

				args := []string{
					"select",
					"--browser", string(candidate.ID),
					"--tab", string(target.ID),
					"--command-timeout", "250ms",
				}
				if testCase.activate {
					args = append(args, "--activate")
				}
				if jsonMode {
					args = append(args, "--json")
				}
				started := time.Now()
				resultDone := make(chan directCommandResult, 1)
				go func() {
					resultDone <- executeDirectCommand(t, configDir, store, directFactory(broker), args...)
				}()

				waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
				_, err := runtime.WaitForOperationAdmitted(waitCtx, testCase.operation)
				cancelWait()
				if err != nil {
					t.Fatalf("wait for %s admission: %v", testCase.operation, err)
				}
				_ = runtime.Disconnect(candidate.ID, "transport_lost")

				resultCtx, cancelResult := context.WithTimeout(context.Background(), 3*time.Second)
				var result directCommandResult
				select {
				case result = <-resultDone:
				case <-resultCtx.Done():
					cancelResult()
					t.Fatalf("select remained blocked after %s: %v", testCase.name, resultCtx.Err())
				}
				cancelResult()
				if result.err == nil {
					t.Fatalf("select succeeded after browser death: stdout=%s", result.stdout)
				}
				if elapsed := time.Since(started); elapsed > DefaultWebMCPDirectCommandTimeout {
					t.Fatalf("select took %s after browser death", elapsed)
				}

				if jsonMode {
					envelope := decodeDirectEnvelope(t, result.stdout)
					if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
						t.Fatalf("%s envelope = %+v", testCase.name, envelope)
					}
					if got := envelope.Error.Details["browser_id"]; got != string(candidate.ID) {
						t.Errorf("browser_id = %#v, want %q", got, candidate.ID)
					}
					if got := envelope.Error.Details["target_id"]; testCase.targetKnown && got != string(target.ID) {
						t.Errorf("target_id = %#v, want %q", got, target.ID)
					}
					if got := envelope.Error.Details["phase"]; got != testCase.phase {
						t.Errorf("phase = %#v, want %q", got, testCase.phase)
					}
					if got := envelope.Error.Details["reconnect_required"]; got != true {
						t.Errorf("reconnect_required = %#v, want true", got)
					}
				} else if !strings.Contains(result.stdout, "Error: browser_disconnected") {
					t.Fatalf("human select output = %q", result.stdout)
				}

				operations := runtime.Operations()
				if hasTestkitOperation(operations, testkit.OperationCloseTarget) {
					t.Fatalf("browser death caused an external target close: %+v", operations)
				}
				if countTestkitOperations(operations, testkit.OperationDetach) > 1 {
					t.Fatalf("browser death caused duplicate detach: %+v", operations)
				}
			})
		}
	}
}

func countTestkitOperations(operations []testkit.Operation, kind testkit.OperationKind) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == kind {
			count++
		}
	}
	return count
}

func TestWebMCPDirectFailedSelectionPreservesPriorSelection(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, target, candidate, _ := directFixture()
	prior := WebMCPSelection{
		Version:          WebMCPSelectionVersion,
		EndpointID:       string(candidate.ID),
		BrowserID:        string(candidate.ID),
		TargetID:         string(target.ID),
		Origin:           target.Origin,
		ContinuityMarker: "prior-selection",
		Generation:       3,
		SelectedAt:       time.Unix(3, 0).UTC(),
	}
	if err := store.Save(prior); err != nil {
		t.Fatalf("save prior selection: %v", err)
	}

	page, _, _, _ := directFixture()
	runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate: candidate,
		Targets:   []testkit.TargetConfig{testkit.NewTargetConfig(target, testkit.WithContext(page), testkit.WithBlockedEnable())},
	})
	defer func() { _ = runtime.Close() }()
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
	})

	resultDone := make(chan directCommandResult, 1)
	go func() {
		resultDone <- executeDirectCommand(t, configDir, store, directFactory(broker),
			"select", "--browser", string(candidate.ID), "--tab", string(target.ID),
			"--persist-selection", "--command-timeout", "250ms", "--json")
	}()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if _, err := runtime.WaitForOperationAdmitted(waitCtx, testkit.OperationEnableWebMCP); err != nil {
		t.Fatalf("wait for enable admission: %v", err)
	}
	_ = runtime.Disconnect(candidate.ID, "transport_lost")
	resultCtx, cancelResult := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelResult()
	var result directCommandResult
	select {
	case result = <-resultDone:
	case <-resultCtx.Done():
		t.Fatalf("failed selection remained blocked: %v", resultCtx.Err())
	}
	if result.err == nil {
		t.Fatalf("failed selection unexpectedly succeeded: %s", result.stdout)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("failed selection envelope = %+v", envelope)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("load preserved selection: %v", err)
	}
	if !reflect.DeepEqual(got, prior) {
		t.Fatalf("failed selection changed persisted state: got=%+v want=%+v", got, prior)
	}

	followUpBroker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
	})
	followUp := executeDirectCommand(t, configDir, store, directFactory(followUpBroker), "context", "--json")
	if followUp.err == nil {
		t.Fatal("follow-up context unexpectedly succeeded after browser loss")
	}
	followUpEnvelope := decodeDirectEnvelope(t, followUp.stdout)
	if followUpEnvelope.OK || followUpEnvelope.Error == nil || followUpEnvelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("follow-up context envelope = %+v", followUpEnvelope)
	}
}

func TestWebMCPDirectCommandDeadlineRemainsBoundedTimeoutClass(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	page, target, candidate, _ := directFixture()
	broker := &blockingDirectSelectBroker{
		directCommandBroker: &directCommandBroker{
			candidates: []webmcp.BrowserCandidate{candidate},
			targets:    []webmcp.Target{target},
			selected:   page,
		},
	}
	started := time.Now()
	result := executeDirectCommand(t, configDir, NewFileWebMCPSelectionStore(configDir), directFactory(broker),
		"select", "--browser", string(candidate.ID), "--tab", string(target.ID), "--command-timeout", "40ms", "--json")
	if result.err == nil {
		t.Fatal("deadline-bound select unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline-bound select took %s", elapsed)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationTimedOut) {
		t.Fatalf("deadline-bound select envelope = %+v", envelope)
	}
	if strings.Contains(result.stdout, string(webmcp.ErrorBrowserDisconnected)) {
		t.Fatalf("genuine deadline was classified as browser loss: %s", result.stdout)
	}
}

type blockingDirectSelectBroker struct {
	*directCommandBroker
	started chan struct{}
}

type blockingDirectDiscoveryBroker struct {
	*directCommandBroker
}

func (b *blockingDirectDiscoveryBroker) Discover(ctx context.Context, _ webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingDirectSelectBroker) SelectWithOptions(ctx context.Context, _ webmcp.TargetSelector, _ webmcp.SelectOptions) (webmcp.PageContext, error) {
	if b.started == nil {
		b.started = make(chan struct{})
	}
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-ctx.Done()
	return webmcp.PageContext{}, ctx.Err()
}

type directCommandResult struct {
	stdout string
	stderr string
	err    error
}

func executeDirectCommand(t *testing.T, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	return executeDirectCommandContext(t, context.Background(), configDir, store, factory, args...)
}

func executeDirectCommandThroughAgentRoot(t *testing.T, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	operations := NewWebMCPOperationsCommand(globalFlags, factory)
	operations.SelectionStore = store
	webmcpCommand := &WebMCPCommand{OperationsCommand: operations}
	root := &cobra.Command{Use: "agent"}
	root.AddCommand(NewPath("webmcp", webmcpCommand.Generate()).CreateCommand())
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"webmcp"}, args...))
	err := root.ExecuteContext(context.Background())
	return directCommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func executeDirectCommandContext(t *testing.T, ctx context.Context, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	operations := NewWebMCPOperationsCommand(globalFlags, factory)
	operations.SelectionStore = store
	root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
	operations.AddCommands(root)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return directCommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func decodeDirectEnvelope(t *testing.T, output string) webmcp.ToolResultEnvelope {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var envelope webmcp.ToolResultEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode direct envelope: %v; output=%q", err, output)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("direct output contains more than one result: err=%v extra=%#v output=%q", err, extra, output)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("invalid direct envelope: %v; output=%q", err, output)
	}
	return envelope
}

func requireDirectSuccess(t *testing.T, result directCommandResult) webmcp.ToolResultEnvelope {
	t.Helper()
	if result.err != nil {
		t.Fatalf("direct command: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if !envelope.OK {
		t.Fatalf("direct command failed: %+v", envelope.Error)
	}
	return envelope
}

func decodeDirectData(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode direct data: %v; data=%s", err, raw)
	}
}

func writeDirectConfig(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	contents := "browser:\n  connection:\n    cdp_url: http://127.0.0.1:9222\n"
	contents += extra
	if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte(contents), 0o600); err != nil {
		t.Fatalf("write direct config: %v", err)
	}
	return dir
}

func directFixture() (webmcp.PageContext, webmcp.Target, webmcp.BrowserCandidate, webmcp.ToolDescriptor) {
	candidate := webmcp.BrowserCandidate{
		ID:           "browser-a",
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      "Chrome/Test",
		Protocol:     "1.3",
		HTTPURL:      "http://127.0.0.1:9222/json/version?token=secret",
		BrowserWSURL: "ws://127.0.0.1/devtools/browser/secret",
		Loopback:     true,
	}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-a",
		Type:      "page",
		Title:     "Fixture page",
		URL:       "https://fixture.test/page?password=secret#fragment",
		Origin:    "https://fixture.test",
		Eligible:  true,
	}
	page := webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID},
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: 7,
		Connected:  true,
		Ready:      true,
	}
	tool := webmcp.ToolDescriptor{
		Ref:         "webmcp.tool-ref.v1:fixture-ref",
		Name:        "read_state",
		Description: "Read fixture state",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"additionalProperties":false}`),
		FrameID:     "frame-1",
		Origin:      target.Origin,
		Generation:  7,
	}
	return page, target, candidate, tool
}

func targetOrigin(target webmcp.Target) string {
	return target.Origin
}

func directFactory(broker webmcp.Broker) WebMCPDoctorFactory {
	return func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		return WebMCPDoctorRuntime{Broker: broker}, nil
	}
}

type directDiscoverer struct {
	candidates []webmcp.BrowserCandidate
}

func (d directDiscoverer) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return append([]webmcp.BrowserCandidate(nil), d.candidates...), nil
}

type directCommandBroker struct {
	candidates []webmcp.BrowserCandidate
	targets    []webmcp.Target
	selected   webmcp.PageContext
	catalog    webmcp.ToolCatalogSnapshot

	discoverErr error
	listErr     error
	selectErr   error
	activateErr error
	toolsErr    error
	invokeErr   error
	cancelErr   error

	invokeResult webmcp.InvokeResult
	watch        <-chan webmcp.BrokerEvent

	selectCalls     []webmcp.TargetSelector
	activateCalls   []webmcp.TargetSelector
	listTargetCalls int
	invokeRequest   webmcp.InvokeRequest
	cancelRequest   webmcp.CancelRequest
	closeCalls      int
}

type selectionOrderingWatchBroker struct {
	*directCommandBroker
	stream     chan webmcp.BrokerEvent
	subscribed bool
}

func (b *selectionOrderingWatchBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	b.subscribed = true
	return b.stream
}

func (b *selectionOrderingWatchBroker) Select(ctx context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	return b.SelectWithOptions(ctx, selector, webmcp.SelectOptions{})
}

func (b *selectionOrderingWatchBroker) SelectWithOptions(_ context.Context, selector webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	if !b.subscribed {
		return webmcp.PageContext{}, errors.New("watch subscription must precede selection")
	}
	page, err := b.directCommandBroker.SelectWithOptions(context.Background(), selector, options)
	if err != nil {
		return webmcp.PageContext{}, err
	}
	b.stream <- webmcp.BrokerEvent{Version: webmcp.BrowserEventsVersion, Type: webmcp.BrokerEventSelected, Sequence: 1, BrowserID: selector.BrowserID, TargetID: selector.TargetID, Generation: page.Generation}
	b.stream <- webmcp.BrokerEvent{Version: webmcp.BrowserEventsVersion, Type: webmcp.BrokerEventCatalogChanged, Sequence: 2, BrowserID: selector.BrowserID, TargetID: selector.TargetID, Generation: page.Generation, Reason: "tools_added"}
	close(b.stream)
	return page, nil
}

type directCancelCommandBroker struct {
	*directCommandBroker

	directCancelRequest webmcp.DirectCancelRequest
	directCancelErr     error
}

type sigintChildBroker struct {
	*directCommandBroker
	cancelContextErr error
}

func (b *sigintChildBroker) WaitInvocation(ctx context.Context, _ webmcp.InvocationID) (webmcp.InvokeResult, error) {
	<-ctx.Done()
	return webmcp.InvokeResult{}, ctx.Err()
}

func (b *sigintChildBroker) Cancel(ctx context.Context, request webmcp.CancelRequest) error {
	b.cancelContextErr = ctx.Err()
	return b.directCommandBroker.Cancel(ctx, request)
}

func (b *directCancelCommandBroker) CancelDirect(_ context.Context, request webmcp.DirectCancelRequest) error {
	b.directCancelRequest = request
	return b.directCancelErr
}

func (b *directCommandBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if b.discoverErr != nil {
		return nil, b.discoverErr
	}
	return append([]webmcp.BrowserCandidate(nil), b.candidates...), nil
}

func (b *directCommandBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	b.listTargetCalls++
	if b.listErr != nil {
		return nil, b.listErr
	}
	return append([]webmcp.Target(nil), b.targets...), nil
}

func (b *directCommandBroker) Select(_ context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	return b.selectWithOptions(selector, false)
}

func (b *directCommandBroker) SelectWithOptions(_ context.Context, selector webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	return b.selectWithOptions(selector, options.Activate)
}

func (b *directCommandBroker) selectWithOptions(selector webmcp.TargetSelector, activate bool) (webmcp.PageContext, error) {
	if b.selectErr != nil {
		return webmcp.PageContext{}, b.selectErr
	}
	b.selectCalls = append(b.selectCalls, selector)
	if activate {
		b.activateCalls = append(b.activateCalls, selector)
	}
	return b.selected, nil
}

func (b *directCommandBroker) Activate(_ context.Context, selector webmcp.TargetSelector) error {
	if b.activateErr != nil {
		return b.activateErr
	}
	b.activateCalls = append(b.activateCalls, selector)
	return nil
}

func (b *directCommandBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return b.selected, nil
}

func (b *directCommandBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	if b.toolsErr != nil {
		return webmcp.ToolCatalogSnapshot{}, b.toolsErr
	}
	return b.catalog, nil
}

func (b *directCommandBroker) Invoke(_ context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	b.invokeRequest = request
	if b.invokeErr != nil {
		return webmcp.InvokeResult{}, b.invokeErr
	}
	return b.invokeResult, nil
}

func (b *directCommandBroker) Cancel(_ context.Context, request webmcp.CancelRequest) error {
	b.cancelRequest = request
	return b.cancelErr
}

func (b *directCommandBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	if b.watch != nil {
		return b.watch
	}
	return closedEventChannel()
}

func (b *directCommandBroker) Close() error {
	b.closeCalls++
	return nil
}

func closedEventChannel() <-chan webmcp.BrokerEvent {
	channel := make(chan webmcp.BrokerEvent)
	close(channel)
	return channel
}
