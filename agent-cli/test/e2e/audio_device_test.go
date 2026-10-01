//go:build e2e

package e2e

import (
	"testing"
)

// TestCubecadeAudioDevice covers the production agent binary, browser, remote
// audio device, and OpenAI Realtime boundary as one billed scenario.
func TestCubecadeAudioDevice(t *testing.T) {
	runScenario(t, "./agent-cli/internal/webmcp/chrome", "TestPinnedChromeCubecadeAgentUsesAudioDeviceServer")
}
