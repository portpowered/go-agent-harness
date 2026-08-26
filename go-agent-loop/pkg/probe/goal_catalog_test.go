package probe_test

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

var updateGoalCatalogGolden = flag.Bool("update-goal-catalog-golden", false, "update the blind-probe goal catalog golden")

// Ordinary test runs compare against committed output. The explicit update
// flag is the only path that rewrites the golden fixture.
//
//go:embed testdata/goal_catalog.golden
var goalCatalogFixtures embed.FS

func TestLoadGoalCatalogIsTypedCompleteAndJSONRoundTrips(t *testing.T) {
	catalog, err := probe.LoadGoalCatalog()
	if err != nil {
		t.Fatalf("LoadGoalCatalog: %v", err)
	}
	if len(catalog) == 0 {
		t.Fatal("loaded catalog is empty")
	}

	wantCapabilities := map[probe.CapabilityArea]bool{
		probe.CapabilityTextInteraction:  false,
		probe.CapabilityAudioInteraction: false,
		probe.CapabilityToolUse:          false,
		probe.CapabilityMultimodalInput:  false,
	}
	for _, goal := range catalog {
		if goal.ID == "" || goal.Text == "" || goal.Capability == "" {
			t.Fatalf("goal is missing typed identity fields: %#v", goal)
		}
		if goal.Expectation.ArtifactClass == "" || goal.Expectation.Description == "" {
			t.Fatalf("goal %q has incomplete artifact expectation: %#v", goal.ID, goal.Expectation)
		}
		if _, ok := wantCapabilities[goal.Capability]; ok {
			wantCapabilities[goal.Capability] = true
		}
	}
	for capability, found := range wantCapabilities {
		if !found {
			t.Errorf("catalog has no goal for capability %q", capability)
		}
	}

	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	var decoded probe.GoalCatalog
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}
	if !reflect.DeepEqual(decoded, catalog) {
		t.Fatalf("catalog changed after JSON round trip:\n got  %#v\n want %#v", decoded, catalog)
	}
}

func TestLoadGoalCatalogIsDeterministicAcrossCalls(t *testing.T) {
	first, err := probe.LoadGoalCatalog()
	if err != nil {
		t.Fatalf("first LoadGoalCatalog: %v", err)
	}
	second, err := probe.LoadCatalog()
	if err != nil {
		t.Fatalf("second LoadCatalog: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("catalog order/content changed across loads:\n first %#v\nsecond %#v", first, second)
	}
}

func TestGoalCatalogRunInputsCoverValidatedGoalsExactlyOnceAndDeterministically(t *testing.T) {
	catalog, err := probe.LoadGoalCatalog()
	if err != nil {
		t.Fatalf("LoadGoalCatalog: %v", err)
	}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("shipped catalog failed validation: %v", err)
	}

	first := catalog.RunInputs()
	second := probe.EnumerateGoalRunInputs(catalog)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("run-input enumeration changed across calls:\n first %#v\nsecond %#v", first, second)
	}
	if len(first) != len(catalog) {
		t.Fatalf("run-input count = %d, want one per goal (%d)", len(first), len(catalog))
	}

	seen := make(map[string]bool, len(first))
	for index, input := range first {
		goal := catalog[index]
		if input.GoalID != goal.ID || input.GoalText != goal.Text {
			t.Errorf("run input %d = %#v, want goal %q with exact text", index, input, goal.ID)
		}
		if seen[input.GoalID] {
			t.Errorf("run input %d repeats goal ID %q", index, input.GoalID)
		}
		seen[input.GoalID] = true
	}
	for _, goal := range catalog {
		if !seen[goal.ID] {
			t.Errorf("goal %q is missing from run inputs", goal.ID)
		}
	}

	inputType := reflect.TypeOf(probe.GoalRunInput{})
	if inputType.NumField() != 2 {
		t.Fatalf("GoalRunInput has %d fields, want exactly ID and text", inputType.NumField())
	}
}

func TestGoalCatalogValidationRejectsDegradedCatalog(t *testing.T) {
	catalog, err := probe.LoadGoalCatalog()
	if err != nil {
		t.Fatalf("LoadGoalCatalog: %v", err)
	}
	if err := probe.ValidateGoalCatalog(catalog); err != nil {
		t.Fatalf("shipped catalog failed validation: %v", err)
	}

	// Deliberately drop the final shipped goal and duplicate the first one.
	degraded := append(probe.GoalCatalog(nil), catalog[:len(catalog)-1]...)
	degraded = append(degraded, catalog[0])
	err = probe.ValidateGoalCatalog(degraded)
	if err == nil {
		t.Fatal("degraded catalog unexpectedly passed validation")
	}
	if !errors.Is(err, probe.ErrInvalidGoalCatalog) || !errors.Is(err, probe.ErrDuplicateGoalID) {
		t.Fatalf("degraded catalog error identity: %v", err)
	}
	var validationErr *probe.GoalCatalogValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("degraded catalog error type: %T", err)
	}
	if validationErr.GoalID != catalog[0].ID || !strings.Contains(err.Error(), catalog[0].ID) {
		t.Fatalf("duplicate diagnostic does not name goal %q: %#v", catalog[0].ID, validationErr)
	}
}

