package rooms

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The room contract must remain usable by transports without importing the
// concrete runtime or device gateway. This keeps construction in Wire and
// prevents a gateway instance from leaking through the public service API.
func TestContractDoesNotImportRuntimeOrDeviceGateway(t *testing.T) {
	root := "."
	entries, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, "\"")
			if strings.Contains(path, "/services/internal/agentruntime") || strings.Contains(path, "/go-device-gateway/") {
				t.Errorf("%s imports concrete implementation %q", path, path)
			}
		}
	}
}
