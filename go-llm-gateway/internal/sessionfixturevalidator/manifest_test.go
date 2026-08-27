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

func TestRepositoryRootPath_TrimpathCallerUsesWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested", "working-directory")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("create nested working directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.24.2\n"), 0644); err != nil {
		t.Fatalf("write go.work marker: %v", err)
	}

	got := repositoryRootPathFrom(nested, "github.com/portpowered/go-agent-harness/go-llm-gateway/internal/sessionfixturevalidator/manifest.go")
	if got != root {
		t.Fatalf("repositoryRootPathFrom() = %q, want %q", got, root)
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
	outputA := filepath.Join(t.TempDir(), "a.manifest.json")
	outputB := filepath.Join(t.TempDir(), "b.manifest.json")
	roots := allCommittedFixtureRoots()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	argsFor := func(output string) []string {
		args := []string{"-emit-manifest", output}
		return append(args, roots...)
	}
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
	if manifest.Count != len(manifest.Files) || manifest.Count == 0 {
		t.Fatalf("emitted manifest = %+v, want a non-empty count derived from entries", manifest)
	}
	if err := validateCommittedFixtureManifest(manifest); err != nil {
		t.Fatalf("emitted manifest scope: %v", err)
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
	if !strings.Contains(err.Error(), "-emit-manifest requires at least one committed-fixture root") {
		t.Fatalf("error = %v, want -emit-manifest root requirement message", err)
	}
}

func TestRun_EmitManifest_RejectsOutsideOmittedAndDuplicateRoots(t *testing.T) {
	output := filepath.Join(t.TempDir(), "manifest.json")
	allRoots := allCommittedFixtureRoots()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "outside scope",
			args: []string{"-emit-manifest", output, t.TempDir()},
			want: "outside the supported repository scope",
		},
		{
			name: "omitted root",
			args: append([]string{"-emit-manifest", output}, allRoots[:2]...),
			want: "missing authoritative committed-fixture root(s)",
		},
		{
			name: "duplicate root",
			args: append([]string{"-emit-manifest", output}, append(allRoots, allRoots[0])...),
			want: "duplicate committed-fixture root",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if err := Run(test.args, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run error = %v, want substring %q", err, test.want)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("manifest output exists after rejected emission: %v", err)
			}
		})
	}
}

func TestBuildFixtureManifest_RejectsOverlappingRoots(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	writeValidFixture(t, filepath.Join(nested, "duplicate.session.json"))

	if _, err := buildFixtureManifest([]string{root, nested}); err == nil || !strings.Contains(err.Error(), "duplicate fixture discovery") {
		t.Fatalf("buildFixtureManifest error = %v, want duplicate discovery error", err)
	}
}

func TestCompareFixtureManifest_ReportsAddDeleteAndRenameDrift(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.session.json")
	second := filepath.Join(root, "second.session.json")
	writeValidFixture(t, first)
	writeValidFixture(t, second)
	manifest, err := buildFixtureManifest([]string{root})
	if err != nil {
		t.Fatalf("buildFixtureManifest: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "added.session.json"), []byte("not validated by inventory"), 0644); err != nil {
		t.Fatalf("add fixture: %v", err)
	}
	addedLive, _, err := relativeFixtureFiles([]string{root})
	if err != nil {
		t.Fatalf("relativeFixtureFiles after add: %v", err)
	}
	addedErr := compareFixtureManifest("testdata/manifest.json", len(addedLive), addedLive, manifest)
	if addedErr == nil || !strings.Contains(addedErr.Error(), "added.session.json") || !strings.Contains(addedErr.Error(), "unregistered") {
		t.Fatalf("addition error = %v, want unregistered added path", addedErr)
	}

	if err := os.Remove(first); err != nil {
		t.Fatalf("delete fixture: %v", err)
	}
	deletedLive, _, err := relativeFixtureFiles([]string{root})
	if err != nil {
		t.Fatalf("relativeFixtureFiles after delete: %v", err)
	}
	deletedErr := compareFixtureManifest("testdata/manifest.json", len(deletedLive), deletedLive, manifest)
	if deletedErr == nil || !strings.Contains(deletedErr.Error(), "first.session.json") || !strings.Contains(deletedErr.Error(), "stale") {
		t.Fatalf("deletion error = %v, want stale deleted path", deletedErr)
	}

	if err := os.Rename(second, filepath.Join(root, "renamed.session.json")); err != nil {
		t.Fatalf("rename fixture: %v", err)
	}
	renamedLive, _, err := relativeFixtureFiles([]string{root})
	if err != nil {
		t.Fatalf("relativeFixtureFiles after rename: %v", err)
	}
	renameErr := compareFixtureManifest("testdata/manifest.json", len(renamedLive), renamedLive, manifest)
	if renameErr == nil || !strings.Contains(renameErr.Error(), "renamed.session.json") || !strings.Contains(renameErr.Error(), "second.session.json") {
		t.Fatalf("rename error = %v, want both renamed and stale paths", renameErr)
	}
	if !strings.Contains(renameErr.Error(), regenerateFixtureManifestCommand) {
		t.Fatalf("rename error = %v, want canonical regeneration command", renameErr)
	}
}

func TestCompareFixtureManifest_RejectsDuplicateLiveDiscovery(t *testing.T) {
	manifest := fixtureManifest{Count: 1, Files: []string{"one.session.json"}}
	err := compareFixtureManifest("testdata/manifest.json", 2,
		[]string{"one.session.json", "one.session.json"}, manifest)
	if err == nil || !strings.Contains(err.Error(), "duplicate live fixture discovery") || !strings.Contains(err.Error(), "one.session.json") {
		t.Fatalf("compareFixtureManifest error = %v, want duplicate live path", err)
	}
}

func TestLoadFixtureManifest_RejectsDuplicateCountAndNoncanonicalEntries(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "duplicate",
			body: `{"count":2,"files":["one.session.json","one.session.json"]}`,
			want: "duplicate manifest entry",
		},
		{
			name: "inconsistent count",
			body: `{"count":2,"files":["one.session.json"]}`,
			want: "does not equal",
		},
		{
			name: "parent traversal",
			body: `{"count":1,"files":["../one.session.json"]}`,
			want: "not canonical",
		},
		{
			name: "backslash",
			body: `{"count":1,"files":["dir\\one.session.json"]}`,
			want: "slash separators",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifestPath := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(manifestPath, []byte(test.body), 0644); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			if _, err := loadFixtureManifest(manifestPath); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadFixtureManifest error = %v, want substring %q", err, test.want)
			}
		})
	}
}
