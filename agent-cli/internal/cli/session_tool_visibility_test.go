package cli

import (
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
)

func TestApplySessionToolVisibility_DefaultHidesComputerControlOnly(t *testing.T) {
	original := &config.Config{}
	got := applySessionToolVisibility(original, false, false, false)
	for _, id := range []string{"show", "mouse"} {
		if got.Tools.ToolEnabled(id) {
			t.Errorf("%s advertised by default", id)
		}
	}
	for _, id := range sessionTerminalToolIDs {
		if !got.Tools.ToolEnabled(id) {
			t.Errorf("%s unexpectedly hidden", id)
		}
	}
	for _, id := range sessionExperimentalToolIDs {
		if got.Tools.ToolEnabled(id) {
			t.Errorf("%s advertised by default", id)
		}
	}
	if len(original.Tools.List) != 0 {
		t.Fatal("input config was mutated")
	}
}

func TestApplySessionToolVisibility_FlagsProduceExactTerminalAndComputerSurface(t *testing.T) {
	got := applySessionToolVisibility(&config.Config{}, true, true, true)
	for _, id := range []string{"show", "mouse"} {
		if !got.Tools.ToolEnabled(id) {
			t.Errorf("%s not exposed with --computer-use", id)
		}
	}
	for _, id := range sessionTerminalToolIDs {
		if got.Tools.ToolEnabled(id) {
			t.Errorf("%s exposed with --no-terminal-tools", id)
		}
	}
	for _, id := range sessionExperimentalToolIDs {
		if !got.Tools.ToolEnabled(id) {
			t.Errorf("%s not exposed with --experimental-tools", id)
		}
	}
}

func TestApplySessionToolVisibility_NoTerminalToolsWithoutOptInsIsEmpty(t *testing.T) {
	got := applySessionToolVisibility(&config.Config{}, false, false, true)
	for _, id := range config.DefaultToolIDs {
		if got.Tools.ToolEnabled(id) {
			t.Errorf("%s exposed without an opt-in", id)
		}
	}
}
