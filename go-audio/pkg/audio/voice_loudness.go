package audio

// voiceLoudnessGainDB contains the measured correction that keeps the
// built-in realtime voices near one playback level. Unknown voices retain the
// conservative zero-gain default.
var voiceLoudnessGainDB = map[string]float64{
	"alloy":   0.0,
	"ash":     6.2,
	"ballad":  9.3,
	"cedar":   3.9,
	"coral":   10.0,
	"echo":    5.5,
	"marin":   5.5,
	"sage":    15.1,
	"shimmer": 2.4,
	"verse":   8.3,
}

// VoiceLoudnessGainDB returns the measured playback correction for voice.
// Empty, unknown, and unmeasured voices return zero.
func VoiceLoudnessGainDB(voice string) float64 { return voiceLoudnessGainDB[voice] }
