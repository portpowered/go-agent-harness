package functional

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath   string
	Dir          string
	TestGoFiles  []string
	XTestGoFiles []string
}

// DiscoverFunctionalInventory finds the concern-oriented Go test packages
// and lists their exact top-level tests. Internal fixture packages and the
// manifest contract package are intentionally not suite members.
func DiscoverFunctionalInventory(ctx context.Context, moduleRoot string) (Inventory, error) {
	cmd := exec.CommandContext(ctx, "go", goCommandArgs("list", "-json", "./test/functional/...")...)
	cmd.Dir = moduleRoot
	output, err := cmd.Output()
	if err != nil {
		return Inventory{}, fmt.Errorf("discover functional packages: %w", commandError(cmd, err, output))
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	var inventory Inventory
	for {
		var listed goListPackage
		if err := decoder.Decode(&listed); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Inventory{}, fmt.Errorf("decode functional package discovery: %w", err)
		}
		if !isFunctionalTestPackage(listed) {
			continue
		}
		tests, err := listPackageTests(listed)
		if err != nil {
			return Inventory{}, err
		}
		if len(tests) == 0 {
			continue
		}
		inventory.Packages = append(inventory.Packages, InventoryPackage{
			Path:  listed.ImportPath,
			Tests: tests,
		})
	}
	sort.Slice(inventory.Packages, func(i, j int) bool {
		return inventory.Packages[i].Path < inventory.Packages[j].Path
	})
	return inventory, nil
}

func isFunctionalTestPackage(listed goListPackage) bool {
	if listed.ImportPath == "" || len(listed.TestGoFiles)+len(listed.XTestGoFiles) == 0 {
		return false
	}
	if strings.HasSuffix(listed.ImportPath, "/test/functional") {
		return false
	}
	return !strings.Contains(listed.ImportPath, "/test/functional/internal/")
}

