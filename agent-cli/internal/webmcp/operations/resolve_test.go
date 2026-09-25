package operations

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

func TestResolveTargetUsesPersistedSelectionAsExactIdentity(t *testing.T) {
	broker := newFakeBroker()
	broker.targets = append(broker.targets, pageTarget(testSecondID, testOrigin))
	selector := Selector{LoadSelection: selectionLoader(storedSelection(), nil)}
	resolution, err := ResolveTarget(context.Background(), broker, selector)
	if err != nil {
		t.Fatalf("ResolveTarget = %v", err)
	}
	if resolution.Target.ID != testTargetID || resolution.Candidate.ID != testBrowserID || resolution.Stored == nil {
		t.Fatalf("resolution = %+v, want the persisted exact target", resolution)
	}
	if resolution.Target.BrowserID != testBrowserID {
		t.Fatalf("target browser = %q, want the candidate browser filled in", resolution.Target.BrowserID)
	}
}

func TestResolveTargetExplicitSelectorsSuppressPersistedSelection(t *testing.T) {
	loaded := false
	loader := func() (selectionstore.Selection, error) {
		loaded = true
		return storedSelection(), nil
	}
	selector := singleSelector()
	selector.LoadSelection = loader
	selector.BrowserFlagChanged = true
	if _, err := ResolveTarget(context.Background(), newFakeBroker(), selector); err != nil {
		t.Fatalf("ResolveTarget = %v", err)
	}
	if _, err := ResolveReplacementTarget(context.Background(), newFakeBroker(), Selector{Browser: singleSelector().Browser, LoadSelection: loader}); err != nil {
		t.Fatalf("ResolveReplacementTarget = %v", err)
	}
	if loaded {
		t.Fatal("an explicit selector or replacement select consulted the persisted selection")
	}
}

func TestResolveTargetRejectsStalePersistedSelection(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fakeBroker, *selectionstore.Selection)
		reason string
	}{
		{name: "endpoint", mutate: func(_ *fakeBroker, s *selectionstore.Selection) { s.EndpointID = testOtherID }, reason: reasonEndpointChanged},
		{name: "instance", mutate: func(b *fakeBroker, _ *selectionstore.Selection) { b.candidates[0].BrowserInstanceID = "instance-2" }, reason: reasonInstanceChanged},
		{name: "origin", mutate: func(_ *fakeBroker, s *selectionstore.Selection) { s.Origin = testOtherSite }, reason: reasonOriginChanged},
		{name: "continuity", mutate: func(b *fakeBroker, s *selectionstore.Selection) {
			s.ContinuityMarker = "marker-1"
			b.targets[0].ContinuityMarker = "marker-2"
		}, reason: reasonContinuityChanged},
		{name: "generation", mutate: func(_ *fakeBroker, s *selectionstore.Selection) { s.Generation = testGeneration + 1 }, reason: reasonGenerationChanged},
		{name: "target", mutate: func(_ *fakeBroker, s *selectionstore.Selection) { s.TargetID = "gone" }, reason: reasonTargetNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broker := newFakeBroker()
			stored := storedSelection()
			testCase.mutate(broker, &stored)
			_, err := ResolveTarget(context.Background(), broker, Selector{LoadSelection: selectionLoader(stored, nil)})
			code, details := classifiedCode(err)
			if code != webmcp.ErrorStaleSelection || details[detailReason] != testCase.reason {
				t.Fatalf("error = %v (%s %v), want stale %s", err, code, details, testCase.reason)
			}
			if recovery, ok := details["recovery"].(map[string]any); !ok || recovery["command"] != SelectionRecoveryCommand {
				t.Fatalf("stale details = %v, want select recovery", details)
			}
		})
	}
}

func TestResolveTargetRecognizesReplacementBrowserAtRememberedEndpoint(t *testing.T) {
	broker := newFakeBroker()
	broker.candidates = []webmcp.BrowserCandidate{{ID: testOtherID, BrowserInstanceID: "instance-2", HTTPURL: "http://127.0.0.1:9222"}}
	selector := Selector{
		Browser:       config.BrowserConfig{Connection: config.BrowserConnectionConfig{CDPURL: "http://127.0.0.1:9222"}},
		LoadSelection: selectionLoader(storedSelection(), nil),
	}
	_, err := ResolveTarget(context.Background(), broker, selector)
	if code, details := classifiedCode(err); code != webmcp.ErrorStaleSelection || details[detailReason] != reasonInstanceChanged {
		t.Fatalf("replacement error = %v, want browser_instance_changed", err)
	}
}

