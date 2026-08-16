package functional

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
		tests, err := listPackageTests(ctx, moduleRoot, listed.ImportPath)
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

func listPackageTests(ctx context.Context, moduleRoot, packagePath string) ([]string, error) {
	args := goCommandArgs("test", "-list", "^Test", "-count=1", packagePath)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = moduleRoot
	cmd.Env = setEnv(os.Environ(), DiscoveryEnv, "1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list functional tests in %s: %w", packagePath, commandError(cmd, err, output))
	}

	seen := make(map[string]struct{})
	var tests []string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if !isTopLevelTestName(name) {
			continue
		}
		if _, exists := seen[name]; exists {
			return nil, &ValidationError{
				Field:    "inventory.tests",
				Selector: packagePath + "/" + name,
				Problem:  "is ambiguous because the test is duplicated",
			}
		}
		seen[name] = struct{}{}
		tests = append(tests, name)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read functional test listing for %s: %w", packagePath, err)
	}
	sort.Strings(tests)
	return tests, nil
}

// RunDiscovered applies the configured manifest to the real discovered
// concern packages and invokes each selected top-level test in a child Go
// test process. A child selector is marked as already selected so concern
// package TestMain hooks cannot accidentally re-enable a quarantined test.
func RunDiscovered(ctx context.Context, moduleRoot string, output io.Writer) (Report, error) {
	if output == nil {
		output = io.Discard
	}
	manifest, err := ReadConfiguredManifest()
	if err != nil {
		return Report{}, err
	}
	inventory, err := DiscoverFunctionalInventory(ctx, moduleRoot)
	if err != nil {
		return Report{}, err
	}
	selection, err := Select(manifest, inventory)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Discovered:           len(selection.Discovered),
		Quarantined:          len(selection.Discovered) - len(selection.Selected),
		QuarantineEntryCount: len(selection.Quarantined),
	}
	for _, record := range selection.Quarantined {
		if _, err := fmt.Fprintf(
			output,
			"quarantine: selector=%s bucket=%s reason=%q exitCondition=%q count=%d observed=skip\n",
			record.Entry.Selector(),
			record.Entry.Bucket,
			record.Entry.Reason,
			record.Entry.ExitCondition,
			len(record.Tests),
		); err != nil {
			return report, fmt.Errorf("write quarantine report: %w", err)
		}
	}

	byPackage := make(map[string][]TestSelector)
	for _, selector := range selection.Selected {
		byPackage[selector.Package] = append(byPackage[selector.Package], selector)
	}
	packages := make([]string, 0, len(byPackage))
	for packagePath := range byPackage {
		packages = append(packages, packagePath)
	}
	sort.Strings(packages)

	var firstErr error
	for _, packagePath := range packages {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		selectors := byPackage[packagePath]
		err := runFunctionalPackage(ctx, moduleRoot, packagePath, selectors)
		report.Executed += len(selectors)
		if err != nil {
			report.Failed += len(selectors)
			if firstErr == nil {
				firstErr = &ExecutionError{Selector: selectors[0].String(), Err: err}
			}
			for _, selector := range selectors {
				if _, writeErr := fmt.Fprintf(output, "functional: selector=%s observed=fail reason=%q\n", selector, err); writeErr != nil {
					return report, fmt.Errorf("write failed selector report: %w", writeErr)
				}
			}
			continue
		}
		report.Passed += len(selectors)
		for _, selector := range selectors {
			if _, writeErr := fmt.Fprintf(output, "functional: selector=%s observed=pass\n", selector); writeErr != nil {
				return report, fmt.Errorf("write passed selector report: %w", writeErr)
			}
		}
	}
	if _, err := fmt.Fprintln(output, report.Summary()); err != nil {
		return report, fmt.Errorf("write functional summary: %w", err)
	}
	return report, firstErr
}

func runFunctionalPackage(ctx context.Context, moduleRoot, packagePath string, selectors []TestSelector) error {
	cmd := exec.CommandContext(ctx, "go", functionalPackageTestCommandArgs(packagePath, selectors)...)
	cmd.Dir = moduleRoot
	cmd.Env = setEnv(os.Environ(), SelectionAppliedEnv, "1")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return commandError(cmd, err, output.Bytes())
	}
	return nil
}

func functionalPackageTestCommandArgs(packagePath string, selectors []TestSelector) []string {
	return goCommandArgs(
		"test",
		"-run", selectorRunPattern(selectors),
		"-count=1",
		packagePath,
	)
}

func selectorRunPattern(selectors []TestSelector) string {
	names := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		names = append(names, regexp.QuoteMeta(selector.Test))
	}
	return "^(" + strings.Join(names, "|") + ")$"
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
	if os.Getenv(SelectionAppliedEnv) == "1" || os.Getenv(DiscoveryEnv) == "1" {
		os.Exit(m.Run())
	}
	if !flag.Parsed() {
		flag.Parse()
	}

	manifest, err := ReadConfiguredManifest()
	if err != nil {
		writePackageRunnerError(err)
		os.Exit(1)
	}
	if len(manifest.Entries) == 0 {
		os.Exit(m.Run())
	}

	moduleRoot, err := functionalModuleRootPath()
	if err != nil {
		writePackageRunnerError(err)
		os.Exit(1)
	}
	inventory, err := DiscoverFunctionalInventory(context.Background(), moduleRoot)
	if err != nil {
		writePackageRunnerError(err)
		os.Exit(1)
	}
	if _, err := Select(manifest, inventory); err != nil {
		writePackageRunnerError(err)
		os.Exit(1)
	}

	var localEntries []Entry
	for _, entry := range manifest.Entries {
		if entry.Package == packagePath {
			localEntries = append(localEntries, entry)
		}
	}
	if len(localEntries) == 0 {
		os.Exit(m.Run())
	}

	var currentPackage InventoryPackage
	for _, discoveredPackage := range inventory.Packages {
		if discoveredPackage.Path == packagePath {
			currentPackage = discoveredPackage
			break
		}
	}
	if currentPackage.Path == "" {
		writePackageRunnerError(&ValidationError{
			Field:    "inventory.packages",
			Selector: packagePath,
			Problem:  "does not resolve to the current discovered package",
		})
		os.Exit(1)
	}
	selection, err := Select(
		Manifest{Version: manifest.Version, Suite: manifest.Suite, Entries: localEntries},
		Inventory{Packages: []InventoryPackage{currentPackage}},
	)
	if err != nil {
		writePackageRunnerError(err)
		os.Exit(1)
	}
	for _, record := range selection.Quarantined {
		if _, err := fmt.Fprintf(
			os.Stdout,
			"quarantine: selector=%s bucket=%s reason=%q exitCondition=%q count=%d observed=skip\n",
			record.Entry.Selector(),
			record.Entry.Bucket,
			record.Entry.Reason,
			record.Entry.ExitCondition,
			len(record.Tests),
		); err != nil {
			writePackageRunnerError(fmt.Errorf("write quarantine report: %w", err))
			os.Exit(1)
		}
	}

	pattern, err := packageRunPattern(selection.Selected)
	if err != nil {
		writePackageRunnerError(err)
		os.Exit(1)
	}
	if err := flag.CommandLine.Set("test.run", pattern); err != nil {
		writePackageRunnerError(fmt.Errorf("set package test selection: %w", err))
		os.Exit(1)
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
		os.Exit(1)
	}
	os.Exit(exitCode)
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