// listPackageTests returns the package's sorted top-level tests. It reads
// the test files `go list` resolved for the active build tags instead of
// linking a test binary for `go test -list`, which made discovery cost one
// link per package.
func listPackageTests(listed goListPackage) ([]string, error) {
	files := make([]string, 0, len(listed.TestGoFiles)+len(listed.XTestGoFiles))
	files = append(files, listed.TestGoFiles...)
	files = append(files, listed.XTestGoFiles...)
	fileSet := token.NewFileSet()
	seen := make(map[string]struct{})
	var tests []string
	for _, name := range files {
		parsed, err := parser.ParseFile(fileSet, filepath.Join(listed.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("list functional tests in %s: %w", listed.ImportPath, err)
		}
		for _, decl := range parsed.Decls {
			testName, ok := topLevelTestFunc(decl)
			if !ok {
				continue
			}
			if _, exists := seen[testName]; exists {
				return nil, &ValidationError{
					Field:    "inventory.tests",
					Selector: listed.ImportPath + "/" + testName,
					Problem:  "is ambiguous because the test is duplicated",
				}
			}
			seen[testName] = struct{}{}
			tests = append(tests, testName)
		}
	}
	sort.Strings(tests)
	return tests, nil
}

// topLevelTestFunc reports the name of a `func TestXxx(t *testing.T)`
// declaration, the shape `go test` runs as a top-level test.
func topLevelTestFunc(decl ast.Decl) (string, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv != nil || fn.Type.TypeParams != nil || !isTopLevelTestName(fn.Name.Name) {
		return "", false
	}
	params := fn.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 || fn.Type.Results != nil {
		return "", false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	return fn.Name.Name, ok && selector.Sel.Name == "T"
}

func goCommandArgs(command string, args ...string) []string {
	commandArgs := []string{command}
	if tags := strings.TrimSpace(os.Getenv(GoTagsEnv)); tags != "" {
		commandArgs = append(commandArgs, "-tags", tags)
	}
	return append(commandArgs, args...)
}

func commandError(cmd *exec.Cmd, err error, output []byte) error {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

// RunPackageTests is called by a concern package's TestMain. It applies only
// selectors for that package and rewrites the testing run filter to the exact
// discovered runnable set. The root runner validates all package selectors;
// this hook is what makes direct `go test ./test/functional/...` invocations
// honor the same subtractive manifest.
func RunPackageTests(m *testing.M, packagePath string) {
	if os.Getenv(SelectionAppliedEnv) == "1" {
		os.Exit(m.Run())
	}
	if !flag.Parsed() {
		flag.Parse()
	}
	os.Exit(runSelectedPackageTests(m, packagePath))
}

// runSelectedPackageTests runs one package under its manifest selection and
// returns the process exit code.
func runSelectedPackageTests(m *testing.M, packagePath string) int {
	selection, selected, err := selectPackageTests(packagePath)
	if err == nil && selected {
		err = applyPackageSelection(selection)
	}
	if err != nil {
		writePackageRunnerError(err)
		return 1
	}
	if !selected {
		return m.Run()
	}

	exitCode := m.Run()
	report := Report{
		Discovered:           len(selection.Discovered),
		Executed:             len(selection.Selected),
		Quarantined:          len(selection.Discovered) - len(selection.Selected),
		QuarantineEntryCount: len(selection.Quarantined),
	}
	if exitCode == 0 {
		report.Passed = report.Executed
	} else {
		report.Failed = report.Executed
	}
	if _, err := fmt.Fprintln(os.Stdout, report.Summary()); err != nil {
		writePackageRunnerError(fmt.Errorf("write package summary: %w", err))
		return 1
	}
	return exitCode
}

// selectPackageTests validates the configured manifest against the whole
// discovered inventory, then selects this package's tests. selected is false
// when no manifest entry applies to the package.
func selectPackageTests(packagePath string) (Selection, bool, error) {
	manifest, err := ReadConfiguredManifest()
	if err != nil || len(manifest.Entries) == 0 {
		return Selection{}, false, err
	}
	moduleRoot, err := functionalModuleRootPath()
	if err != nil {
		return Selection{}, false, err
	}
	inventory, err := DiscoverFunctionalInventory(context.Background(), moduleRoot)
	if err != nil {
		return Selection{}, false, err
	}
	if _, err := Select(manifest, inventory); err != nil {
		return Selection{}, false, err
	}

	var localEntries []Entry
	for _, entry := range manifest.Entries {
		if entry.Package == packagePath {
			localEntries = append(localEntries, entry)
		}
	}
	if len(localEntries) == 0 {
		return Selection{}, false, nil
	}

	var currentPackage InventoryPackage
	for _, discoveredPackage := range inventory.Packages {
		if discoveredPackage.Path == packagePath {
			currentPackage = discoveredPackage
			break
		}
	}
	if currentPackage.Path == "" {
		return Selection{}, false, &ValidationError{Field: "inventory.packages", Selector: packagePath, Problem: "does not resolve to the current discovered package"}
	}
	localManifest := Manifest{Version: manifest.Version, Suite: manifest.Suite, Entries: localEntries}
	selection, err := Select(localManifest, Inventory{Packages: []InventoryPackage{currentPackage}})
	return selection, err == nil, err
}

// applyPackageSelection reports quarantined selectors and narrows test.run to
// the selected tests.
func applyPackageSelection(selection Selection) error {
	for _, record := range selection.Quarantined {
		if _, err := fmt.Fprintf(os.Stdout, "quarantine: selector=%s bucket=%s reason=%q exitCondition=%q count=%d observed=skip\n",
			record.Entry.Selector(), record.Entry.Bucket, record.Entry.Reason, record.Entry.ExitCondition, len(record.Tests)); err != nil {
			return fmt.Errorf("write quarantine report: %w", err)
		}
	}
	pattern, err := packageRunPattern(selection.Selected)
	if err != nil {
		return err
	}
	if err := flag.CommandLine.Set("test.run", pattern); err != nil {
		return fmt.Errorf("set package test selection: %w", err)
	}
	return nil
}

func packageRunPattern(selected []TestSelector) (string, error) {
	allowed := make(map[string]struct{}, len(selected))
	for _, selector := range selected {
		allowed[selector.Test] = struct{}{}
	}
	var names []string
	if runFlag := flag.CommandLine.Lookup("test.run"); runFlag != nil {
		original := runFlag.Value.String()
		if original != "" {
			filter, err := regexp.Compile(original)
			if err != nil {
				return "", fmt.Errorf("compile existing test.run filter %q: %w", original, err)
			}
			for name := range allowed {
				if filter.MatchString(name) {
					names = append(names, name)
				}
			}
		} else {
			for name := range allowed {
				names = append(names, name)
			}
		}
	} else {
		return "", errors.New("test.run flag is unavailable")
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "a^", nil
	}
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(?:" + strings.Join(quoted, "|") + ")$", nil
}

func setEnv(environment []string, key, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		filtered = append(filtered, item)
	}
	return append(filtered, prefix+value)
}

func writePackageRunnerError(err error) {
	if _, writeErr := fmt.Fprintf(os.Stderr, "functional quarantine runner: %v\n", err); writeErr != nil {
		return
	}
}

func functionalModuleRootPath() (string, error) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..")), nil
}
