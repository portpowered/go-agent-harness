package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrManifestInvalid          = errors.New("invalid coverage manifest")
	ErrManifestMinimumPrecision = errors.New("coverage minimum must use exactly two decimal places")
	ErrManifestUnsorted         = errors.New("coverage manifest packages are not strictly sorted")
	ErrManifestBothFields       = errors.New("coverage manifest entry defines both minimum and exception")
	ErrManifestNeitherField     = errors.New("coverage manifest entry defines neither minimum nor exception")
	ErrManifestException        = errors.New("coverage manifest exception must be a string")
	ErrManifestDuplicate        = errors.New("coverage manifest contains duplicate package registrations")
	ErrProfileInvalid           = errors.New("invalid Go coverage profile")
	ErrUnregisteredPackage      = errors.New("coverage profile contains an unregistered package")
	ErrMissingCoverage          = errors.New("manifest package has no measured coverage")
	ErrCoverageFloorViolation   = errors.New("measured coverage is below its minimum")
	ErrCoverageFloorStale       = errors.New("coverage minimum is too far below measured coverage")
	ErrManifestZeroMinimum      = errors.New("coverage minimum 0.00 enforces nothing")
	ErrExceptionMeasured        = errors.New("coverage exception registered for a package with covered statements")
)

// Manifest is the validated coverage registration set loaded from the legacy
// document or from one-package registration fragments.
type Manifest struct {
	Packages []PackageEntry
}

// PackageEntry is one manifest registration. MinimumCents is a percentage in
// hundredths (40.20% is 4020). Exceptions are registered but do not impose a
// floor or require a measured profile entry.
type PackageEntry struct {
	ImportPath   string
	MinimumCents int
	HasMinimum   bool
	Exception    string
	HasException bool
}

// Coverage is the statement aggregate for one import path.
type Coverage struct {
	Covered int64
	Total   int64
}

// Violation is a deterministic, actionable floor failure.
type Violation struct {
	ImportPath    string
	ExpectedCents int
	ActualCents   int
	DeltaCents    int
}

// StaleFloor is a floor that has fallen more than its allowed headroom below
// the measured coverage. Floors only ratchet up: the gate fails until the
// manifest minimum is raised to at least SuggestedCents.
type StaleFloor struct {
	ImportPath     string
	MinimumCents   int
	ActualCents    int
	AllowedCents   int
	SuggestedCents int
}

// FindingsError aggregates every missing package and floor regression found
// in one comparison. It unwraps to typed sentinels so callers can branch on
// the failure category without parsing the rendered report.
type FindingsError struct {
	Unregistered []string
	Unmeasured   []string
	Violations   []Violation
	Stale        []StaleFloor
	// MeasuredExceptions are exception registrations whose package has
	// covered statements: it has something to measure, so it needs a floor.
	MeasuredExceptions []MeasuredException
}

