package sessionfixturevalidator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
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

const committedFixtureManifestRelPath = "testdata/committed-fixtures.manifest.json"

const sessionFixtureValidatorPackageRelPath = "go-llm-gateway/internal/sessionfixturevalidator"

// committedFixtureRootRegistry is the single authoritative registry for the
// committed session-fixture roots. Keep these repository-relative paths
// explicit: both the validator's committed-fixture check and manifest emitter
// resolve and consume this registry.
var committedFixtureRootRegistry = [...]string{
	"go-llm-gateway/pkg/providers/openai/testdata",
	"go-llm-gateway/pkg/testing/testdata/session-fixtures",
	"agent-cli/test/integration/testdata",
}

// fixtureManifest is the checked-in registry of every committed session
// fixture under the registered roots. Fixture registration is generated, not
// hand-maintained: a fixture change without regenerating the manifest is a
// drift failure naming the drifted paths.
type fixtureManifest struct {
	Count int      `json:"count"`
	Files []string `json:"files"`
}

// authoritativeCommittedFixtureRootRelPaths returns a copy so callers cannot
// accidentally alter the registry while assembling command arguments.
func authoritativeCommittedFixtureRootRelPaths() []string {
	return append([]string(nil), committedFixtureRootRegistry[:]...)
}

// allCommittedFixtureRoots resolves the authoritative registry from the
// repository containing this package. It is used by committed-fixture
// verification and is deliberately kept in production package state so the
// emitter and verifier cannot silently acquire different roots.
func allCommittedFixtureRoots() []string {
	paths := make([]string, 0, len(committedFixtureRootRegistry))
	for _, relPath := range committedFixtureRootRegistry {
		paths = append(paths, filepath.Join(repositoryRootPath(), filepath.FromSlash(relPath)))
	}
	return paths
}

// gatewayCommittedFixtureRoots returns the two gateway-owned roots used by
// the existing hygiene smoke check. The complete committed-fixture check uses
// allCommittedFixtureRoots, including the agent-cli integration root.
func gatewayCommittedFixtureRoots() []string {
	roots := allCommittedFixtureRoots()
	return roots[:2]
}

// repositoryRootPath derives the checkout root from a real filesystem anchor.
// Go's -trimpath build mode can replace runtime.Caller filenames with import
// paths, so the working-directory walk is preferred and the caller filename is
// used only when it is an absolute path.
func repositoryRootPath() string {
	workingDirectory, _ := os.Getwd()
	var callerFile string
	if _, currentFile, _, ok := runtime.Caller(0); ok {
		callerFile = currentFile
	}
	return repositoryRootPathFrom(workingDirectory, callerFile)
}

func repositoryRootPathFrom(workingDirectory, callerFile string) string {
	if workingDirectory != "" {
		if root, ok := findRepositoryRoot(workingDirectory); ok {
			return root
		}
	}
	if filepath.IsAbs(callerFile) {
		return filepath.Clean(filepath.Join(filepath.Dir(callerFile), "../../../"))
	}
	return "."
}

func findRepositoryRoot(start string) (string, bool) {
	absoluteStart, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	directory := filepath.Clean(absoluteStart)
	for {
		workFile := filepath.Join(directory, "go.work")
		if info, err := os.Stat(workFile); err == nil && !info.IsDir() {
			return directory, true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", false
		}
		directory = parent
	}
}

// repoPathFromHere retains the package-relative path helper used by the
// validator tests and keeps fixture data lookup anchored to this source tree.
func repoPathFromHere(rel string) string {
	_, currentFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(currentFile) {
		return filepath.Clean(filepath.Join(filepath.Dir(currentFile), rel))
	}
	return filepath.Join(repositoryRootPath(), filepath.FromSlash(sessionFixtureValidatorPackageRelPath), filepath.FromSlash(rel))
}

