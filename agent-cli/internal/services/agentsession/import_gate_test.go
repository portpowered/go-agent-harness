package agentsession

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicSessionContractDoesNotImportConcreteGateway(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract path")
	}
	path := filepath.Join(filepath.Dir(file), "interface.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, "go-device-gateway") || strings.Contains(path, "go-llm-gateway") {
			t.Fatalf("public contract imports concrete gateway package %q", path)
		}
	}
}