// MeasuredException is an exception registration contradicted by the
// measurement.
type MeasuredException struct {
	ImportPath string
	Coverage   Coverage
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
	if len(e.Stale) > 0 {
		var b strings.Builder
		b.WriteString("coverage gate found stale coverage floors (raise each minimum to at least the suggested value):")
		for _, stale := range e.Stale {
			fmt.Fprintf(&b, "\n- %s: minimum %s%%, actual %s%%, headroom %s%% exceeds allowed %s%%; raise minimum to %s",
				stale.ImportPath,
				formatCents(stale.MinimumCents),
				formatCents(stale.ActualCents),
				formatCents(stale.ActualCents-stale.MinimumCents),
				formatCents(stale.AllowedCents),
				formatCents(stale.SuggestedCents),
			)
		}
		sections = append(sections, b.String())
	}
	if len(e.MeasuredExceptions) > 0 {
		var b strings.Builder
		b.WriteString("coverage gate found exceptions for packages with covered statements (register a minimum instead):")
		for _, measured := range e.MeasuredExceptions {
			fmt.Fprintf(&b, "\n- %s: actual %s%% (%d of %d statements)",
				measured.ImportPath,
				formatCents(measured.Coverage.actualCents()),
				measured.Coverage.Covered,
				measured.Coverage.Total,
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
	if len(e.Stale) > 0 {
		causes = append(causes, ErrCoverageFloorStale)
	}
	if len(e.MeasuredExceptions) > 0 {
		causes = append(causes, ErrExceptionMeasured)
	}
	return causes
}

type ManifestError struct {
	Kind       error
	ImportPath string
	Message    string
}

func (e *ManifestError) Error() string { return e.Message }

func (e *ManifestError) Unwrap() error { return e.Kind }

// DuplicateRegistration identifies two fragments that claim the same
// package. Fragment paths are retained so maintainers can resolve the
// conflict without inspecting the merged catalog.
type DuplicateRegistration struct {
	ImportPath     string
	FirstFragment  string
	SecondFragment string
}

// DuplicateManifestError reports all duplicate package registrations found
// while loading a fragment directory.
type DuplicateManifestError struct {
	Duplicates []DuplicateRegistration
}

func (e *DuplicateManifestError) Error() string {
	var b strings.Builder
	b.WriteString("coverage manifest found duplicate package registrations:")
	for _, duplicate := range e.Duplicates {
		fmt.Fprintf(&b, "\n- %s: fragments %q and %q",
			duplicate.ImportPath,
			duplicate.FirstFragment,
			duplicate.SecondFragment,
		)
	}
	return b.String()
}

func (e *DuplicateManifestError) Unwrap() error { return ErrManifestDuplicate }

var minimumPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{2}$`)

// coverageComparisonBandCents accounts for the one-decimal precision of Go
// coverage measurements without changing the configured two-decimal floor.
const coverageComparisonBandCents = 10

// ratchetHeadroomCents is the most a floor may sit below measured coverage
// (2.00 points). A larger gap means coverage rose without the floor
// following it, so a later regression of that size would pass unnoticed.
const ratchetHeadroomCents = 200

// allowedHeadroomCents is the ratchet headroom for a package with total
// statements. In a small package one statement is worth more than 2.00
// points, so the headroom widens to one statement (rounded up to Go's 0.1
// precision) plus the comparison band: a floor that tolerates losing a
// single covered statement is never reported as stale.
func allowedHeadroomCents(total int64) int {
	if total <= 0 {
		return ratchetHeadroomCents
	}
	oneStatementTenths := (permille + total - 1) / total
	return max(ratchetHeadroomCents, int(oneStatementTenths)*10+coverageComparisonBandCents)
}

// LoadManifest loads either the fragment directory used by the repository
// gate or the legacy single JSON manifest. Keeping the file form available
// makes the command transition safe for callers that still use a file.
func LoadManifest(path string) (Manifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: stat coverage manifest %q: %w", ErrManifestInvalid, path, err)
	}
	if info.IsDir() {
		return LoadManifestDir(path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: read coverage manifest %q: %w", ErrManifestInvalid, path, err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("coverage manifest %q: %w", path, err)
	}
	return manifest, nil
}

// LoadManifestDir discovers and merges one-package registration fragments
// below path. The directory is a dedicated catalog: every non-directory
// entry is treated as a fragment, including entries in nested directories.
// Discovery and merged registrations are sorted explicitly so neither
// filesystem traversal order nor fragment filenames affect the result.
func LoadManifestDir(path string) (Manifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: stat coverage manifest directory %q: %w", ErrManifestInvalid, path, err)
	}
	if !info.IsDir() {
		return Manifest{}, fmt.Errorf("%w: coverage manifest path %q is not a directory", ErrManifestInvalid, path)
	}

	var fragmentPaths []string
	walkErr := filepath.WalkDir(path, func(fragmentPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%w: inspect coverage manifest fragment %q: %w", ErrManifestInvalid, fragmentPath, err)
		}
		if entry.IsDir() {
			return nil
		}
		fragmentPaths = append(fragmentPaths, fragmentPath)
		return nil
	})
	if walkErr != nil {
		return Manifest{}, walkErr
	}
	sort.Strings(fragmentPaths)

	manifest := Manifest{Packages: make([]PackageEntry, 0, len(fragmentPaths))}
	locations := make(map[string]string, len(fragmentPaths))
	var duplicates []DuplicateRegistration
	for _, fragmentPath := range fragmentPaths {
		data, err := os.ReadFile(fragmentPath)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: read coverage manifest fragment %q: %w", ErrManifestInvalid, fragmentPath, err)
		}
		entry, err := ParseManifestFragment(data)
		if err != nil {
			return Manifest{}, fmt.Errorf("coverage manifest fragment %q: %w", fragmentPath, err)
		}
		if firstFragment, ok := locations[entry.ImportPath]; ok {
			duplicates = append(duplicates, DuplicateRegistration{
				ImportPath:     entry.ImportPath,
				FirstFragment:  firstFragment,
				SecondFragment: fragmentPath,
			})
			continue
		}
		locations[entry.ImportPath] = fragmentPath
		manifest.Packages = append(manifest.Packages, entry)
	}
	if len(duplicates) != 0 {
		sort.Slice(duplicates, func(i, j int) bool {
			if duplicates[i].ImportPath != duplicates[j].ImportPath {
				return duplicates[i].ImportPath < duplicates[j].ImportPath
			}
			return duplicates[i].SecondFragment < duplicates[j].SecondFragment
		})
		return Manifest{}, &DuplicateManifestError{Duplicates: duplicates}
	}

	sort.Slice(manifest.Packages, func(i, j int) bool {
		return manifest.Packages[i].ImportPath < manifest.Packages[j].ImportPath
	})
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ParseManifestFragment parses one JSON registration object. Unlike the
// legacy manifest parser, a fragment has no packages array: exactly one
// package registration is required in the object itself.
func ParseManifestFragment(data []byte) (PackageEntry, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var rawEntry json.RawMessage
	if err := decoder.Decode(&rawEntry); err != nil {
		return PackageEntry{}, fmt.Errorf("%w: %w", ErrManifestInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return PackageEntry{}, fmt.Errorf("%w: manifest fragment contains more than one JSON value", ErrManifestInvalid)
		}
		return PackageEntry{}, fmt.Errorf("%w: %w", ErrManifestInvalid, err)
	}
	return parseEntry(rawEntry)
}

// ParseManifest validates the JSON and preserves the lexical minimum format
// before converting it to an integer percentage representation.
func ParseManifest(data []byte) (Manifest, error) {
	var document struct {
		Packages []json.RawMessage `json:"packages"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrManifestInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("%w: manifest contains more than one JSON value", ErrManifestInvalid)
		}
		return Manifest{}, fmt.Errorf("%w: %w", ErrManifestInvalid, err)
	}
	if document.Packages == nil {
		return Manifest{}, fmt.Errorf("%w: packages must be an array", ErrManifestInvalid)
	}

	manifest := Manifest{Packages: make([]PackageEntry, 0, len(document.Packages))}
	seen := make(map[string]int, len(document.Packages))
	for index, rawEntry := range document.Packages {
		entry, err := parseEntry(rawEntry)
		if err != nil {
			return Manifest{}, err
		}
		if previousIndex, duplicate := seen[entry.ImportPath]; duplicate {
			return Manifest{}, &ManifestError{
				Kind:       ErrManifestDuplicate,
				ImportPath: entry.ImportPath,
				Message: fmt.Sprintf(
					"coverage manifest contains duplicate package %q at entries %d and %d",
					entry.ImportPath, previousIndex, index,
				),
			}
		}
		seen[entry.ImportPath] = index
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
		return PackageEntry{}, fmt.Errorf("%w: package entry must be an object: %w", ErrManifestInvalid, err)
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
		return parseException(importPath, exceptionRaw)
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
	if minimumCents > maxPercentCents {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestMinimumPrecision,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q minimum must be between 0.00 and 100.00: got %s", importPath, lexeme),
		}
	}
	if minimumCents == 0 {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestZeroMinimum,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q minimum 0.00 enforces nothing; register a positive minimum, or an exception stating why the package has nothing to measure", importPath),
		}
	}
	return PackageEntry{ImportPath: importPath, MinimumCents: minimumCents, HasMinimum: true}, nil
}