// relativeFixtureFiles scans paths with the same recursive WalkDir semantics as
// ValidatePaths and returns every discovered fixture as a slash-separated,
// sorted path relative to the longest common directory of the inputs. It is a
// generic helper for tests; committed-root emission uses
// relativeFixtureFilesFromRepositoryRoots so its paths are always rooted at
// the checkout.
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
	for _, fixturePath := range paths {
		absPath, absErr := filepath.Abs(fixturePath)
		if absErr != nil {
			return nil, "", fmt.Errorf("resolve %s: %w", fixturePath, absErr)
		}
		if info, statErr := os.Stat(absPath); statErr == nil && !info.IsDir() {
			absPath = filepath.Dir(absPath)
		}
		anchors = append(anchors, absPath)
	}
	base = longestCommonDir(anchors)
	for _, fixturePath := range collected {
		absFile, absErr := filepath.Abs(fixturePath)
		if absErr != nil {
			return nil, "", fmt.Errorf("resolve %s: %w", fixturePath, absErr)
		}
		relPath, relErr := filepath.Rel(base, absFile)
		if relErr != nil {
			return nil, "", fmt.Errorf("relativize %s against %s: %w", fixturePath, base, relErr)
		}
		files = append(files, filepath.ToSlash(relPath))
	}
	sort.Strings(files)
	if duplicate := firstDuplicate(files); duplicate != "" {
		return nil, "", fmt.Errorf("duplicate fixture discovery for %q", duplicate)
	}
	return files, base, nil
}

// relativeFixtureFilesFromRepositoryRoots scans the complete authoritative
// root set and emits canonical repository-relative paths. It validates all
// roots before scanning and rejects duplicate discovery rather than silently
// collapsing it into a set.
func relativeFixtureFilesFromRepositoryRoots(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("scan committed fixture roots: at least one root is required")
	}
	collected, err := collectSessionFixtureFiles(roots)
	if err != nil {
		return nil, err
	}
	base := repositoryRootPath()
	files := make([]string, 0, len(collected))
	for _, fixturePath := range collected {
		absFile, absErr := filepath.Abs(fixturePath)
		if absErr != nil {
			return nil, fmt.Errorf("resolve %s: %w", fixturePath, absErr)
		}
		relPath, relErr := filepath.Rel(base, absFile)
		if relErr != nil {
			return nil, fmt.Errorf("relativize %s against repository root %s: %w", fixturePath, base, relErr)
		}
		relPath = filepath.ToSlash(relPath)
		if err := validateCanonicalManifestPath(relPath); err != nil {
			return nil, fmt.Errorf("fixture %s is outside the repository scope: %w", fixturePath, err)
		}
		if !pathBelongsToRegisteredRoot(relPath, authoritativeCommittedFixtureRootRelPaths()) {
			return nil, fmt.Errorf("fixture %s is outside the registered committed-fixture roots", relPath)
		}
		files = append(files, relPath)
	}
	sort.Strings(files)
	if duplicate := firstDuplicate(files); duplicate != "" {
		return nil, fmt.Errorf("duplicate fixture discovery for %q", duplicate)
	}
	return files, nil
}

// buildFixtureManifest scans paths and returns a manifest whose Files are
// slash-separated and sorted. The count is derived from the emitted entries.
func buildFixtureManifest(paths []string) (fixtureManifest, error) {
	files, _, err := relativeFixtureFiles(paths)
	if err != nil {
		return fixtureManifest{}, err
	}
	return fixtureManifest{Count: len(files), Files: files}, nil
}

func buildCommittedFixtureManifest(roots []string) (fixtureManifest, error) {
	files, err := relativeFixtureFilesFromRepositoryRoots(roots)
	if err != nil {
		return fixtureManifest{}, err
	}
	return fixtureManifest{Count: len(files), Files: files}, nil
}

// render encodes the manifest deterministically: fixed field order, two-space
// indent, sorted relative paths, no timestamps or absolute paths, and a
// trailing newline. Count is deliberately recomputed from Files so it cannot
// become a second hand-maintained source of truth.
func (m fixtureManifest) render() ([]byte, error) {
	files := append([]string(nil), m.Files...)
	if files == nil {
		files = []string{}
	}
	if err := validateManifestFileEntries(files); err != nil {
		return nil, err
	}
	canonical := fixtureManifest{Count: len(files), Files: files}
	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode fixture manifest: %w", err)
	}
	return append(data, '\n'), nil
}

