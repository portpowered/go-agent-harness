package probe_test

import (
	"encoding/json"
	"reflect"
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
