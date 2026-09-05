package cli

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeviceListTransportImportsOnlyPublicServiceBoundary(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(filename), "devices.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse device list transport: %v", err)
	}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, "\"")
		if strings.Contains(path, "go-device-gateway") || strings.Contains(path, "/services/internal/") {
			t.Fatalf("device list transport imports implementation dependency %q", path)
		}
	}
}
