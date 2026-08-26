package probe_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

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
