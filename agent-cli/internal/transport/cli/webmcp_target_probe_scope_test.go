package cli

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func TestExactSelectionDoesNotProbeUnrelatedRestoredTab(t *testing.T) {
	var browser config.BrowserConfig
	browser.Selection.Tab = "target-selected"
	probe := productionTargetProbe{owner: &productionWebMCPComposition{browser: browser}}
	// No runtime is installed: calling the renderer would panic. Listing an
	// unrelated tab must leave its capability unknown without touching it.
	capability, err := probe.Probe(context.Background(), discovery.BrowserCandidate{},
		discovery.Target{ID: "target-suspended"})
	if err != nil || capability.DomainKnown || capability.PageToolsKnown || capability.ToolCount != -1 {
		t.Fatalf("unrelated capability should remain unknown: %+v, %v", capability, err)
	}
}
