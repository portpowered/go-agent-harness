package agentruntime

import "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

// VoiceLoudnessGainDB returns the fixed output gain, in dB, that normalizes
// voice toward the shared cross-voice loudness target. An empty, unknown, or
// unmeasured voice returns exactly 0 (no adjustment): the map's zero value
// is the documented, deliberately conservative default described on
// openAIRealtimeVoiceLoudnessGainDB. All 10 currently-documented built-in
// voices have an independently measured entry.
func VoiceLoudnessGainDB(voice string) float64 {
	return audio.VoiceLoudnessGainDB(voice)
}