func TestResolveTargetClassifiesPersistedBrowserLossAsDisconnected(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeBroker)
		phase string
	}{
		{name: "discovery", setup: func(b *fakeBroker) { b.discoverErr = errors.New("dial tcp: connection refused") }, phase: phaseDiscovery},
		{name: "browser gone", setup: func(b *fakeBroker) { b.candidates[0].ID = testOtherID }, phase: phaseDiscovery},
		{name: "targets", setup: func(b *fakeBroker) {
			b.listTargetsErr = errors.Join(errTestBroker, webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "gone", map[string]any{detailPhase: "list_targets"}))
		}, phase: "list_targets"},
		{name: "wrapped", setup: func(b *fakeBroker) {
			b.listTargetsErr = fmt.Errorf("wrapped: %w", webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, "gone", map[string]any{detailPhase: "bad phase!"}))
		}, phase: phaseTargets},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broker := newFakeBroker()
			testCase.setup(broker)
			_, err := ResolveTarget(context.Background(), broker, Selector{LoadSelection: selectionLoader(storedSelection(), nil)})
			code, details := classifiedCode(err)
			if code != webmcp.ErrorBrowserDisconnected || details[detailPhase] != testCase.phase || details["reconnect_required"] != true {
				t.Fatalf("loss error = %v (%v), want browser_disconnected at %s", err, details, testCase.phase)
			}
		})
	}
}

func TestResolveTargetWithoutPersistenceReturnsDiscoveryErrorsUnchanged(t *testing.T) {
	broker := newFakeBroker()
	broker.listTargetsErr = errTestBroker
	if _, err := ResolveTarget(context.Background(), broker, singleSelector()); !errors.Is(err, errTestBroker) {
		t.Fatalf("target list error = %v, want the broker error", err)
	}
	broker = newFakeBroker()
	broker.discoverErr = errTestBroker
	if _, err := ResolveTarget(context.Background(), broker, singleSelector()); !errors.Is(err, errTestBroker) {
		t.Fatalf("discovery error = %v, want the broker error", err)
	}
}

func TestResolveTargetRejectsUnreadablePersistedSelection(t *testing.T) {
	cases := map[string]Selector{
		reasonPersistedInvalid:    {LoadSelection: selectionLoader(selectionstore.Selection{}, errTestBroker)},
		reasonPersistedIncomplete: {LoadSelection: selectionLoader(selectionstore.Selection{BrowserID: testBrowserID}, nil)},
	}
	cases[reasonPersistedInvalid+"-nil"] = Selector{}
	for reason, selector := range cases {
		_, err := ResolveTarget(context.Background(), newFakeBroker(), selector)
		code, details := classifiedCode(err)
		want := reason
		if reason == reasonPersistedInvalid+"-nil" {
			want = reasonPersistedInvalid
		}
		if code != webmcp.ErrorStaleSelection || details[detailReason] != want {
			t.Fatalf("%s: error = %v, want stale %s", reason, err, want)
		}
	}
}

func TestResolveTargetAutoSelectionPolicy(t *testing.T) {
	cases := []struct {
		name       string
		autoSelect string
		targets    []webmcp.Target
		code       webmcp.ErrorCode
		reason     string
	}{
		{name: "single", autoSelect: config.BrowserAutoSelectSingle, targets: []webmcp.Target{pageTarget(testTargetID, testOrigin)}},
		{name: "off", autoSelect: config.BrowserAutoSelectOff, targets: []webmcp.Target{pageTarget(testTargetID, testOrigin)}, code: webmcp.ErrorStaleSelection, reason: reasonSelectionRequired},
		{name: "persisted", autoSelect: config.BrowserAutoSelectPersisted, targets: []webmcp.Target{pageTarget(testTargetID, testOrigin)}, code: webmcp.ErrorStaleSelection, reason: reasonPersistedMissing},
		{name: "none", autoSelect: config.BrowserAutoSelectSingle, code: webmcp.ErrorNoEligibleTab},
		{name: "ambiguous", autoSelect: config.BrowserAutoSelectSingle, targets: []webmcp.Target{pageTarget(testSecondID, testOrigin), pageTarget(testTargetID, testOrigin)}, code: webmcp.ErrorAmbiguousTab},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broker := newFakeBroker()
			broker.targets = testCase.targets
			selector := Selector{Browser: config.BrowserConfig{Selection: config.BrowserSelectionConfig{AutoSelect: testCase.autoSelect}}, LoadSelection: selectionLoader(selectionstore.Selection{}, nil)}
			resolution, err := ResolveTarget(context.Background(), broker, selector)
			code, details := classifiedCode(err)
			if code != testCase.code || (testCase.reason != "" && details[detailReason] != testCase.reason) {
				t.Fatalf("error = %v (%v), want %q %q", err, details, testCase.code, testCase.reason)
			}
			if testCase.code == "" && resolution.Target.ID != testTargetID {
				t.Fatalf("auto-selected %q, want %q", resolution.Target.ID, testTargetID)
			}
			if ids, ok := details["candidate_target_ids"].([]string); testCase.code == webmcp.ErrorAmbiguousTab && (!ok || len(ids) != len(testCase.targets)) {
				t.Fatalf("ambiguity details = %v", details)
			}
		})
	}
}

