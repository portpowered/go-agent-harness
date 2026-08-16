package functional

import (
	"testing"

	loopfunctional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
)

func TestFunctionalQuarantineManifest(t *testing.T) {
	manifest, err := ReadFunctionalQuarantine()
	if err != nil {
		t.Fatalf("read agent-cli functional quarantine: %v", err)
	}
	if _, err := loopfunctional.Select(manifest, loopfunctional.Inventory{}); err != nil {
		t.Fatalf("validate agent-cli functional quarantine against its current inventory: %v", err)
	}
}
