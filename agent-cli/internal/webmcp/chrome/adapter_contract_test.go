package chrome

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	cdpWebMCP "github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
	webmcp "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

var _ webmcp.BrowserRuntime = (*Runtime)(nil)

func TestPinnedBindingsContainRequiredWebMCPSurface(t *testing.T) {
	_ = cdpWebMCP.Enable
	_ = cdpWebMCP.Disable
	_ = cdpWebMCP.InvokeTool
	_ = cdpWebMCP.CancelInvocation
	_ = cdpWebMCP.EventToolsAdded{}
	_ = cdpWebMCP.EventToolsRemoved{}
	_ = cdpWebMCP.EventToolInvoked{}
	_ = cdpWebMCP.EventToolResponded{}
}

func TestRuntimeSatisfiesNeutralBrowserRuntime(t *testing.T) {
	if NewRuntime() == nil {
		t.Fatal("NewRuntime returned a nil neutral runtime")
	}
}

func TestExportedChromeAPIDoesNotLeakProtocolTypes(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	directory := filepath.Dir(sourceFile)
	fileSet := token.NewFileSet()
	//lint:ignore SA1019 ParseDir is the contract test's deliberate source-file boundary.
	parsed, err := parser.ParseDir(fileSet, directory, func(fileInfo os.FileInfo) bool {
		return !strings.HasSuffix(fileInfo.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse Chrome package: %v", err)
	}
	var files []*ast.File
	for _, packageFiles := range parsed {
		for _, file := range packageFiles.Files {
			files = append(files, file)
		}
	}
	for _, file := range files {
		protocolAliases := protocolImportAliases(file)
		for _, declaration := range file.Decls {
			for _, publicTypeNode := range exportedTypeNodes(declaration) {
				ast.Inspect(publicTypeNode, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					packageName, ok := selector.X.(*ast.Ident)
					if ok && protocolAliases[packageName.Name] {
						t.Fatalf("exported Chrome API references protocol package through %s", packageName.Name)
					}
					return true
				})
			}
		}
	}
}

func exportedTypeNodes(declaration ast.Decl) []ast.Node {
	switch declaration := declaration.(type) {
	case *ast.FuncDecl:
		if declaration.Name.IsExported() {
			return []ast.Node{declaration.Type}
		}
	case *ast.GenDecl:
		var nodes []ast.Node
		for _, specification := range declaration.Specs {
			switch specification := specification.(type) {
			case *ast.TypeSpec:
				if specification.Name.IsExported() {
					nodes = append(nodes, specification.Type)
				}
			case *ast.ValueSpec:
				for _, name := range specification.Names {
					if name.IsExported() && specification.Type != nil {
						nodes = append(nodes, specification.Type)
						break
					}
				}
			}
		}
		return nodes
	}
	return nil
}

func protocolImportAliases(file *ast.File) map[string]bool {
	aliases := make(map[string]bool)
	for _, declaration := range file.Imports {
		path, err := strconv.Unquote(declaration.Path.Value)
		if err != nil || (!strings.HasPrefix(path, "github.com/chromedp/") && !strings.HasPrefix(path, "github.com/go-json-experiment/")) {
			continue
		}
		if declaration.Name != nil {
			aliases[declaration.Name.Name] = true
			continue
		}
		parts := strings.Split(path, "/")
		aliases[parts[len(parts)-1]] = true
	}
	return aliases
}

const capitalOneShoppingLiveEnv = "WEBMCP_CAPITAL_ONE_SHOPPING_LIVE"

// TestCapitalOneShoppingAdapterLive is an opt-in, read-only production-site
// gate. It launches an isolated Chrome profile, waits for the actual page to
// settle, observes the injected page catalog, and performs a bounded scan. It
// never invokes an offer activation or purchase control.
func TestCapitalOneShoppingAdapterLive(t *testing.T) {
	if os.Getenv(capitalOneShoppingLiveEnv) != "1" {
		t.Skipf("set %s=1 to run the live Capital One Shopping adapter gate", capitalOneShoppingLiveEnv)
	}
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

type capitalOneShoppingLiveOffer struct {
	Merchant        string   `json:"merchant"`
	Description     string   `json:"description"`
	CashbackPercent *float64 `json:"cashback_percent"`
	BonusUSD        *float64 `json:"bonus_usd"`
	RewardCapUSD    *float64 `json:"reward_cap_usd"`
	QualifyingSpend *float64 `json:"qualifying_spend_usd"`
	CostUSD         *float64 `json:"cost_usd"`
}

type capitalOneShoppingDocumentState struct {
	Title             string `json:"title"`
	ReadyState        string `json:"ready_state"`
	BodyPresent       bool   `json:"body_present"`
	ModelContext      string `json:"model_context"`
	NavigatorContext  string `json:"navigator_context"`
	AdapterInstalled  bool   `json:"adapter_installed"`
	AdapterRegistered bool   `json:"adapter_registered"`
	AdapterError      string `json:"adapter_error"`
}

func inspectCapitalOneShoppingDocument(t *testing.T, ctx context.Context, session *targetSession) capitalOneShoppingDocumentState {
	t.Helper()
	var state capitalOneShoppingDocumentState
	expression := `(() => { const adapter = globalThis.__yuiCapitalOneShoppingWebMCPAdapterV1; return { title: document.title || "", ready_state: document.readyState, body_present: !!document.body, model_context: typeof document.modelContext, navigator_context: typeof navigator.modelContext, adapter_installed: !!adapter, adapter_registered: !!adapter?.registered, adapter_error: String(adapter?.error || "") }; })()`
	if err := session.run(ctx, chromedp.Evaluate(expression, &state)); err != nil {
		t.Fatalf("inspect Capital One Shopping document: %v", err)
	}
	return state
}

func waitForCapitalOneShoppingDocument(t *testing.T, ctx context.Context, session *targetSession) capitalOneShoppingDocumentState {
	t.Helper()
	for {
		state := inspectCapitalOneShoppingDocument(t, ctx, session)
		if state.BodyPresent && state.ReadyState == "complete" && state.Title != "" {
			return state
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Capital One Shopping document: %v (last=%+v)", ctx.Err(), state)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitForCapitalOneShoppingCatalog(ctx context.Context, session webmcp.TargetSession) (map[string]webmcp.ToolDescriptor, error) {
	tools := make(map[string]webmcp.ToolDescriptor, 4)
	for len(tools) < 4 {
		added, err := waitForIntegrationEvent(ctx, session.Events(), "Capital One Shopping live adapter catalog", func(event webmcp.BrowserEvent) bool {
			return event.Type == webmcp.EventToolsAdded && len(event.Tools) > 0
		})
		if err != nil {
			return nil, err
		}
		for _, tool := range added.Tools {
			if len(tool.Name) >= len("capital_one_shopping_") && tool.Name[:len("capital_one_shopping_")] == "capital_one_shopping_" {
				tools[tool.Name] = tool
			}
		}
	}
	return tools, nil
}