func TestResolveTargetRequiresExactBrowserWhenSeveralMatch(t *testing.T) {
	broker := newFakeBroker()
	broker.candidates = append(broker.candidates, webmcp.BrowserCandidate{ID: testOtherID})
	_, err := ResolveTarget(context.Background(), broker, singleSelector())
	code, details := classifiedCode(err)
	if ids, ok := details["candidate_browser_ids"].([]string); code != webmcp.ErrorAmbiguousBrowser || !ok || len(ids) != len(broker.candidates) {
		t.Fatalf("error = %v, want ambiguous browser listing both IDs", err)
	}
}

func TestResolveTargetValidatesTheChosenTarget(t *testing.T) {
	cases := []struct {
		name   string
		target webmcp.Target
		origin string
		code   webmcp.ErrorCode
		reason string
	}{
		{name: "unsupported", target: webmcp.Target{ID: testTargetID, Type: targetTypePage, EligibilityReason: eligibilityUnsupported}, code: webmcp.ErrorUnsupportedWebMCP},
		{name: "ineligible", target: webmcp.Target{ID: testTargetID, Type: targetTypePage, EligibilityReason: "loading"}, code: webmcp.ErrorNoEligibleTab, reason: "loading"},
		{name: "origin", target: pageTarget(testTargetID, testOrigin), origin: testOtherSite, code: webmcp.ErrorNoEligibleTab, reason: reasonOriginMismatch},
		{name: "denied", target: pageTarget(testTargetID, testOtherSite), code: webmcp.ErrorOriginDenied},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broker := newFakeBroker()
			broker.targets = []webmcp.Target{testCase.target}
			browser := config.BrowserConfig{Selection: config.BrowserSelectionConfig{Tab: testTargetID, Origin: testCase.origin}}
			browser.Policy.DeniedOrigins = []string{testOtherSite}
			_, err := ResolveTarget(context.Background(), broker, Selector{Browser: browser})
			code, details := classifiedCode(err)
			if code != testCase.code || (testCase.reason != "" && details[detailReason] != testCase.reason) {
				t.Fatalf("error = %v (%v), want %s %s", err, details, testCase.code, testCase.reason)
			}
		})
	}
}

func TestResolveTargetHonorsCompositeTargetReference(t *testing.T) {
	broker := newFakeBroker()
	broker.candidates = append(broker.candidates, webmcp.BrowserCandidate{ID: testOtherID})
	composite := testBrowserID + "/" + testTargetID
	selector := Selector{Browser: config.BrowserConfig{Selection: config.BrowserSelectionConfig{Tab: composite}}}
	resolution, err := ResolveTarget(context.Background(), broker, selector)
	if err != nil || resolution.Candidate.ID != testBrowserID || resolution.Target.ID != testTargetID {
		t.Fatalf("composite resolution = %+v, %v", resolution, err)
	}
	selector.Browser.Selection.Browser = testOtherID
	_, err = ResolveTarget(context.Background(), broker, selector)
	if code, details := classifiedCode(err); code != webmcp.ErrorStaleSelection || details[detailReason] != reasonSelectorMismatch {
		t.Fatalf("mismatched composite error = %v, want selector_browser_mismatch", err)
	}
}
