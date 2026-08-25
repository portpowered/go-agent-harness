package sessionfixturevalidator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeValidFixture(t *testing.T, path string) {
	t.Helper()
	writeCapture(t, path, validSyntheticCapture())
}

func TestRelativeFixtureFiles_SortedRelativeToCommonParent(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested", "deeper")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	writeValidFixture(t, filepath.Join(root, "b.session.json"))
	writeValidFixture(t, filepath.Join(nested, "a.session.json"))

	files, base, err := relativeFixtureFiles([]string{root})
	if err != nil {
		t.Fatalf("relativeFixtureFiles: %v", err)
	}

	want := []string{
		"b.session.json",
		filepath.ToSlash(filepath.Join("nested", "deeper", "a.session.json")),
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files[%d] = %q, want %q", i, files[i], want[i])
		}
		if strings.Contains(files[i], base) || filepath.IsAbs(files[i]) {
			t.Fatalf("files[%d] = %q is not relative to base %q", i, files[i], base)
		}
	}
}

func TestRelativeFixtureFiles_RequiresInputPath(t *testing.T) {
	if _, _, err := relativeFixtureFiles(nil); err == nil {
		t.Fatalf("relativeFixtureFiles(nil) error = nil, want error")
	}
}

func TestBuildFixtureManifest_RenderLoadRoundTripIsByteStable(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	writeValidFixture(t, filepath.Join(root, "one.session.json"))
	writeValidFixture(t, filepath.Join(sub, "two.session.json"))

	first, err := buildFixtureManifest([]string{root})
	if err != nil {
		t.Fatalf("buildFixtureManifest: %v", err)
	}
	second, err := buildFixtureManifest([]string{root})
	if err != nil {
		t.Fatalf("buildFixtureManifest second pass: %v", err)
	}
	firstData, err := first.render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	secondData, err := second.render()
	if err != nil {
		t.Fatalf("render second: %v", err)
	}
	if !bytes.Equal(firstData, secondData) {
		t.Fatalf("manifest render not byte-stable:\n%s\nvs\n%s", firstData, secondData)
	}
	if first.Count != 2 {
		t.Fatalf("Count = %d, want 2", first.Count)
	}
	wantRendering := "{\n  \"count\": 2,\n  \"files\": [\n    \"one.session.json\",\n    \"sub/two.session.json\"\n  ]\n}\n"
	if string(firstData) != wantRendering {
		t.Fatalf("unexpected manifest rendering:\n%s\nwant:\n%s", firstData, wantRendering)
	}

	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifestPath, firstData, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	loaded, err := loadFixtureManifest(manifestPath)
	if err != nil {
		t.Fatalf("loadFixtureManifest: %v", err)
	}
	if loaded.Count != first.Count || strings.Join(loaded.Files, ",") != strings.Join(first.Files, ",") {
		t.Fatalf("loaded manifest = %+v, want %+v", loaded, first)
	}
}

func TestLoadFixtureManifest_RejectsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("write broken manifest: %v", err)
	}
	if _, err := loadFixtureManifest(path); err == nil {
		t.Fatalf("loadFixtureManifest(%s) error = nil, want parse error", path)
	}
}

func TestDiffFixtureSets_ReportsBothDriftDirectionsInScanOrder(t *testing.T) {
	live := []string{"a.session.json", "c.session.json", "d.session.json"}
	registered := []string{"a.session.json", "b.session.json", "c.session.json"}

	unregistered, stale := diffFixtureSets(live, registered)

	if strings.Join(unregistered, ",") != "d.session.json" {
		t.Fatalf("unregistered = %v, want [d.session.json]", unregistered)
	}
	if strings.Join(stale, ",") != "b.session.json" {
		t.Fatalf("stale = %v, want [b.session.json]", stale)
	}
}

func TestFormatManifestDrift_NamesPathsAndRegenerateCommand(t *testing.T) {
	message := formatManifestDrift(committedFixtureManifestRelPath, 28, 27,
		[]string{"agent-cli/test/integration/testdata/new.session.json"},
		[]string{"go-llm-gateway/pkg/testing/testdata/session-fixtures/gone.session.json"})
	for _, want := range []string{
		committedFixtureManifestRelPath,
		"scanned:    28",
		"registered: 27",
		"new.session.json",
		"gone.session.json",
		regenerateFixtureManifestCommand,
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("drift message missing %q:\n%s", want, message)
		}
	}
}

func TestRun_EmitManifest_WritesDeterministicFile(t *testing.T) {
	root := t.TempDir()
	writeValidFixture(t, filepath.Join(root, "kept.session.json"))
	outputA := filepath.Join(t.TempDir(), "a.manifest.json")
	outputB := filepath.Join(t.TempDir(), "b.manifest.json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	argsFor := func(output string) []string { return []string{"-emit-manifest", output, root} }
	if err := Run(argsFor(outputA), &stdout, &stderr); err != nil {
		t.Fatalf("Run emit-manifest: %v; stderr=%s", err, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := Run(argsFor(outputB), &stdout, &stderr); err != nil {
		t.Fatalf("Run emit-manifest second pass: %v; stderr=%s", err, stderr.String())
	}
	dataA, err := os.ReadFile(outputA)
	if err != nil {
		t.Fatalf("read manifest A: %v", err)
	}
	dataB, err := os.ReadFile(outputB)
	if err != nil {
		t.Fatalf("read manifest B: %v", err)
	}
	if !bytes.Equal(dataA, dataB) {
		t.Fatalf("emitted manifests differ:\n%s\nvs\n%s", dataA, dataB)
	}
	manifest, err := loadFixtureManifest(outputA)
	if err != nil {
		t.Fatalf("load emitted manifest: %v", err)
	}
	if manifest.Count != 1 || strings.Join(manifest.Files, ",") != "kept.session.json" {
		t.Fatalf("emitted manifest = %+v, want one entry kept.session.json", manifest)
	}
}

func TestRun_EmitManifest_RequiresAtLeastOneRoot(t *testing.T) {
	output := filepath.Join(t.TempDir(), "unused.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run([]string{"-emit-manifest", output}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run with -emit-manifest and no roots = nil error, want error")
	}
	if !strings.Contains(err.Error(), "-emit-manifest requires at least one file or directory") {
		t.Fatalf("error = %v, want -emit-manifest root requirement message", err)
	}
}
