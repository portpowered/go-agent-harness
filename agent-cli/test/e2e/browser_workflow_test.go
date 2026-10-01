//go:build e2e

package e2e

import (
	"testing"
)

// TestPaperieMarginBrowserWorkflow covers billed, multi-page browser tool use.
func TestPaperieMarginBrowserWorkflow(t *testing.T) {
	runScenario(t, "./agent-cli/internal/transport/cli", "TestSessionPaperieMarginFromBaselineAgentsMD")
}
