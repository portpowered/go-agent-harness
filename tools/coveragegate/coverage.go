package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrManifestInvalid             = errors.New("invalid coverage manifest")
	ErrManifestMinimumPrecision    = errors.New("coverage minimum must use exactly two decimal places")
	ErrManifestUnsorted             = errors.New("coverage manifest packages are not strictly sorted")
	ErrManifestBothFields           = errors.New("coverage manifest entry defines both minimum and exception")
	ErrManifestNeitherField          = errors.New("coverage manifest entry defines neither minimum nor exception")
	ErrManifestException             = errors.New("coverage manifest exception must be a string")
	ErrProfileInvalid                = errors.New("invalid Go coverage profile")
	ErrUnregisteredPackage           = errors.New("coverage profile contains an unregistered package")
	ErrMissingCoverage               = errors.New("manifest package has no measured coverage")
	ErrCoverageFloorViolation        = errors.New("measured coverage is below its minimum")
)

// Manifest is the validated, hand-maintained coverage registration set.
type Manifest struct {
	Packages []PackageEntry
}

// PackageEntry is one manifest registration. MinimumCents is a percentage in
// hundredths (40.20% is 4020). Exceptions are registered but do not impose a
// floor or require a measured profile entry.
type PackageEntry struct {
	ImportPath  string
	MinimumCents int
	HasMinimum  bool
	Exception   string
	HasException bool
}

// Coverage is the statement aggregate for one import path.
type Coverage struct {
	Covered int64
	Total   int64
}

// Violation is a deterministic, actionable floor failure.
type Violation struct {
	ImportPath   string
	ExpectedCents int
	ActualCents   int
	DeltaCents    int
}

// FindingsError aggregates every missing package and floor regression found
// in one comparison. It unwraps to typed sentinels so callers can branch on
// the failure category without parsing the rendered report.
type FindingsError struct {
	Unregistered []string
	Unmeasured   []string
	Violations   []Violation
}

func (e *FindingsError) Error() string {
	var sections []string
	if len(e.Unregistered) > 0 {
		var b strings.Builder
		b.WriteString("coverage gate found unregistered packages:")
		for _, packagePath := range e.Unregistered {
			fmt.Fprintf(&b, "\n- %s", packagePath)
		}
		sections = append(sections, b.String())
	}
	if len(e.Unmeasured) > 0 {
		var b strings.Builder
		b.WriteString("coverage gate found manifest packages without measured coverage:")
		for _, packagePath := range e.Unmeasured {
			fmt.Fprintf(&b, "\n- %s", packagePath)
		}
		sections = append(sections, b.String())
	}
	if len(e.Violations) > 0 {
		var b strings.Builder
		b.WriteString("coverage gate found coverage floor violations:")
		for _, violation := range e.Violations {
			fmt.Fprintf(&b, "\n- %s: expected minimum %s%%, actual %s%%, delta %s%%",
				violation.ImportPath,
				formatCents(violation.ExpectedCents),
				formatCents(violation.ActualCents),
				formatSignedCents(violation.DeltaCents),
			)
		}
		sections = append(sections, b.String())
	}
	return strings.Join(sections, "\n")
}

func (e *FindingsError) Unwrap() []error {
	var causes []error
	if len(e.Unregistered) > 0 {
		causes = append(causes, ErrUnregisteredPackage)
	}
	if len(e.Unmeasured) > 0 {
		causes = append(causes, ErrMissingCoverage)
	}
	if len(e.Violations) > 0 {
		causes = append(causes, ErrCoverageFloorViolation)
	}
	return causes
}

type ManifestError struct {
	Kind        error
	ImportPath  string
	Message     string
}

func (e *ManifestError) Error() string { return e.Message }

func (e *ManifestError) Unwrap() error { return e.Kind }

var minimumPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{2}$`)

// ParseManifest validates the JSON and preserves the lexical minimum format
// before converting it to an integer percentage representation.
func ParseManifest(data []byte) (Manifest, error) {
	var document struct {
		Packages []json.RawMessage `json:"packages"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifestInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("%w: manifest contains more than one JSON value", ErrManifestInvalid)
		}
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifestInvalid, err)
	}
	if document.Packages == nil {
		return Manifest{}, fmt.Errorf("%w: packages must be an array", ErrManifestInvalid)
	}

	manifest := Manifest{Packages: make([]PackageEntry, 0, len(document.Packages))}
	for _, rawEntry := range document.Packages {
		entry, err := parseEntry(rawEntry)
		if err != nil {
			return Manifest{}, err
		}
		if len(manifest.Packages) > 0 {
			previous := manifest.Packages[len(manifest.Packages)-1].ImportPath
			if entry.ImportPath <= previous {
				return Manifest{}, &ManifestError{
					Kind:       ErrManifestUnsorted,
					ImportPath: entry.ImportPath,
					Message: fmt.Sprintf(
						"coverage manifest packages must be strictly sorted by import path: %q follows %q",
						entry.ImportPath,
						previous,
					),
				}
			}
		}
		manifest.Packages = append(manifest.Packages, entry)
	}
	return manifest, nil
}

