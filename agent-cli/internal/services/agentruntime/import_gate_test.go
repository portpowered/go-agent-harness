package agentruntime

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicRuntimeContractHasNoConcreteRuntimeImports(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract path")
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(filename), "interface.go"), nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, "\"")
		if strings.Contains(path, "/services/internal/") || strings.Contains(path, "go-device-gateway") || strings.Contains(path, "go-llm-gateway") {
			t.Fatalf("public runtime contract imports concrete implementation %q", path)
		}
	}
}
