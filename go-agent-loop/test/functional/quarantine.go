// Package functional contains the functional-suite manifest contract and the
// small selection runner shared by the module's concern-oriented test trees.
package functional

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	ManifestVersion = 1
	SuiteName       = "functional"

	BucketEnvironmentDependent = "ENVIRONMENT-DEPENDENT"
	BucketGenuinelyFailing     = "GENUINELY FAILING"
)

//go:embed functional-quarantine.json
var manifestFiles embed.FS

// Manifest is the versioned, subtractive functional-suite quarantine
// document. An empty entries array is a valid manifest and selects every
// discovered test.
type Manifest struct {
	Version int     `json:"version"`
	Suite   string  `json:"suite"`
	Entries []Entry `json:"entries"`
}

// Entry identifies either an entire discovered package or one exact top-level
// test in that package.
type Entry struct {
	Package       string `json:"package"`
	Test          string `json:"test,omitempty"`
	Bucket        string `json:"bucket"`
	Reason        string `json:"reason"`
	ExitCondition string `json:"exitCondition"`
}

// Selector returns the stable package[/test] spelling used in diagnostics.
func (e Entry) Selector() string {
	if e.Test == "" {
		return e.Package
	}
	return e.Package + "/" + e.Test
}

// ValidationError identifies a malformed or unresolvable manifest field.
// Callers can use errors.As to distinguish fail-closed manifest errors from
// subprocess or test failures.
type ValidationError struct {
	Field    string
	Selector string
	Problem  string
}

func (e *ValidationError) Error() string {
	where := "field " + fmt.Sprintf("%q", e.Field)
	if e.Selector != "" {
		where += " selector " + fmt.Sprintf("%q", e.Selector)
	}
	return "functional quarantine: " + where + ": " + e.Problem
}

// ManifestError is kept as a descriptive alias for callers that prefer the
// contract terminology; it is the same typed error as ValidationError.
type ManifestError = ValidationError

// ReadManifest reads and strictly parses a quarantine document from disk.
func ReadManifest(path string) (Manifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read functional quarantine %q: %w", path, err)
	}
	return ParseManifest(body)
}

// ReadEmbeddedManifest reads the versioned manifest committed beside this
// package. It is useful for runners that should have a deterministic default.
func ReadEmbeddedManifest() (Manifest, error) {
	body, err := manifestFiles.ReadFile("functional-quarantine.json")
	if err != nil {
		return Manifest{}, fmt.Errorf("read embedded functional quarantine: %w", err)
	}
	return ParseManifest(body)
}

