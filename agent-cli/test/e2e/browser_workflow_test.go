//go:build e2e

package e2e

import (
	"os"
	"testing"
)

// TestPaperieMarginBrowserWorkflow covers billed, multi-page browser tool use.
func TestPaperieMarginBrowserWorkflow(t *testing.T) {
	if os.Getenv("WEBMCP_PAPERIE_MARGIN_LIVE") != "1" {
		t.Skip("set WEBMCP_PAPERIE_MARGIN_LIVE=1 to run the billed browser workflow")
	}
	runScenario(t, "./agent-cli/internal/transport/cli", "TestSessionPaperieMarginFromBaselineAgentsMD")
}
