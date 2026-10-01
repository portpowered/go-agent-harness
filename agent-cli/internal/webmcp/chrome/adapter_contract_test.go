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
