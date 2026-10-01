package chrome

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	webmcp "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

var _ webmcp.BrowserRuntime = (*Runtime)(nil)

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

func TestResolveBrowserWebSocketResolvesRootWebSocketEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != devToolsVersionPath {
			t.Fatalf("request path = %q, want /json/version", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte(`{"webSocketDebuggerUrl":"ws://browser.example/devtools/browser/pinned"}`)); err != nil {
			t.Errorf("write version response: %v", err)
		}
	}))
	defer server.Close()

	runtime := NewRuntime(WithHTTPClient(server.Client()))
	rootWebSocket := strings.Replace(server.URL, "http://", "ws://", 1) + "/"
	resolved, err := runtime.resolveBrowserWebSocket(context.Background(), rootWebSocket)
	if err != nil {
		t.Fatalf("resolve root websocket endpoint: %v", err)
	}
	if resolved != "ws://browser.example/devtools/browser/pinned" {
		t.Fatalf("resolved websocket = %q, want pinned browser websocket", resolved)
	}
}

func TestResolveBrowserWebSocketPreservesFullBrowserWebSocketEndpoint(t *testing.T) {
	runtime := NewRuntime()
	endpoint := "ws://browser.example/devtools/browser/pinned"
	resolved, err := runtime.resolveBrowserWebSocket(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("resolve full browser websocket endpoint: %v", err)
	}
	if resolved != endpoint {
		t.Fatalf("resolved websocket = %q, want %q", resolved, endpoint)
	}
}
