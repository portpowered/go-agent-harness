package main

import (
	"errors"
	"testing"
)

const staleHeader = "coverage gate found stale coverage floors (raise each minimum to the suggested value):"

func TestCompareRatchetsFloorsUpToTwoPointsBelowMeasuredCoverage(t *testing.T) {
	tests := []struct {
		name    string
		minimum string
		want    string
	}{
		{name: "exactly two points below", minimum: "88.00"},
		{name: "within the comparison band above", minimum: "90.10"},
		{
			name:    "more than two points below",
			minimum: "87.90",
			want:    staleHeader + "\n- example/a: minimum 87.90%, actual 90.00%, headroom 2.10% exceeds allowed 2.00%; raise minimum to 89.00",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := mustParseManifest(t, manifestJSON(`{"package":"example/a","minimum":`+tt.minimum+`}`))
			err := Compare(manifest, map[string]Coverage{"example/a": {Covered: 900, Total: 1000}})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Compare() = %v, want pass", err)
				}
				return
			}
			assertManifestError(t, err, ErrCoverageFloorStale, tt.want)
			if errors.Is(err, ErrCoverageFloorViolation) {
				t.Fatal("a stale floor is reported as a regression")
			}
		})
	}
}

// The suggested minimum sits half the headroom below the measurement, so a
// floor raised to it survives the next run moving a full point either way:
// it neither fails on the dip nor goes stale on the rise.
func TestSuggestedMinimumToleratesJitterInBothDirections(t *testing.T) {
	measured := Coverage{Covered: 900, Total: 1000}
	suggested := suggestedMinimumCents(measured)
	if suggested != 8900 {
		t.Fatalf("suggested minimum = %d cents, want 8900 (actual - 1.00)", suggested)
	}
	manifest := mustParseManifest(t, manifestJSON(`{"package":"example/a","minimum":`+formatCents(suggested)+`}`))
	for _, covered := range []int64{890, 900, 910} {
		if err := Compare(manifest, map[string]Coverage{"example/a": {Covered: covered, Total: 1000}}); err != nil {
			t.Fatalf("Compare() at %d/1000 = %v, want the suggested floor to pass", covered, err)
		}
	}
	if err := Compare(manifest, map[string]Coverage{"example/a": {Covered: 911, Total: 1000}}); !errors.Is(err, ErrCoverageFloorStale) {
		t.Fatalf("Compare() 2.10 points above the floor = %v, want stale", err)
	}
	if err := Compare(manifest, map[string]Coverage{"example/a": {Covered: 888, Total: 1000}}); !errors.Is(err, ErrCoverageFloorViolation) {
		t.Fatalf("Compare() 0.20 points below the floor = %v, want a regression", err)
	}
}

// In a five-statement package one statement is 20 points. The suggested
// floor tolerates losing one covered statement, and stays fresh when one
// more statement is covered.
func TestSmallPackageFloorToleratesOneStatementEitherWay(t *testing.T) {
	measured := Coverage{Covered: 4, Total: 5}
	if got := suggestedMinimumCents(measured); got != 5990 {
		t.Fatalf("suggested minimum = %d cents, want 5990", got)
	}
	manifest := mustParseManifest(t, manifestJSON(`{"package":"example/small","minimum":59.90}`))
	for _, covered := range []int64{3, 4, 5} {
		if err := Compare(manifest, map[string]Coverage{"example/small": {Covered: covered, Total: 5}}); err != nil {
			t.Fatalf("Compare() at %d/5 = %v, want pass", covered, err)
		}
	}
	stale := mustParseManifest(t, manifestJSON(`{"package":"example/small","minimum":39.70}`))
	assertManifestError(t, Compare(stale, map[string]Coverage{"example/small": measured}), ErrCoverageFloorStale,
		staleHeader+"\n- example/small: minimum 39.70%, actual 80.00%, headroom 40.30% exceeds allowed 40.20%; raise minimum to 59.90")
}

