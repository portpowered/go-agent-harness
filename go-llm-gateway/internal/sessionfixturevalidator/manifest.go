package sessionfixturevalidator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// regenerateFixtureManifestCommand is the canonical command (run from the
// repository root, where go.work lives) that regenerates the checked-in
// committed fixture manifest after an intentional fixture set change.
const regenerateFixtureManifestCommand = "go run ./go-llm-gateway/cmd/session-fixture-validator" +
	" -emit-manifest go-llm-gateway/internal/sessionfixturevalidator/testdata/committed-fixtures.manifest.json" +
	" go-llm-gateway/pkg/providers/openai/testdata" +
	" go-llm-gateway/pkg/testing/testdata/session-fixtures" +
	" agent-cli/test/integration/testdata"

// fixtureManifest is the checked-in registry of every committed session
// fixture under the registered roots. Fixture registration is generated, not
// hand-maintained: a fixture change without regenerating the manifest is a
// drift failure naming the drifted paths.
type fixtureManifest struct {
	Count int      `json:"count"`
	Files []string `json:"files"`
}

const committedFixtureManifestRelPath = "testdata/committed-fixtures.manifest.json"

// relativeFixtureFiles scans paths with the same recursive WalkDir semantics as
// ValidatePaths and returns every discovered fixture as a slash-separated,
// sorted path relative to the longest common parent directory of the inputs,
// so the live scan and the generated manifest are always expressed in the same
// terms.
func relativeFixtureFiles(paths []string) (files []string, base string, err error) {
	if len(paths) == 0 {
		return nil, "", fmt.Errorf("scan fixtures: at least one file or directory is required")
	}
	collected, err := collectSessionFixtureFiles(paths)
	if err != nil {
		return nil, "", err
	}
	// Anchor at the input itself when it is a directory so scanning one root
	// yields paths relative to that root; file inputs anchor at their parent.
	anchors := make([]string, 0, len(paths))
	for _, path := range paths {
		absPath, absErr := filepath.Abs(path)
		if absErr != nil {
			return nil, "", fmt.Errorf("resolve %s: %w", path, absErr)
		}
		if info, statErr := os.Stat(absPath); statErr == nil && !info.IsDir() {
			absPath = filepath.Dir(absPath)
		}
		anchors = append(anchors, absPath)
	}
	base = longestCommonDir(anchors)
	for _, file := range collected {
		absFile, absErr := filepath.Abs(file)
		if absErr != nil {
			return nil, "", fmt.Errorf("resolve %s: %w", file, absErr)
		}
		relPath, relErr := filepath.Rel(base, absFile)
		if relErr != nil {
			return nil, "", fmt.Errorf("relativize %s against %s: %w", file, base, relErr)
		}
		files = append(files, filepath.ToSlash(relPath))
	}
	sort.Strings(files)
	return files, base, nil
}

// buildFixtureManifest scans paths and returns a manifest whose Files are
// slash-separated, sorted, and relative to the longest common parent directory
// of the inputs, so repeated invocations over the same tree are byte-stable.
func buildFixtureManifest(paths []string) (fixtureManifest, error) {
	files, _, err := relativeFixtureFiles(paths)
	if err != nil {
		return fixtureManifest{}, err
	}
	return fixtureManifest{Count: len(files), Files: files}, nil
}

// render encodes the manifest deterministically: fixed field order, two-space
// indent, sorted relative paths, no timestamps or absolute paths, and a
// trailing newline.
func (m fixtureManifest) render() ([]byte, error) {
	files := m.Files
	if files == nil {
		files = []string{}
	}
	data, err := json.MarshalIndent(fixtureManifest{Count: m.Count, Files: files}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode fixture manifest: %w", err)
	}
	return append(data, '\n'), nil
}

// loadFixtureManifest reads and parses a manifest written by render.
func loadFixtureManifest(path string) (fixtureManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fixtureManifest{}, fmt.Errorf("read fixture manifest %s: %w", path, err)
	}
	var manifest fixtureManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fixtureManifest{}, fmt.Errorf("parse fixture manifest %s: %w", path, err)
	}
	return manifest, nil
}

// diffFixtureSets compares the live scan against the registered manifest and
// returns drifted paths in scan order: unregistered exist on disk but not in
// the manifest; stale are registered but missing on disk.
func diffFixtureSets(live, registered []string) (unregistered, stale []string) {
	liveSet := make(map[string]bool, len(live))
	for _, file := range live {
		liveSet[file] = true
	}
	registeredSet := make(map[string]bool, len(registered))
	for _, file := range registered {
		registeredSet[file] = true
	}
	for _, file := range live {
		if !registeredSet[file] {
			unregistered = append(unregistered, file)
		}
	}
	for _, file := range registered {
		if !liveSet[file] {
			stale = append(stale, file)
		}
	}
	return unregistered, stale
}

// formatManifestDrift renders the self-explaining drift failure naming the
// drifted paths and the regenerate command.
func formatManifestDrift(manifestRef string, scanned, registered int, unregistered, stale []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "committed session fixture set drifted from manifest %s", manifestRef)
	fmt.Fprintf(&b, "\n  scanned:    %d fixture(s)", scanned)
	fmt.Fprintf(&b, "\n  registered: %d fixture(s)", registered)
	fmt.Fprintf(&b, "\n  unregistered (on disk, missing from manifest):")
	if len(unregistered) == 0 {
		b.WriteString(" (none)")
	}
	for _, file := range unregistered {
		fmt.Fprintf(&b, "\n    %s", file)
	}
	fmt.Fprintf(&b, "\n  stale (in manifest, missing on disk):")
	if len(stale) == 0 {
		b.WriteString(" (none)")
	}
	for _, file := range stale {
		fmt.Fprintf(&b, "\n    %s", file)
	}
	fmt.Fprintf(&b, "\nif the fixture change is intentional, regenerate the manifest from the repository root with:\n  %s", regenerateFixtureManifestCommand)
	return b.String()
}

// longestCommonDir returns the component-wise longest common prefix of
// already-absolute directory paths.
func longestCommonDir(absDirs []string) string {
	parts := splitSlash(absDirs[0])
	for _, dir := range absDirs[1:] {
		other := splitSlash(dir)
		n := 0
		for n < len(parts) && n < len(other) && parts[n] == other[n] {
			n++
		}
		parts = parts[:n]
	}
	return filepath.Clean(strings.Join(parts, "/"))
}

func splitSlash(path string) []string {
	return strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
}
