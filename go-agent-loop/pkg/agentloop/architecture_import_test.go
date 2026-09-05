package agentloop

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestProductionAudioAndDeviceOwnership keeps the loop independent from
// device implementations and local PCM codecs. Audio bytes leave the loop
// through go-audio/pkg/codec instead of being parsed in this module.
func TestProductionAudioAndDeviceOwnership(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Join(filepath.Dir(filename), "..", "..")
	fset := token.NewFileSet()
	var violations []string
	err := filepath.Walk(moduleRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if importPath == "encoding/binary" ||
				strings.Contains(importPath, "/go-device-gateway/") ||
				strings.Contains(importPath, "/agent-cli/internal/audio") ||
				strings.Contains(importPath, "/go-llm-gateway/pkg/wavio") ||
				strings.Contains(importPath, "/go-agent-loop/pkg/platform/clock") {
				violations = append(violations, path+" imports "+importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan loop production imports: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("loop production ownership violations:\n%s\nuse go-audio/pkg/codec for PCM and keep device/runtime dependencies outside go-agent-loop", strings.Join(violations, "\n"))
	}
}