// parseException reads an exception registration. Its reason is required:
// an exception exempts the package from every floor, so the manifest must
// say why there is nothing to measure.
func parseException(importPath string, raw json.RawMessage) (PackageEntry, error) {
	var exception string
	if err := json.Unmarshal(raw, &exception); err != nil {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestException,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q exception must be a string", importPath),
		}
	}
	if strings.TrimSpace(exception) == "" {
		return PackageEntry{}, &ManifestError{
			Kind:       ErrManifestException,
			ImportPath: importPath,
			Message:    fmt.Sprintf("coverage manifest package %q exception must state its reason", importPath),
		}
	}
	return PackageEntry{ImportPath: importPath, Exception: exception, HasException: true}, nil
}

// ReadProfiles parses explicit Go coverage profile paths and aggregates
// statements by import path. Every profile must use the same coverage mode.
func ReadProfiles(paths []string) (map[string]Coverage, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: no profile paths provided", ErrProfileInvalid)
	}
	blocks := make(map[string]profileBlock)
	var mode string
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("%w: open %q: %w", ErrProfileInvalid, path, err)
		}
		profileMode, parseErr := parseProfile(file, path, blocks)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, parseErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("%w: close %q: %w", ErrProfileInvalid, path, closeErr)
		}
		if mode == "" {
			mode = profileMode
		} else if mode != profileMode {
			return nil, fmt.Errorf("%w: coverage profiles use different modes %q and %q", ErrProfileInvalid, mode, profileMode)
		}
	}
	measurements := make(map[string]Coverage)
	for _, block := range blocks {
		current := measurements[block.packagePath]
		current.Total += block.statements
		if block.covered {
			current.Covered += block.statements
		}
		measurements[block.packagePath] = current
	}
	return measurements, nil
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
			findings.checkException(entry, measurements[entry.ImportPath])
			continue
		}
		coverage, ok := measurements[entry.ImportPath]
		if !ok || coverage.Total == 0 {
			findings.Unmeasured = append(findings.Unmeasured, entry.ImportPath)
			continue
		}
		findings.checkFloor(entry, coverage)
	}

	return finishFindings(findings)
}

