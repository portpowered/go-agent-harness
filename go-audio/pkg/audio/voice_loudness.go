package audio

// Measured playback corrections that keep the built-in realtime voices near
// one playback level. Unknown voices retain the conservative zero-gain default.
const (
	voiceGainAshDB     = 6.2
	voiceGainBalladDB  = 9.3
	voiceGainCedarDB   = 3.9
	voiceGainCoralDB   = 10.0
	voiceGainEchoDB    = 5.5
	voiceGainMarinDB   = 5.5
	voiceGainSageDB    = 15.1
	voiceGainShimmerDB = 2.4
	voiceGainVerseDB   = 8.3
)

// VoiceLoudnessGainDB returns the measured playback correction for voice.
// Empty, unknown, unmeasured, and reference ("alloy") voices return zero.
func VoiceLoudnessGainDB(voice string) float64 {
	switch voice {
	case "ash":
		return voiceGainAshDB
	case "ballad":
		return voiceGainBalladDB
	case "cedar":
		return voiceGainCedarDB
	case "coral":
		return voiceGainCoralDB
	case "echo":
		return voiceGainEchoDB
	case "marin":
		return voiceGainMarinDB
	case "sage":
		return voiceGainSageDB
	case "shimmer":
		return voiceGainShimmerDB
	case "verse":
		return voiceGainVerseDB
	default:
		return 0
	}
}
