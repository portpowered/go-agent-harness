package testing

const (
	// SharedSessionFixtureOwner names the authoritative owner for committed shared
	// .session.json replay fixtures in this repository.
	SharedSessionFixtureOwner = "go-llm-gateway/pkg/testing"

	// SharedSessionFixtureRoot is the repository-relative canonical root for
	// committed shared .session.json replay fixtures.
	SharedSessionFixtureRoot = "go-llm-gateway/pkg/testing/testdata/session-fixtures"

	// Phase2SessionFixtureOwnershipStep records the Phase 2 enabling step
	// satisfied by the ownership boundary work.
	Phase2SessionFixtureOwnershipStep = "Phase 2 enabling step: session fixture ownership and boundary cleanup before broader API hardening"
)
