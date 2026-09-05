package devices

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicDeviceContractDoesNotImportGatewayOrPrivateImplementation(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(filename), "interface.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse public device contract: %v", err)
	}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, "\"")
		if strings.Contains(path, "go-device-gateway") || strings.Contains(path, "/services/internal/") {
			t.Fatalf("public device contract imports private implementation dependency %q", path)
		}
	}
}
