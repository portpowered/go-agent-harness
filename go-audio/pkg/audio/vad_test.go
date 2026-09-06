package audio_test

import (
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// makeSpeechFrame returns one FrameSize frame of high-energy samples
// (alternating +amplitude / -amplitude) that passes DefaultVADConfig.EnergyThreshold.
func makeSpeechFrame(amplitude int16) []int16 {
	frame := make([]int16, audio.FrameSize)
	for i := range frame {
		if i%2 == 0 {
			frame[i] = amplitude
		} else {
			frame[i] = -amplitude
		}
	}
	return frame
}

// makeSilenceFrame returns one FrameSize frame of silence (all zeros).
func makeSilenceFrame() []int16 {
	return make([]int16, audio.FrameSize)
}

// TestVAD_SilenceNeverTriggersInclusion verifies that silence frames never
// cause the VAD to include frames or signal completion.
func TestVAD_SilenceNeverTriggersInclusion(t *testing.T) {
	vad := audio.NewVAD(audio.DefaultVADConfig)
	silence := makeSilenceFrame()

	for i := 0; i < 100; i++ {
		include, complete := vad.Process(silence)
		if include {
			t.Fatalf("frame %d: silence frame marked as include", i)
		}
		if complete {
			t.Fatalf("frame %d: silence frame marked utterance complete", i)
		}
	}
}

// TestVAD_SpeechBelow_MinSpeechFrames verifies that fewer than MinSpeechFrames
// consecutive speech frames do not start an utterance.
func TestVAD_SpeechBelow_MinSpeechFrames(t *testing.T) {
	vad := audio.NewVAD(audio.DefaultVADConfig)
	speech := makeSpeechFrame(1000)

	below := audio.DefaultVADConfig.MinSpeechFrames - 1
	for i := 0; i < below; i++ {
		include, complete := vad.Process(speech)
		if include {
			t.Errorf("frame %d: expected include=false before MinSpeechFrames reached, got true", i)
		}
		if complete {
			t.Errorf("frame %d: unexpected complete=true", i)
		}
	}
}

// TestVAD_SpeechDetectedAfterMinSpeechFrames verifies that the utterance starts
// exactly when MinSpeechFrames consecutive speech frames have been seen.
func TestVAD_SpeechDetectedAfterMinSpeechFrames(t *testing.T) {
	vad := audio.NewVAD(audio.DefaultVADConfig)
	speech := makeSpeechFrame(1000)

	min := audio.DefaultVADConfig.MinSpeechFrames

	// Frames 0 … MinSpeechFrames-2 must NOT trigger inclusion.
	for i := 0; i < min-1; i++ {
		include, _ := vad.Process(speech)
		if include {
			t.Fatalf("frame %d: premature include before threshold", i)
		}
	}

	// Frame MinSpeechFrames-1 (0-indexed) crosses the threshold.
	include, complete := vad.Process(speech)
	if !include {
		t.Error("expected include=true once MinSpeechFrames reached")
	}
	if complete {
		t.Error("expected complete=false when speech just started")
	}
}

// TestVAD_SpeechContinuesWhileActive verifies that every speech frame while
// the utterance is active is included.
func TestVAD_SpeechContinuesWhileActive(t *testing.T) {
	vad := audio.NewVAD(audio.DefaultVADConfig)
	speech := makeSpeechFrame(1000)

	// Prime the detector until inSpeech=true.
	for i := 0; i < audio.DefaultVADConfig.MinSpeechFrames; i++ {
		vad.Process(speech)
	}

	for i := 0; i < 50; i++ {
		include, complete := vad.Process(speech)
		if !include {
			t.Errorf("frame %d: expected include=true for ongoing speech", i)
		}
		if complete {
			t.Errorf("frame %d: unexpected complete=true during speech", i)
		}
	}
}

// TestVAD_UtteranceEndsAfterMaxSilenceFrames verifies that the detector signals
// complete=true after MaxSilenceFrames consecutive silence frames follow speech.
func TestVAD_UtteranceEndsAfterMaxSilenceFrames(t *testing.T) {
	vad := audio.NewVAD(audio.DefaultVADConfig)
	speech := makeSpeechFrame(1000)
	silence := makeSilenceFrame()

	// Start an utterance.
	for i := 0; i < audio.DefaultVADConfig.MinSpeechFrames; i++ {
		vad.Process(speech)
	}

	max := audio.DefaultVADConfig.MaxSilenceFrames

	// Feed max-1 silence frames – not yet done.
	for i := 0; i < max-1; i++ {
		_, complete := vad.Process(silence)
		if complete {
			t.Fatalf("frame %d: unexpected complete=true before MaxSilenceFrames", i)
		}
	}

	// The max-th silence frame must trigger completion.
	_, complete := vad.Process(silence)
	if !complete {
		t.Error("expected complete=true after MaxSilenceFrames of silence")
	}
}

// TestVAD_Reset clears state so a subsequent call starts fresh.
func TestVAD_Reset(t *testing.T) {
	vad := audio.NewVAD(audio.DefaultVADConfig)
	speech := makeSpeechFrame(1000)

	// Advance into an active utterance.
	for i := 0; i < audio.DefaultVADConfig.MinSpeechFrames; i++ {
		vad.Process(speech)
	}

	vad.Reset()

	// After reset the first speech frame should NOT yet include (need MinSpeechFrames again).
	include, complete := vad.Process(speech)
	if include {
		t.Error("expected include=false immediately after Reset")
	}
	if complete {
		t.Error("expected complete=false immediately after Reset")
	}
}