func TestGoalCatalogValidationRejectsBlankTextAndMissingExpectation(t *testing.T) {
	catalog, err := probe.LoadGoalCatalog()
	if err != nil {
		t.Fatalf("LoadGoalCatalog: %v", err)
	}

	cases := []struct {
		name     string
		mutate   func(*probe.Goal)
		wantKind error
		wantText string
	}{
		{
			name: "blank text",
			mutate: func(goal *probe.Goal) {
				goal.Text = " \t\n"
			},
			wantKind: probe.ErrBlankGoalText,
			wantText: "text",
		},
		{
			name: "missing artifact class",
			mutate: func(goal *probe.Goal) {
				goal.Expectation.ArtifactClass = ""
			},
			wantKind: probe.ErrMissingGoalExpectation,
			wantText: "artifact_class",
		},
		{
			name: "missing expectation description",
			mutate: func(goal *probe.Goal) {
				goal.Expectation.Description = ""
			},
			wantKind: probe.ErrMissingGoalExpectation,
			wantText: "description",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := append(probe.GoalCatalog(nil), catalog...)
			test.mutate(&candidate[0])
			err := candidate.Validate()
			if err == nil {
				t.Fatal("invalid catalog unexpectedly passed validation")
			}
			if !errors.Is(err, probe.ErrInvalidGoalCatalog) || !errors.Is(err, test.wantKind) {
				t.Fatalf("error identity: %v", err)
			}
			var validationErr *probe.GoalCatalogValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error type: %T", err)
			}
			if validationErr.GoalID != candidate[0].ID || !strings.Contains(validationErr.Field, test.wantText) {
				t.Fatalf("diagnostic: %#v", validationErr)
			}
		})
	}
}

func TestGoalCatalogValidationRejectsEmptyCatalog(t *testing.T) {
	err := (probe.GoalCatalog{}).Validate()
	if err == nil {
		t.Fatal("empty catalog unexpectedly passed validation")
	}
	if !errors.Is(err, probe.ErrInvalidGoalCatalog) || !errors.Is(err, probe.ErrEmptyGoalCatalog) {
		t.Fatalf("empty catalog error identity: %v", err)
	}
	var validationErr *probe.GoalCatalogValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("empty catalog error type: %T", err)
	}
	if validationErr.Index != -1 || !strings.Contains(err.Error(), "goals") {
		t.Fatalf("empty catalog diagnostic: %#v (%v)", validationErr, err)
	}
}

func TestGoalCatalogGoalTextIsBlindProbeReadyAndMatchesGolden(t *testing.T) {
	catalog, err := probe.LoadGoalCatalog()
	if err != nil {
		t.Fatalf("LoadGoalCatalog: %v", err)
	}

	for _, goal := range catalog {
		if !strings.Contains(strings.ToLower(goal.Expectation.ArtifactClass), "record") {
			t.Errorf("goal %q expectation class %q does not name a recorded artifact", goal.ID, goal.Expectation.ArtifactClass)
		}
		if !strings.Contains(strings.ToLower(goal.Expectation.Description), "record") {
			t.Errorf("goal %q expectation description %q does not identify recorded evidence", goal.ID, goal.Expectation.Description)
		}
	}

	got := []byte(renderGoalList(catalog))
	path := filepath.FromSlash("testdata/goal_catalog.golden")
	if *updateGoalCatalogGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := goalCatalogFixtures.ReadFile(filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("goal catalog golden differs; run with -update-goal-catalog-golden only after reviewing the behavior change\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestGoalCatalogValidationRejectsBlindProbeHints(t *testing.T) {
	catalog, err := probe.LoadGoalCatalog()
	if err != nil {
		t.Fatalf("LoadGoalCatalog: %v", err)
	}

	for _, test := range []struct {
		name string
		text string
	}{
		{name: "internal package vocabulary", text: "Ask the assistant to use go-agent-loop for the answer."},
		{name: "flag spelling", text: "Ask the assistant to run --help and explain the result."},
		{name: "repository file path", text: "Ask the assistant to read README.md from the current folder."},
		{name: "program documentation", text: "Ask the assistant to follow the program documentation."},
		{name: "multiline hint", text: "Ask the assistant to answer this request.\nThen use the result."},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := append(probe.GoalCatalog(nil), catalog...)
			candidate[0].Text = test.text

			err := candidate.Validate()
			if err == nil {
				t.Fatal("hint-bearing goal unexpectedly passed validation")
			}
			if !errors.Is(err, probe.ErrInvalidGoalCatalog) || !errors.Is(err, probe.ErrGoalTextNotBlindProbeReady) {
				t.Fatalf("error identity: %v", err)
			}
			var validationErr *probe.GoalCatalogValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error type: %T", err)
			}
			if validationErr.GoalID != candidate[0].ID || validationErr.Field != "text" || !strings.Contains(err.Error(), candidate[0].ID) {
				t.Fatalf("diagnostic: %#v (%v)", validationErr, err)
			}
		})
	}
}

func renderGoalList(catalog probe.GoalCatalog) string {
	var rendered strings.Builder
	for _, goal := range catalog {
		rendered.WriteString(goal.ID)
		rendered.WriteString(": ")
		rendered.WriteString(goal.Text)
		rendered.WriteByte('\n')
	}
	return rendered.String()
}