// ParseManifest strictly parses JSON, rejecting unknown fields, trailing JSON,
// a missing/null entries array, and incomplete entry metadata.
func ParseManifest(data []byte) (Manifest, error) {
	var raw struct {
		Version int             `json:"version"`
		Suite   string          `json:"suite"`
		Entries json.RawMessage `json:"entries"`
	}
	if err := decodeOne(data, &raw); err != nil {
		return Manifest{}, &ValidationError{Field: "document", Problem: err.Error()}
	}
	if len(raw.Entries) == 0 || bytes.Equal(bytes.TrimSpace(raw.Entries), []byte("null")) {
		return Manifest{}, &ValidationError{Field: "entries", Problem: "must be an array"}
	}

	var entries []Entry
	if err := decodeOne(raw.Entries, &entries); err != nil {
		return Manifest{}, &ValidationError{Field: "entries", Problem: err.Error()}
	}
	manifest := Manifest{Version: raw.Version, Suite: raw.Suite, Entries: entries}
	if err := validateDocument(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func decodeOne(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validateDocument(manifest Manifest) error {
	if manifest.Version != ManifestVersion {
		return &ValidationError{Field: "version", Problem: fmt.Sprintf("got %d, want %d", manifest.Version, ManifestVersion)}
	}
	if manifest.Suite != SuiteName {
		return &ValidationError{Field: "suite", Problem: fmt.Sprintf("got %q, want %q", manifest.Suite, SuiteName)}
	}
	for i, entry := range manifest.Entries {
		if err := validateEntryShape(entry, fmt.Sprintf("entries[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

func validateEntryShape(entry Entry, field string) error {
	if strings.TrimSpace(entry.Package) == "" {
		return &ValidationError{Field: field + ".package", Problem: "is required"}
	}
	if strings.TrimSpace(entry.Package) != entry.Package || strings.ContainsAny(entry.Package, "*?[]") || strings.Contains(entry.Package, "...") {
		return &ValidationError{Field: field + ".package", Selector: entry.Selector(), Problem: "must be one exact package path"}
	}
	if entry.Test != "" && !isTopLevelTestName(entry.Test) {
		return &ValidationError{Field: field + ".test", Selector: entry.Selector(), Problem: "must be an exact top-level test name"}
	}
	if entry.Bucket != BucketEnvironmentDependent && entry.Bucket != BucketGenuinelyFailing {
		return &ValidationError{Field: field + ".bucket", Selector: entry.Selector(), Problem: fmt.Sprintf("unsupported bucket %q", entry.Bucket)}
	}
	if strings.TrimSpace(entry.Reason) == "" {
		return &ValidationError{Field: field + ".reason", Selector: entry.Selector(), Problem: "must be non-empty"}
	}
	if strings.TrimSpace(entry.ExitCondition) == "" {
		return &ValidationError{Field: field + ".exitCondition", Selector: entry.Selector(), Problem: "must be non-empty"}
	}
	return nil
}

func isTopLevelTestName(name string) bool {
	if len(name) < len("TestX") || !strings.HasPrefix(name, "Test") {
		return false
	}
	first := name[len("Test")]
	return first >= 'A' && first <= 'Z'
}

// InventoryPackage describes one discovered functional package and its exact
// top-level tests. Package and test paths are intentionally explicit rather
// than glob patterns so stale or ambiguous quarantines fail closed.
type InventoryPackage struct {
	Path  string
	Tests []string
}

// Inventory is the discovery result consumed by ValidateManifest and Select.
type Inventory struct {
	Packages []InventoryPackage
}

// ValidateManifest validates manifest metadata and resolves every selector
// against the discovered inventory. It rejects duplicate and overlapping
// selectors before any test is selected.
func ValidateManifest(manifest Manifest, inventory Inventory) error {
	if err := validateDocument(manifest); err != nil {
		return err
	}
	packages, err := indexInventory(inventory)
	if err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(manifest.Entries))
	packageSelectors := make(map[string]string)
	for i, entry := range manifest.Entries {
		selector := entry.Selector()
		key := selector
		if _, exists := seen[key]; exists {
			return &ValidationError{Field: fmt.Sprintf("entries[%d]", i), Selector: selector, Problem: "duplicate selector"}
		}
		seen[key] = struct{}{}

		pkg, exists := packages[entry.Package]
		if !exists {
			return &ValidationError{Field: fmt.Sprintf("entries[%d].package", i), Selector: selector, Problem: "does not resolve to a discovered package"}
		}
		if entry.Test == "" {
			packageSelectors[entry.Package] = selector
			continue
		}
		if _, exists := pkg.tests[entry.Test]; !exists {
			return &ValidationError{Field: fmt.Sprintf("entries[%d].test", i), Selector: selector, Problem: "does not resolve to a discovered top-level test"}
		}
		if packageSelector, overlaps := packageSelectors[entry.Package]; overlaps {
			return &ValidationError{Field: fmt.Sprintf("entries[%d]", i), Selector: selector, Problem: fmt.Sprintf("overlaps package selector %q", packageSelector)}
		}
	}
	for i, entry := range manifest.Entries {
		if entry.Test == "" {
			continue
		}
		if packageSelector, overlaps := packageSelectors[entry.Package]; overlaps {
			return &ValidationError{Field: fmt.Sprintf("entries[%d]", i), Selector: entry.Selector(), Problem: fmt.Sprintf("overlaps package selector %q", packageSelector)}
		}
	}
	return nil
}

type indexedPackage struct {
	tests map[string]struct{}
}

func indexInventory(inventory Inventory) (map[string]indexedPackage, error) {
	packages := make(map[string]indexedPackage, len(inventory.Packages))
	for i, pkg := range inventory.Packages {
		if strings.TrimSpace(pkg.Path) == "" {
			return nil, &ValidationError{Field: fmt.Sprintf("inventory.packages[%d].path", i), Problem: "is required"}
		}
		if _, exists := packages[pkg.Path]; exists {
			return nil, &ValidationError{Field: fmt.Sprintf("inventory.packages[%d].path", i), Selector: pkg.Path, Problem: "is ambiguous because the package is duplicated"}
		}
		tests := make(map[string]struct{}, len(pkg.Tests))
		for j, testName := range pkg.Tests {
			if !isTopLevelTestName(testName) {
				return nil, &ValidationError{Field: fmt.Sprintf("inventory.packages[%d].tests[%d]", i, j), Selector: pkg.Path + "/" + testName, Problem: "is not an exact top-level test name"}
			}
			if _, exists := tests[testName]; exists {
				return nil, &ValidationError{Field: fmt.Sprintf("inventory.packages[%d].tests[%d]", i, j), Selector: pkg.Path + "/" + testName, Problem: "is ambiguous because the test is duplicated"}
			}
			tests[testName] = struct{}{}
		}
		packages[pkg.Path] = indexedPackage{tests: tests}
	}
	return packages, nil
}

// TestSelector identifies one runnable top-level test.
type TestSelector struct {
	Package string
	Test    string
}

func (s TestSelector) String() string {
	if s.Test == "" {
		return s.Package
	}
	return s.Package + "/" + s.Test
}

// QuarantinedSelector records one manifest entry and the discovered tests it
// subtracts from the runnable selection.
type QuarantinedSelector struct {
	Entry Entry
	Tests []TestSelector
}

// Selection is the deterministic subtractive result of applying a manifest.
type Selection struct {
	Discovered  []TestSelector
	Selected    []TestSelector
	Quarantined []QuarantinedSelector
}

// Select validates the contract and returns all discovered tests partitioned
// into selected and explicitly quarantined selectors.
func Select(manifest Manifest, inventory Inventory) (Selection, error) {
	if err := ValidateManifest(manifest, inventory); err != nil {
		return Selection{}, err
	}
	packages, err := indexInventory(inventory)
	if err != nil {
		return Selection{}, err
	}

	all := make([]TestSelector, 0)
	for packagePath, pkg := range packages {
		for testName := range pkg.tests {
			all = append(all, TestSelector{Package: packagePath, Test: testName})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].String() < all[j].String() })

	quarantinedTests := make(map[string]struct{})
	quarantined := make([]QuarantinedSelector, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		record := QuarantinedSelector{Entry: entry}
		for _, selector := range all {
			if selector.Package != entry.Package || (entry.Test != "" && selector.Test != entry.Test) {
				continue
			}
			record.Tests = append(record.Tests, selector)
			quarantinedTests[selector.String()] = struct{}{}
		}
		quarantined = append(quarantined, record)
	}
	selected := make([]TestSelector, 0, len(all)-len(quarantinedTests))
	for _, selector := range all {
		if _, quarantined := quarantinedTests[selector.String()]; !quarantined {
			selected = append(selected, selector)
		}
	}
	return Selection{Discovered: all, Selected: selected, Quarantined: quarantined}, nil
}

// Report contains exact runtime counts for one selected functional run.
type Report struct {
	Discovered           int
	Executed             int
	Passed               int
	Failed               int
	Quarantined          int
	QuarantineEntryCount int
}

// Summary returns the stable one-line count diagnostic emitted by Run.
func (r Report) Summary() string {
	return fmt.Sprintf("summary: discovered=%d executed=%d passed=%d failed=%d quarantined=%d", r.Discovered, r.Executed, r.Passed, r.Failed, r.Quarantined)
}

// ExecutionError identifies a selected test whose executor failed.
type ExecutionError struct {
	Selector string
	Err      error
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("functional selector %q failed: %v", e.Selector, e.Err)
}

func (e *ExecutionError) Unwrap() error { return e.Err }

// TestExecutor runs one selected test. A quarantined selector is never passed
// to the executor.
type TestExecutor func(context.Context, TestSelector) error

// Run applies the manifest, reports every quarantine entry, and executes only
// the selected tests. Validation happens before the first callback, so invalid
// manifests fail closed without a partial run.
func Run(ctx context.Context, manifest Manifest, inventory Inventory, execute TestExecutor, output io.Writer) (Report, error) {
	if execute == nil {
		return Report{}, &ValidationError{Field: "executor", Problem: "is required"}
	}
	selection, err := Select(manifest, inventory)
	if err != nil {
		return Report{}, err
	}
	if output == nil {
		output = io.Discard
	}

	report := Report{
		Discovered:           len(selection.Discovered),
		Quarantined:          0,
		QuarantineEntryCount: len(selection.Quarantined),
	}
	for _, record := range selection.Quarantined {
		report.Quarantined += len(record.Tests)
		fmt.Fprintf(output, "quarantine: selector=%s bucket=%s reason=%q exitCondition=%q count=%d observed=skip\n", record.Entry.Selector(), record.Entry.Bucket, record.Entry.Reason, record.Entry.ExitCondition, len(record.Tests))
	}

	var firstErr error
	for _, selector := range selection.Selected {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Executed++
		err := execute(ctx, selector)
		if err != nil {
			report.Failed++
			if firstErr == nil {
				firstErr = &ExecutionError{Selector: selector.String(), Err: err}
			}
			fmt.Fprintf(output, "functional: selector=%s observed=fail reason=%q\n", selector, err)
			continue
		}
		report.Passed++
		fmt.Fprintf(output, "functional: selector=%s observed=pass\n", selector)
	}
	fmt.Fprintln(output, report.Summary())
	return report, firstErr
}

// CommandFactory creates an injected subprocess for one selector. The
// context is supplied so callers can enforce a bounded run without global
// processes or credentials.
type CommandFactory func(context.Context, TestSelector) *exec.Cmd

// RunSubprocess is the subprocess adapter for Run. It is deliberately
// factory-driven so contract tests can inject a hermetic fixture and assert
// positive execution signals without invoking live providers.
func RunSubprocess(ctx context.Context, manifest Manifest, inventory Inventory, command CommandFactory, output io.Writer) (Report, error) {
	if command == nil {
		return Report{}, &ValidationError{Field: "command", Problem: "is required"}
	}
	return Run(ctx, manifest, inventory, func(runCtx context.Context, selector TestSelector) error {
		cmd := command(runCtx, selector)
		if cmd == nil {
			return errors.New("command factory returned nil")
		}
		var combined bytes.Buffer
		captureOutput := cmd.Stdout == nil && cmd.Stderr == nil
		if captureOutput {
			cmd.Stdout = &combined
			cmd.Stderr = &combined
		}
		err := cmd.Run()
		if err != nil {
			detail := strings.TrimSpace(combined.String())
			if detail != "" {
				return fmt.Errorf("%w: %s", err, detail)
			}
			return err
		}
		return nil
	}, output)
}
