package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
)

func TestConstructorsBuildServiceGraphs(t *testing.T) {
	injected := NewService(textseed.AllocatorFunc(func() string { return "wire-test" }))
	if got := injected.Allocate(); got != "wire-test" {
		t.Fatalf("injected allocation = %q", got)
	}
	if got := NewDefaultService().Allocate(); got == "" {
		t.Fatal("default allocation is empty")
	}
}
