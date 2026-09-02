//go:build e2e

package e2e

import (
	"os"
	"testing"
)

// TestCubecadeAudioDevice covers the production agent binary, browser, remote
// audio device, and OpenAI Realtime boundary as one billed scenario.
func TestCubecadeAudioDevice(t *testing.T) {
	if os.Getenv("WEBMCP_CUBECADE_AUDIO_DEVICE_LIVE") != "1" {
		t.Skip("set WEBMCP_CUBECADE_AUDIO_DEVICE_LIVE=1 to run the billed audio-device scenario")
	}
	runScenario(t, "./agent-cli/internal/webmcp/chrome", "TestPinnedChromeCubecadeAgentUsesAudioDeviceServer")
}