// loadFixtureManifest reads and validates a manifest written by render.
func loadFixtureManifest(manifestPath string) (fixtureManifest, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fixtureManifest{}, fmt.Errorf("read fixture manifest %s: %w", manifestPath, err)
	}
	var document struct {
		Count *int      `json:"count"`
		Files *[]string `json:"files"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fixtureManifest{}, fmt.Errorf("parse fixture manifest %s: %w", manifestPath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fixtureManifest{}, fmt.Errorf("parse fixture manifest %s: trailing JSON value", manifestPath)
		}
		return fixtureManifest{}, fmt.Errorf("parse fixture manifest %s: trailing data: %w", manifestPath, err)
	}
	if document.Count == nil || document.Files == nil {
		return fixtureManifest{}, fmt.Errorf("fixture manifest %s must contain non-null count and files fields", manifestPath)
	}
	manifest := fixtureManifest{Count: *document.Count, Files: *document.Files}
	if err := validateLoadedFixtureManifest(manifest); err != nil {
		return fixtureManifest{}, fmt.Errorf("validate fixture manifest %s: %w", manifestPath, err)
	}
	return manifest, nil
}

func validateLoadedFixtureManifest(manifest fixtureManifest) error {
	if manifest.Count < 0 {
		return fmt.Errorf("count %d must not be negative", manifest.Count)
	}
	if err := validateManifestFileEntries(manifest.Files); err != nil {
		return err
	}
	if manifest.Count != len(manifest.Files) {
		return fmt.Errorf("count %d does not equal %d unique file entries", manifest.Count, len(manifest.Files))
	}
	return nil
}

// validateCommittedFixtureManifest checks that a structurally valid manifest
// contains only repository-relative paths beneath the authoritative roots.
func validateCommittedFixtureManifest(manifest fixtureManifest) error {
	roots := authoritativeCommittedFixtureRootRelPaths()
	for _, file := range manifest.Files {
		if !pathBelongsToRegisteredRoot(file, roots) {
			return fmt.Errorf("manifest entry %q is outside the registered committed-fixture roots", file)
		}
	}
	return nil
}

// compareFixtureManifest checks the live inventory against a structurally
// validated manifest. Keeping this comparison separate makes the add/delete/
// rename behavior testable with controlled fixture trees while the committed
// check supplies the authoritative roots and repository-relative paths.
func compareFixtureManifest(manifestRef string, scanned int, live []string, manifest fixtureManifest) error {
	if err := validateLoadedFixtureManifest(manifest); err != nil {
		return formatManifestIntegrityFailure(manifestRef, err)
	}
	if duplicates := duplicatePaths(live); len(duplicates) > 0 {
		return formatManifestIntegrityFailure(manifestRef, fmt.Errorf("duplicate live fixture discovery: %s", strings.Join(duplicates, ", ")))
	}
	unregistered, stale := diffFixtureSets(live, manifest.Files)
	if len(unregistered) > 0 || len(stale) > 0 || scanned != manifest.Count {
		return fmt.Errorf("%s", formatManifestDrift(manifestRef, scanned, manifest.Count, unregistered, stale))
	}
	return nil
}

func verifyCommittedFixtureManifest(manifestPath string, roots []string, scanned int) error {
	manifest, err := loadFixtureManifest(manifestPath)
	if err != nil {
		return formatManifestIntegrityFailure(committedFixtureManifestRelPath, err)
	}
	if err := validateCommittedFixtureManifest(manifest); err != nil {
		return formatManifestIntegrityFailure(committedFixtureManifestRelPath, err)
	}
	live, err := relativeFixtureFilesFromRepositoryRoots(roots)
	if err != nil {
		return formatManifestIntegrityFailure(committedFixtureManifestRelPath, err)
	}
	return compareFixtureManifest(committedFixtureManifestRelPath, scanned, live, manifest)
}

func formatManifestIntegrityFailure(manifestRef string, err error) error {
	return fmt.Errorf("committed session fixture manifest %s is invalid: %v\nif the fixture change is intentional, regenerate the manifest from the repository root with:\n  %s", manifestRef, err, regenerateFixtureManifestCommand)
}

func validateManifestFileEntries(files []string) error {
	seen := make(map[string]struct{}, len(files))
	previous := ""
	for index, file := range files {
		if err := validateCanonicalManifestPath(file); err != nil {
			return fmt.Errorf("manifest entry %d %q: %w", index, file, err)
		}
		if _, exists := seen[file]; exists {
			return fmt.Errorf("duplicate manifest entry %q", file)
		}
		if index > 0 && file <= previous {
			return fmt.Errorf("manifest entries must be in lexical order: %q follows %q", file, previous)
		}
		seen[file] = struct{}{}
		previous = file
	}
	return nil
}

func duplicatePaths(values []string) []string {
	counts := make(map[string]int, len(values))
	for _, value := range values {
		counts[value]++
	}
	duplicates := make([]string, 0)
	for value, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, value)
		}
	}
	sort.Strings(duplicates)
	return duplicates
}

func validateCanonicalManifestPath(file string) error {
	if file == "" {
		return fmt.Errorf("path must not be empty")
	}
	if strings.Contains(file, "\\") {
		return fmt.Errorf("path must use slash separators")
	}
	if path.IsAbs(file) || filepath.IsAbs(file) {
		return fmt.Errorf("path must be repository-relative")
	}
	if strings.Contains(file, "//") || strings.HasPrefix(file, "./") || path.Clean(file) != file {
		return fmt.Errorf("path is not canonical")
	}
	for _, component := range strings.Split(file, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("path is not canonical")
		}
	}
	if !strings.HasSuffix(file, sessionCaptureSuffix) {
		return fmt.Errorf("path must name a %s file", sessionCaptureSuffix)
	}
	return nil
}

func pathBelongsToRegisteredRoot(file string, roots []string) bool {
	for _, root := range roots {
		if file == root || strings.HasPrefix(file, root+"/") {
			return true
		}
	}
	return false
}

// diffFixtureSets compares the live scan against the registered manifest and
// returns drifted paths in lexical order: unregistered exist on disk but not
// in the manifest; stale are registered but missing on disk.
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
	sort.Strings(unregistered)
	sort.Strings(stale)
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

// validateCommittedFixtureRootArguments accepts exactly the authoritative
// roots, in any order, and rejects omission, duplication, overlap, missing
// roots, and paths outside the supported repository scope before any output is
// written.
func validateCommittedFixtureRootArguments(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("-emit-manifest requires at least one committed-fixture root")
	}
	authoritative := allCommittedFixtureRoots()
	expected := make(map[string]string, len(authoritative))
	for _, root := range authoritative {
		canonical, err := canonicalDirectoryPath(root)
		if err != nil {
			return nil, fmt.Errorf("authoritative committed-fixture root %q is unavailable: %w", root, err)
		}
		expected[canonical] = root
	}
	seen := make(map[string]struct{}, len(paths))
	for _, root := range paths {
		canonical, err := canonicalDirectoryPath(root)
		if err != nil {
			return nil, fmt.Errorf("committed-fixture root %q is unavailable: %w", root, err)
		}
		if _, duplicate := seen[canonical]; duplicate {
			return nil, fmt.Errorf("duplicate committed-fixture root %q", root)
		}
		seen[canonical] = struct{}{}
		if _, supported := expected[canonical]; !supported {
			return nil, fmt.Errorf("committed-fixture root %q is outside the supported repository scope; expected one of %v", root, authoritativeCommittedFixtureRootRelPaths())
		}
	}
	var missing []string
	for canonical, root := range expected {
		if _, present := seen[canonical]; !present {
			missing = append(missing, root)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing authoritative committed-fixture root(s): %s", strings.Join(missing, ", "))
	}
	canonicalProvided := make([]string, 0, len(seen))
	for canonical := range seen {
		canonicalProvided = append(canonicalProvided, canonical)
	}
	for i := range canonicalProvided {
		for j := i + 1; j < len(canonicalProvided); j++ {
			if pathsOverlap(canonicalProvided[i], canonicalProvided[j]) {
				return nil, fmt.Errorf("overlapping committed-fixture roots %q and %q would duplicate discovery", canonicalProvided[i], canonicalProvided[j])
			}
		}
	}
	// Scan in registry order, so the emitted repository-relative paths are
	// independent of the argument order supplied by the caller.
	return authoritative, nil
}

func canonicalDirectoryPath(directory string) (string, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return "", fmt.Errorf("missing directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	absPath, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func pathsOverlap(first, second string) bool {
	return first == second || pathWithin(first, second) || pathWithin(second, first)
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func firstDuplicate(values []string) string {
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return values[index]
		}
	}
	return ""
}

// longestCommonDir returns the component-wise longest common prefix of
// already-absolute directory paths.
func longestCommonDir(absDirs []string) string {
	parts := splitSlash(absDirs[0])
	for _, directory := range absDirs[1:] {
		other := splitSlash(directory)
		n := 0
		for n < len(parts) && n < len(other) && parts[n] == other[n] {
			n++
		}
		parts = parts[:n]
	}
	return filepath.Clean(strings.Join(parts, "/"))
}

func splitSlash(pathValue string) []string {
	return strings.Split(filepath.ToSlash(filepath.Clean(pathValue)), "/")
}
