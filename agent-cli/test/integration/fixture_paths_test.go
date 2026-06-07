package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gwtesting "github.com/portpowered/go-llm-gateway/pkg/testing"
)

func locateSharedSessionFixture(t *testing.T, name string) string {
	t.Helper()

	path := gwtesting.SharedSessionFixturePath(name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("shared session fixture %q not found at %q: %v", name, path, err)
	}
	return path
}

func locateCLIFixture(t *testing.T, name string) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve CLI fixture helper path: runtime.Caller failed")
	}

	path := filepath.Join(filepath.Dir(currentFile), "testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("CLI fixture %q not found at %q: %v", name, path, err)
	}
	return path
}

func TestLocateSharedSessionFixtureUsesGatewayOwnedRoot(t *testing.T) {
	path := locateSharedSessionFixture(t, "session_text_reply.session.json")

	normalizedPath := filepath.ToSlash(path)
	if !strings.Contains(normalizedPath, gwtesting.SharedSessionFixtureRoot+"/") {
		t.Fatalf("shared fixture path %q should resolve under %q", normalizedPath, gwtesting.SharedSessionFixtureRoot)
	}
}

func TestLocateCLIFixtureUsesAgentCLIPrivateTestdata(t *testing.T) {
	path := locateCLIFixture(t, "openai_realtime_text.session.json")

	normalizedPath := filepath.ToSlash(path)
	if !strings.Contains(normalizedPath, "/agent-cli/test/integration/testdata/") {
		t.Fatalf("CLI fixture path %q should stay under agent-cli private testdata", normalizedPath)
	}
}
