package roomaudiofixture

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// committedFixtureRoot is the directory holding the committed fixtures that
// the roomreplay wire golden tests consume.
const committedFixtureRoot = "../../wire/testdata/room-audio"

// TestGenerateReproducesCommittedFixtures regenerates every shape and requires
// each generated file to match the committed fixture byte for byte, so the
// goldens stay reproducible from this generator.
func TestGenerateReproducesCommittedFixtures(t *testing.T) {
	for _, shape := range []string{ShapeCleanTurnTaking, ShapeDeliberateOverlap, ShapeLongConversationTermination} {
		t.Run(shape, func(t *testing.T) {
			output := t.TempDir()
			if err := Generate(shape, output); err != nil {
				t.Fatalf("Generate(%q): %v", shape, err)
			}
			assertTreesMatch(t, readTree(t, output), readTree(t, filepath.Join(committedFixtureRoot, shape)))
		})
	}
}

func TestGenerateRejectsUnknownShapeAndEmptyOutput(t *testing.T) {
	if err := Generate("unknown", t.TempDir()); err == nil || !strings.Contains(err.Error(), `unknown room audio fixture shape "unknown"`) {
		t.Fatalf("unknown shape error = %v", err)
	}
	for _, shape := range []string{ShapeCleanTurnTaking, ShapeDeliberateOverlap, ShapeLongConversationTermination} {
		if err := Generate(shape, ""); err == nil || err.Error() != "fixture output directory is empty" {
			t.Fatalf("Generate(%q, \"\") error = %v", shape, err)
		}
	}
}

// assertTreesMatch requires every generated file to equal its committed
// counterpart and every committed file except hand-written READMEs to be
// generated.
func assertTreesMatch(t *testing.T, generated, committed map[string][]byte) {
	t.Helper()
	for path, data := range generated {
		want, ok := committed[path]
		if !ok {
			t.Errorf("generated %s is not committed", path)
			continue
		}
		if !bytes.Equal(data, want) {
			t.Errorf("generated %s differs from the committed fixture", path)
		}
	}
	for path := range committed {
		if _, ok := generated[path]; !ok && !strings.HasSuffix(path, "README.md") {
			t.Errorf("committed %s is not generated", path)
		}
	}
}

func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("read fixture tree %s: %v", root, err)
	}
	return files
}