func parseEntry(raw json.RawMessage) (PackageEntry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return PackageEntry{}, fmt.Errorf("%w: package entry must be an object: %v", ErrManifestInvalid, err)
	}

	var importPath string
	packageRaw, ok := fields["package"]
	if !ok || json.Unmarshal(packageRaw, &importPath) != nil || strings.TrimSpace(importPath) == "" {
		return PackageEntry{}, fmt.Errorf("%w: package entry requires a non-empty string package", ErrManifestInvalid)
	}
	minimumRaw, hasMinimum := fields["minimum"]
	exceptionRaw, hasException := fields["exception"]
	if hasMinimum && hasException {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestBothFields,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q must define exactly one of minimum or exception; found both", importPath),
		}
	}
	if !hasMinimum && !hasException {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestNeitherField,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q must define exactly one of minimum or exception; found neither", importPath),
		}
	}
	if hasException {
		var exception string
		if err := json.Unmarshal(exceptionRaw, &exception); err != nil {
			return PackageEntry{}, &ManifestError{
				Kind:       ErrManifestException,
				ImportPath: importPath,
				Message:    fmt.Sprintf("coverage manifest package %q exception must be a string", importPath),
			}
		}
		return PackageEntry{ImportPath: importPath, Exception: exception, HasException: true}, nil
	}

	lexeme := strings.TrimSpace(string(minimumRaw))
	if !minimumPattern.MatchString(lexeme) {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestMinimumPrecision,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q minimum must use exactly two decimal places: got %s", importPath, lexeme),
		}
	}
	whole, err := strconv.Atoi(lexeme[:strings.IndexByte(lexeme, '.')])
	if err != nil {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestMinimumPrecision,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q minimum is not a valid percentage: got %s", importPath, lexeme),
		}
	}
	if whole > 100 {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestMinimumPrecision,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q minimum must be between 0.00 and 100.00: got %s", importPath, lexeme),
		}
	}
	fraction, err := strconv.Atoi(lexeme[len(lexeme)-2:])
	if err != nil {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestMinimumPrecision,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q minimum is not a valid percentage: got %s", importPath, lexeme),
		}
	}
	minimumCents := whole*100 + fraction
	if minimumCents > 10000 {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestMinimumPrecision,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q minimum must be between 0.00 and 100.00: got %s", importPath, lexeme),
		}
	}
	return PackageEntry{ImportPath: importPath, MinimumCents: minimumCents, HasMinimum: true}, nil
}

// ReadProfiles parses explicit Go coverage profile paths and aggregates
// statements by import path. Every profile must use the same coverage mode.
func ReadProfiles(paths []string) (map[string]Coverage, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: no profile paths provided", ErrProfileInvalid)
	}
	measurements := make(map[string]Coverage)
	var mode string
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("%w: open %q: %v", ErrProfileInvalid, path, err)
		}
		profileMode, profileMeasurements, parseErr := parseProfile(file, path)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, parseErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("%w: close %q: %v", ErrProfileInvalid, path, closeErr)
		}
		if mode == "" {
			mode = profileMode
		} else if mode != profileMode {
			return nil, fmt.Errorf("%w: coverage profiles use different modes %q and %q", ErrProfileInvalid, mode, profileMode)
		}
		for packagePath, coverage := range profileMeasurements {
			current := measurements[packagePath]
			current.Covered += coverage.Covered
			current.Total += coverage.Total
			measurements[packagePath] = current
		}
	}
	return measurements, nil
}