func TestAllowedHeadroomAndSuggestedMinimum(t *testing.T) {
	for _, tt := range []struct {
		coverage      Coverage
		wantAllowed   int
		wantSuggested int
	}{
		{coverage: Coverage{}, wantAllowed: 200, wantSuggested: 20},
		{coverage: Coverage{Covered: 1, Total: 1}, wantAllowed: 20020, wantSuggested: 20},
		{coverage: Coverage{Covered: 1, Total: 3}, wantAllowed: 6700, wantSuggested: 20},
		{coverage: Coverage{Covered: 0, Total: 12}, wantAllowed: 1700, wantSuggested: 20},
		{coverage: Coverage{Covered: 45, Total: 50}, wantAllowed: 420, wantSuggested: 8790},
		{coverage: Coverage{Covered: 90, Total: 100}, wantAllowed: 220, wantSuggested: 8890},
		{coverage: Coverage{Covered: 180, Total: 200}, wantAllowed: 200, wantSuggested: 8900},
		{coverage: Coverage{Covered: 4500, Total: 5000}, wantAllowed: 200, wantSuggested: 8900},
	} {
		if got := allowedHeadroomCents(tt.coverage.Total); got != tt.wantAllowed {
			t.Errorf("allowedHeadroomCents(%d) = %d, want %d", tt.coverage.Total, got, tt.wantAllowed)
		}
		if got := suggestedMinimumCents(tt.coverage); got != tt.wantSuggested {
			t.Errorf("suggestedMinimumCents(%+v) = %d, want %d", tt.coverage, got, tt.wantSuggested)
		}
	}
}

func TestCompareSelectedReportsStaleSelectedFloorOnly(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/a","minimum":50.00},{"package":"example/b","minimum":50.00}]}`)
	measured := map[string]Coverage{"example/a": {Covered: 90, Total: 100}, "example/b": {Covered: 90, Total: 100}}
	err := CompareSelected(manifest, []string{"example/b"}, measured)
	assertManifestError(t, err, ErrCoverageFloorStale,
		staleHeader+"\n- example/b: minimum 50.00%, actual 90.00%, headroom 40.00% exceeds allowed 2.20%; raise minimum to 88.90")
}

func TestCompareRejectsExceptionsForPackagesWithCoveredStatements(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/entry","exception":"process entrypoint"},{"package":"example/tested","exception":"declarations only"}]}`)
	measured := map[string]Coverage{"example/entry": {Covered: 0, Total: 12}, "example/tested": {Covered: 3, Total: 4}}
	err := Compare(manifest, measured)
	assertManifestError(t, err, ErrExceptionMeasured,
		"coverage gate found exceptions for packages with covered statements (register a minimum instead):\n- example/tested: actual 75.00% (3 of 4 statements); register minimum 49.90")
	if err := CompareSelected(manifest, []string{"example/tested"}, measured); !errors.Is(err, ErrExceptionMeasured) {
		t.Fatalf("CompareSelected() = %v, want ErrExceptionMeasured", err)
	}
	if err := CompareSelected(manifest, []string{"example/entry"}, measured); err != nil {
		t.Fatalf("CompareSelected() = %v, want an unexercised entrypoint exception to pass", err)
	}
}

// A minimum at or below the 0.10 comparison band passes with nothing
// covered, so it enforces as little as 0.00 does.
func TestManifestRejectsNoopMinimumsAndUnexplainedExceptions(t *testing.T) {
	for _, minimum := range []string{"0.00", "0.10"} {
		_, err := ParseManifestFragment([]byte(`{"package":"example/a","minimum":` + minimum + `}`))
		assertManifestError(t, err, ErrManifestNoopMinimum,
			`coverage manifest package "example/a" minimum `+minimum+` passes with no statement covered; register a minimum of at least 0.20, or an exception stating why the package has nothing to measure`)
	}
	entry, err := ParseManifestFragment([]byte(`{"package":"example/a","minimum":0.11}`))
	if err != nil || entry.MinimumCents != 11 {
		t.Fatalf("ParseManifestFragment(0.11) = %+v, %v; want accepted", entry, err)
	}
	if err := Compare(Manifest{Packages: []PackageEntry{entry}}, map[string]Coverage{"example/a": {Total: 1}}); !errors.Is(err, ErrCoverageFloorViolation) {
		t.Fatalf("Compare() of 0.11 with nothing covered = %v, want a regression", err)
	}

	_, err = ParseManifest([]byte(manifestJSON(`{"package":"example/a","exception":"  "}`)))
	assertManifestError(t, err, ErrManifestException, `coverage manifest package "example/a" exception must state its reason`)

	constructed := Manifest{Packages: []PackageEntry{{ImportPath: "example/a", HasMinimum: true, MinimumCents: 10}}}
	if err := Compare(constructed, nil); !errors.Is(err, ErrManifestNoopMinimum) {
		t.Fatalf("Compare() of a constructed 0.10 minimum = %v, want ErrManifestNoopMinimum", err)
	}
}
