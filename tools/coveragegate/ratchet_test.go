package main

import (
	"errors"
	"testing"
)

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
			want:    "coverage gate found stale coverage floors (raise each minimum to at least the suggested value):\n- example/a: minimum 87.90%, actual 90.00%, headroom 2.10% exceeds allowed 2.00%; raise minimum to 88.00",
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

// In a five-statement package one statement is 20 points, so a floor that
// tolerates losing one covered statement must sit about 20 points below the
// measurement without being reported as stale.
func TestCompareWidensRatchetHeadroomToOneStatementForSmallPackages(t *testing.T) {
	measured := map[string]Coverage{"example/small": {Covered: 4, Total: 5}}
	tolerant := mustParseManifest(t, manifestJSON(`{"package":"example/small","minimum":60.00}`))
	if err := Compare(tolerant, measured); err != nil {
		t.Fatalf("Compare() = %v, want a one-statement floor to pass", err)
	}
	if err := Compare(tolerant, map[string]Coverage{"example/small": {Covered: 3, Total: 5}}); err != nil {
		t.Fatalf("Compare() after losing one statement = %v, want the floor to tolerate it", err)
	}
	stale := mustParseManifest(t, manifestJSON(`{"package":"example/small","minimum":59.80}`))
	assertManifestError(t, Compare(stale, measured), ErrCoverageFloorStale,
		"coverage gate found stale coverage floors (raise each minimum to at least the suggested value):\n- example/small: minimum 59.80%, actual 80.00%, headroom 20.20% exceeds allowed 20.10%; raise minimum to 59.90")
}

func TestAllowedHeadroomCents(t *testing.T) {
	for _, tt := range []struct {
		total int64
		want  int
	}{
		{total: 0, want: 200},
		{total: 1, want: 10010},
		{total: 3, want: 3350},
		{total: 50, want: 210},
		{total: 51, want: 210},
		{total: 100, want: 200},
		{total: 5000, want: 200},
	} {
		if got := allowedHeadroomCents(tt.total); got != tt.want {
			t.Errorf("allowedHeadroomCents(%d) = %d, want %d", tt.total, got, tt.want)
		}
	}
}

func TestCompareSelectedReportsStaleSelectedFloorOnly(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/a","minimum":50.00},{"package":"example/b","minimum":50.00}]}`)
	measured := map[string]Coverage{"example/a": {Covered: 90, Total: 100}, "example/b": {Covered: 90, Total: 100}}
	err := CompareSelected(manifest, []string{"example/b"}, measured)
	assertManifestError(t, err, ErrCoverageFloorStale,
		"coverage gate found stale coverage floors (raise each minimum to at least the suggested value):\n- example/b: minimum 50.00%, actual 90.00%, headroom 40.00% exceeds allowed 2.00%; raise minimum to 88.00")
}

func TestCompareRejectsExceptionsForPackagesWithCoveredStatements(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/entry","exception":"process entrypoint"},{"package":"example/tested","exception":"declarations only"}]}`)
	measured := map[string]Coverage{"example/entry": {Covered: 0, Total: 12}, "example/tested": {Covered: 3, Total: 4}}
	err := Compare(manifest, measured)
	assertManifestError(t, err, ErrExceptionMeasured,
		"coverage gate found exceptions for packages with covered statements (register a minimum instead):\n- example/tested: actual 75.00% (3 of 4 statements)")
	if err := CompareSelected(manifest, []string{"example/tested"}, measured); !errors.Is(err, ErrExceptionMeasured) {
		t.Fatalf("CompareSelected() = %v, want ErrExceptionMeasured", err)
	}
	if err := CompareSelected(manifest, []string{"example/entry"}, measured); err != nil {
		t.Fatalf("CompareSelected() = %v, want an unexercised entrypoint exception to pass", err)
	}
}

func TestManifestRejectsZeroMinimumsAndUnexplainedExceptions(t *testing.T) {
	_, err := ParseManifestFragment([]byte(`{"package":"example/a","minimum":0.00}`))
	assertManifestError(t, err, ErrManifestZeroMinimum,
		`coverage manifest package "example/a" minimum 0.00 enforces nothing; register a positive minimum, or an exception stating why the package has nothing to measure`)
	_, err = ParseManifest([]byte(manifestJSON(`{"package":"example/a","exception":"  "}`)))
	assertManifestError(t, err, ErrManifestException, `coverage manifest package "example/a" exception must state its reason`)

	constructed := Manifest{Packages: []PackageEntry{{ImportPath: "example/a", HasMinimum: true}}}
	if err := Compare(constructed, nil); !errors.Is(err, ErrManifestZeroMinimum) {
		t.Fatalf("Compare() of a constructed zero minimum = %v, want ErrManifestZeroMinimum", err)
	}
}