func parseProfile(reader io.Reader, name string) (string, map[string]Coverage, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	lineNumber := 0
	mode := ""
	measurements := make(map[string]Coverage)
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if mode == "" {
			if !strings.HasPrefix(line, "mode: ") {
				return "", nil, profileError(name, lineNumber, "first non-empty line must declare mode")
			}
			mode = strings.TrimSpace(strings.TrimPrefix(line, "mode: "))
			if mode == "" {
				return "", nil, profileError(name, lineNumber, "coverage mode is empty")
			}
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 3 {
			return "", nil, profileError(name, lineNumber, "coverage block must contain file range, statements, and count")
		}
		fileRange := fields[0]
		colon := strings.LastIndexByte(fileRange, ':')
		if colon <= 0 || !strings.Contains(fileRange[colon+1:], ",") {
			return "", nil, profileError(name, lineNumber, "coverage block has an invalid file range")
		}
		packagePath := fileRange[:colon]
		if slash := strings.LastIndexByte(packagePath, '/'); slash <= 0 || slash == len(packagePath)-1 {
			return "", nil, profileError(name, lineNumber, "coverage block has an invalid import path")
		} else {
			packagePath = packagePath[:slash]
		}
		statements, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || statements < 0 {
			return "", nil, profileError(name, lineNumber, "statement count is invalid")
		}
		count, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || count < 0 {
			return "", nil, profileError(name, lineNumber, "execution count is invalid")
		}
		if statements == 0 {
			continue
		}
		coverage := measurements[packagePath]
		coverage.Total += statements
		if count > 0 {
			coverage.Covered += statements
		}
		measurements[packagePath] = coverage
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("%w: read %q: %v", ErrProfileInvalid, name, err)
	}
	if mode == "" {
		return "", nil, profileError(name, lineNumber+1, "profile does not declare a coverage mode")
	}
	return mode, measurements, nil
}

func profileError(name string, line int, detail string) error {
	return fmt.Errorf("%w: %s:%d: %s", ErrProfileInvalid, name, line, detail)
}

// Compare validates a manifest and compares all measured package aggregates.
// Findings are accumulated and sorted before being returned.
func Compare(manifest Manifest, measurements map[string]Coverage) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	registered := make(map[string]PackageEntry, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		registered[entry.ImportPath] = entry
	}

	findings := &FindingsError{}
	for packagePath, coverage := range measurements {
		if coverage.Total == 0 {
			continue
		}
		if _, ok := registered[packagePath]; !ok {
			findings.Unregistered = append(findings.Unregistered, packagePath)
		}
	}

	for _, entry := range manifest.Packages {
		if entry.HasException {
			continue
		}
		coverage, ok := measurements[entry.ImportPath]
		if !ok || coverage.Total == 0 {
			findings.Unmeasured = append(findings.Unmeasured, entry.ImportPath)
			continue
		}
		actualCents := coverage.actualCents()
		if actualCents < entry.MinimumCents {
			findings.Violations = append(findings.Violations, Violation{
				ImportPath:    entry.ImportPath,
				ExpectedCents: entry.MinimumCents,
				ActualCents:   actualCents,
				DeltaCents:    actualCents - entry.MinimumCents,
			})
		}
	}

	sort.Strings(findings.Unregistered)
	sort.Strings(findings.Unmeasured)
	sort.Slice(findings.Violations, func(i, j int) bool {
		return findings.Violations[i].ImportPath < findings.Violations[j].ImportPath
	})
	if len(findings.Unregistered) != 0 || len(findings.Unmeasured) != 0 || len(findings.Violations) != 0 {
		return findings
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	previous := ""
	for _, entry := range manifest.Packages {
		if strings.TrimSpace(entry.ImportPath) == "" {
			return fmt.Errorf("%w: package import path is empty", ErrManifestInvalid)
		}
		if entry.HasMinimum == entry.HasException {
			if entry.HasMinimum {
				return &ManifestError{
					Kind:       ErrManifestBothFields,
					ImportPath: entry.ImportPath,
					Message:    fmt.Sprintf("coverage manifest package %q must define exactly one of minimum or exception; found both", entry.ImportPath),
				}
			}
			return &ManifestError{
				Kind:       ErrManifestNeitherField,
				ImportPath: entry.ImportPath,
				Message:    fmt.Sprintf("coverage manifest package %q must define exactly one of minimum or exception; found neither", entry.ImportPath),
			}
		}
		if previous != "" && entry.ImportPath <= previous {
			return &ManifestError{
				Kind:       ErrManifestUnsorted,
				ImportPath: entry.ImportPath,
				Message:    fmt.Sprintf("coverage manifest packages must be strictly sorted by import path: %q follows %q", entry.ImportPath, previous),
			}
		}
		if entry.HasMinimum && (entry.MinimumCents < 0 || entry.MinimumCents > 10000) {
			return fmt.Errorf("%w: package %q minimum is outside 0.00..100.00", ErrManifestInvalid, entry.ImportPath)
		}
		previous = entry.ImportPath
	}
	return nil
}

func (c Coverage) actualCents() int {
	if c.Total <= 0 {
		return 0
	}
	// Go's package coverage report records one decimal place. Preserve that
	// measurement before comparing it to the manifest's lexical two decimals.
	tenths := int(math.Floor((1000*float64(c.Covered))/float64(c.Total) + 0.5))
	return tenths * 10
}

func formatCents(cents int) string {
	if cents < 0 {
		return "-" + formatCents(-cents)
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func formatSignedCents(cents int) string {
	if cents > 0 {
		return "+" + formatCents(cents)
	}
	return formatCents(cents)
}
