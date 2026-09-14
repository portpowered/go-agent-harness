package textseed_test

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
)

func TestAllocatorFuncCallsTheInjectedFunction(t *testing.T) {
	allocator := textseed.AllocatorFunc(func() string { return "deterministic" })
	if got := allocator.Allocate(); got != "deterministic" {
		t.Fatalf("allocation = %q, want deterministic", got)
	}
}
