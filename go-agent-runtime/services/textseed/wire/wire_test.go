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

func TestDefaultConstructorsKeepLiveAllocationsDistinct(t *testing.T) {
	services := make([]textseed.Service, 32)
	seen := make(map[string]struct{}, len(services))
	for i := range services {
		services[i] = NewDefaultService()
		value := services[i].Allocate()
		if value == "" {
			t.Fatal("default allocation is empty")
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("live Wire services allocated the same value %q", value)
		}
		seen[value] = struct{}{}
	}
}
