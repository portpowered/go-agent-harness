package codec

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The audio module must remain usable without the agent, provider, or device
// modules. Scan all build variants, including platform-specific source files.
func TestAudioModuleHasNoReverseDependencies(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate module source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			for _, module := range []string{"agent-cli", "go-agent-loop", "go-llm-gateway", "go-device-gateway"} {
				prefix := "github.com/portpowered/go-agent-harness/" + module
				if imported == prefix || strings.HasPrefix(imported, prefix+"/") {
					t.Errorf("%s imports higher-level module %s", path, imported)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