// CompareSelected applies the same rounded coverage-floor calculation as
// Compare, but only to the selected package paths. Measurements for
// unchanged packages are intentionally ignored so a changed-package run can
// enforce local floors without accidentally becoming a repository-wide gate.
func CompareSelected(manifest Manifest, selected []string, measurements map[string]Coverage) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	registered := make(map[string]PackageEntry, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		registered[entry.ImportPath] = entry
	}

	findings := &FindingsError{}
	seen := make(map[string]struct{}, len(selected))
	for _, packagePath := range selected {
		if _, duplicate := seen[packagePath]; duplicate {
			continue
		}
		seen[packagePath] = struct{}{}
		entry, ok := registered[packagePath]
		if !ok {
			findings.Unregistered = append(findings.Unregistered, packagePath)
			continue
		}
		if entry.HasException {
			findings.checkException(entry, measurements[packagePath])
			continue
		}
		coverage, measured := measurements[packagePath]
		if !measured || coverage.Total == 0 {
			findings.Unmeasured = append(findings.Unmeasured, packagePath)
			continue
		}
		findings.checkFloor(entry, coverage)
	}
	return finishFindings(findings)
}

// checkException records an exception whose package has covered statements.
// A process entrypoint measured at 0% keeps its exception; anything a test
// covers can hold a floor.
func (e *FindingsError) checkException(entry PackageEntry, coverage Coverage) {
	if coverage.Covered > 0 {
		e.MeasuredExceptions = append(e.MeasuredExceptions, MeasuredException{ImportPath: entry.ImportPath, Coverage: coverage})
	}
}

// checkFloor records a floor regression, or a stale floor when coverage has
// risen more than the allowed headroom above the registered minimum.
func (e *FindingsError) checkFloor(entry PackageEntry, coverage Coverage) {
	actualCents := coverage.actualCents()
	if actualCents+coverageComparisonBandCents < entry.MinimumCents {
		e.Violations = append(e.Violations, Violation{
			ImportPath:    entry.ImportPath,
			ExpectedCents: entry.MinimumCents,
			ActualCents:   actualCents,
			DeltaCents:    actualCents - entry.MinimumCents,
		})
		return
	}
	allowed := allowedHeadroomCents(coverage.Total)
	if actualCents-entry.MinimumCents > allowed {
		e.Stale = append(e.Stale, StaleFloor{
			ImportPath:     entry.ImportPath,
			MinimumCents:   entry.MinimumCents,
			ActualCents:    actualCents,
			AllowedCents:   allowed,
			SuggestedCents: actualCents - allowed,
		})
	}
}

func finishFindings(findings *FindingsError) error {
	sort.Strings(findings.Unregistered)
	sort.Strings(findings.Unmeasured)
	sort.Slice(findings.Violations, func(i, j int) bool {
		return findings.Violations[i].ImportPath < findings.Violations[j].ImportPath
	})
	sort.Slice(findings.Stale, func(i, j int) bool {
		return findings.Stale[i].ImportPath < findings.Stale[j].ImportPath
	})
	sort.Slice(findings.MeasuredExceptions, func(i, j int) bool {
		return findings.MeasuredExceptions[i].ImportPath < findings.MeasuredExceptions[j].ImportPath
	})
	if len(findings.Unregistered) != 0 || len(findings.Unmeasured) != 0 || len(findings.Violations) != 0 ||
		len(findings.Stale) != 0 || len(findings.MeasuredExceptions) != 0 {
		return findings
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	previous := ""
	seen := make(map[string]int, len(manifest.Packages))
	for index, entry := range manifest.Packages {
		if strings.TrimSpace(entry.ImportPath) == "" {
			return fmt.Errorf("%w: package import path is empty", ErrManifestInvalid)
		}
		if previousIndex, duplicate := seen[entry.ImportPath]; duplicate {
			return &ManifestError{
				Kind:       ErrManifestDuplicate,
				ImportPath: entry.ImportPath,
				Message: fmt.Sprintf(
					"coverage manifest contains duplicate package %q at entries %d and %d",
					entry.ImportPath, previousIndex, index,
				),
			}
		}
		seen[entry.ImportPath] = index
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
		if entry.HasMinimum && entry.MinimumCents == 0 {
			return fmt.Errorf("%w: package %q", ErrManifestZeroMinimum, entry.ImportPath)
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
	tenths := int(math.Round(permille * float64(c.Covered) / float64(c.Total)))
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
