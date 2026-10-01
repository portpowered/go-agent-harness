//go:build e2e

package chrome

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// TestCapitalOneShoppingAdapterLive is an opt-in, read-only production-site
// gate. It launches an isolated Chrome profile, waits for the actual page to
// settle, observes the injected page catalog, and performs a bounded scan. It
// never invokes an offer activation or purchase control.
func TestCapitalOneShoppingAdapterLive(t *testing.T) {
	chromeExecutable, chromeVersion := findQualifiedStockChromeForIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	launcher := NewManagedBrowserLauncher(ManagedBrowserLaunchOptions{
		ConfigDir:  t.TempDir(),
		StartupURL: "https://capitaloneshopping.com/",
		Headless:   true,
		Acquirer: ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
			return ChromeExecutable{Path: chromeExecutable, Version: chromeVersion, Major: MinimumManagedChromeMajor, Source: ExecutableSourceStock}, nil
		}),
		StartupTimeout: 30 * time.Second,
	})
	browser, err := launcher.Launch(ctx)
	if err != nil {
		t.Fatalf("launch stock Chrome: %v", err)
	}
	t.Cleanup(func() {
		if err := browser.Close(); err != nil {
			t.Logf("close stock Chrome: %v", err)
		}
	})
	target, err := waitForFixturePageTarget(ctx, browserHTTPURL(browser.Endpoint().BrowserWSEndpoint), "https://capitaloneshopping.com/")
	if err != nil {
		t.Fatalf("discover Capital One Shopping target: %v", err)
	}
	runtimeAdapter := NewRuntime(WithCommandTimeout(30 * time.Second))
	handle, err := runtimeAdapter.Open(ctx, webmcp.BrowserCandidate{
		ID: "capital-one-shopping-live-browser", HTTPURL: browser.Endpoint().CDPURL,
		BrowserWSURL: browser.Endpoint().BrowserWSEndpoint, Loopback: true,
	})
	if err != nil {
		t.Fatalf("open browser runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Logf("close browser runtime: %v", err)
		}
	})
	session, err := handle.Attach(ctx, webmcp.TargetID(target.ID), webmcp.TargetOwnershipHarnessOwned)
	if err != nil {
		t.Fatalf("attach Capital One Shopping target: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Logf("close browser session: %v", err)
		}
	})
	targetSession, ok := session.(*targetSession)
	if !ok {
		t.Fatalf("browser session has type %T, want *targetSession", session)
	}

	readiness := waitForCapitalOneShoppingDocument(t, ctx, targetSession)
	if err := session.EnableWebMCP(ctx); err != nil {
		t.Fatalf("enable WebMCP: %v", err)
	}
	catalogCtx, cancelCatalog := context.WithTimeout(ctx, 15*time.Second)
	defer cancelCatalog()
	tools, catalogErr := waitForCapitalOneShoppingCatalog(catalogCtx, session)
	if catalogErr != nil {
		readiness = inspectCapitalOneShoppingDocument(t, ctx, targetSession)
		t.Fatalf("wait for live adapter catalog: %v; page=%+v", catalogErr, readiness)
	}
	fixture := adapterFixture{ctx: ctx, session: session, target: targetSession, tools: tools, count: 4, version: chromeVersion}
	output := invokeAdapterTool(t, fixture, "capital_one_shopping_scan_offers", `{"max_pages":20,"max_cost_usd":500,"min_cashback_percent":70,"min_bonus_usd":300,"reward_match":"any","unknown_cost_policy":"separate"}`)
	var result struct {
		Data struct {
			PagesScanned          int                           `json:"pages_scanned"`
			OffersObserved        int                           `json:"offers_observed"`
			MatchCount            int                           `json:"match_count"`
			UnknownCostMatchCount int                           `json:"unknown_cost_match_count"`
			StopReason            string                        `json:"stop_reason"`
			Matches               []capitalOneShoppingLiveOffer `json:"matches"`
			UnknownCostMatches    []capitalOneShoppingLiveOffer `json:"unknown_cost_matches"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.Data.PagesScanned < 1 || result.Data.PagesScanned > 20 || result.Data.OffersObserved == 0 || result.Data.StopReason != "max_pages" && result.Data.StopReason != "no_growth" {
		t.Fatalf("live scan result=%s decode=%v", output, err)
	}
	offerEvidence, err := json.Marshal(struct {
		Matches     []capitalOneShoppingLiveOffer `json:"matches"`
		UnknownCost []capitalOneShoppingLiveOffer `json:"unknown_cost_matches"`
	}{Matches: result.Data.Matches, UnknownCost: result.Data.UnknownCostMatches})
	if err != nil {
		t.Fatalf("encode live offer evidence: %v", err)
	}
	t.Logf("WEBMCP_CAPITAL_ONE_SHOPPING_LIVE_PASS chrome=%s title=%q pages=%d offers=%d matches=%d unknown_cost_matches=%d offer_evidence=%s stop=%s", chromeVersion, readiness.Title, result.Data.PagesScanned, result.Data.OffersObserved, result.Data.MatchCount, result.Data.UnknownCostMatchCount, offerEvidence, result.Data.StopReason)
}
