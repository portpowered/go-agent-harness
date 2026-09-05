package tools

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicToolContractDoesNotImportPrivateServiceImplementation(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(filename), "interface.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse public tool contract: %v", err)
	}
	for _, spec := range file.Imports {
		importPath := strings.Trim(spec.Path.Value, "\"")
		if strings.Contains(importPath, "/services/internal/") {
			t.Fatalf("public tool contract imports private service implementation %q", importPath)
		}
	}
}
