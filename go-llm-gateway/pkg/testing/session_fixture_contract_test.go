package testing

import (
	"strings"
	"testing"
)

func TestSharedSessionFixtureContract(t *testing.T) {
	if SharedSessionFixtureOwner != "go-llm-gateway/pkg/testing" {
		t.Fatalf("SharedSessionFixtureOwner = %q", SharedSessionFixtureOwner)
	}
	if SharedSessionFixtureRoot != "go-llm-gateway/pkg/testing/testdata/session-fixtures" {
		t.Fatalf("SharedSessionFixtureRoot = %q", SharedSessionFixtureRoot)
	}
	if !strings.HasPrefix(SharedSessionFixtureRoot, SharedSessionFixtureOwner+"/") {
		t.Fatalf("SharedSessionFixtureRoot %q must remain under owner %q", SharedSessionFixtureRoot, SharedSessionFixtureOwner)
	}
	if !strings.Contains(Phase2SessionFixtureOwnershipStep, "Phase 2 enabling step") {
		t.Fatalf("Phase2SessionFixtureOwnershipStep = %q", Phase2SessionFixtureOwnershipStep)
	}
}
