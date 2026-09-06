package audio_test

import (
	"context"
	"io"
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// makeSpeech returns n frames of high-energy PCM samples.
func makeSpeech(frames int) []int16 {
	n := audio.FrameSize * frames
	s := make([]int16, n)
	for i := range s {
		if i%2 == 0 {
			s[i] = 1000
		} else {
			s[i] = -1000
		}
	}
	return s
}

// makeSilence returns n frames of silent (zero) PCM samples.
func makeSilence(frames int) []int16 {
	return make([]int16, audio.FrameSize*frames)
}

// TestPipeline_EmitsUtteranceAfterSpeechAndSilence verifies that the pipeline
// accumulates speech frames and emits the utterance once trailing silence
// exceeds MaxSilenceFrames.
func TestPipeline_EmitsUtteranceAfterSpeechAndSilence(t *testing.T) {
	speechFrames := 20
	silenceFrames := audio.DefaultVADConfig.MaxSilenceFrames

	samples := append(makeSpeech(speechFrames), makeSilence(silenceFrames)...)
	src := audio.NewSliceSource(samples)
	vad := audio.NewVAD(audio.DefaultVADConfig)
	pipeline := audio.NewPipeline(src, vad, audio.DefaultPipelineConfig)

	utterance, err := pipeline.ReadUtterance(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(utterance) == 0 {
		t.Fatal("expected non-empty utterance")
	}

	// Utterance must contain at least the confirmed speech frames
	// (speechFrames - MinSpeechFrames + 1) × FrameSize samples.
	minExpected := (speechFrames-audio.DefaultVADConfig.MinSpeechFrames+1)*audio.FrameSize +
		// …plus the silence frames included while waiting for VAD end.
		(audio.DefaultVADConfig.MaxSilenceFrames-1)*audio.FrameSize
	if len(utterance) < minExpected {
		t.Errorf("utterance too short: got %d samples, want >= %d", len(utterance), minExpected)
	}
}

// TestPipeline_ForceFlushAtMaxLength verifies that an utterance is emitted when
// accumulated speech reaches MaxUtteranceFrames, even without trailing silence.
func TestPipeline_ForceFlushAtMaxLength(t *testing.T) {
	maxFrames := 10
	cfg := audio.PipelineConfig{MaxUtteranceFrames: maxFrames}

	// Provide more speech than maxFrames so the force-flush triggers.
	src := audio.NewSliceSource(makeSpeech(maxFrames + 5))
	vad := audio.NewVAD(audio.DefaultVADConfig)
	pipeline := audio.NewPipeline(src, vad, cfg)

	utterance, err := pipeline.ReadUtterance(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After force-flush the utterance must be exactly maxFrames × FrameSize.
	expected := maxFrames * audio.FrameSize
	if len(utterance) != expected {
		t.Errorf("force-flush utterance length: got %d, want %d", len(utterance), expected)
	}
}

// TestPipeline_EOFWithNoSpeech verifies that ReadUtterance returns io.EOF when
// the source is empty and no speech was detected.
func TestPipeline_EOFWithNoSpeech(t *testing.T) {
	src := audio.NewSliceSource(makeSilence(5)) // only silence
	vad := audio.NewVAD(audio.DefaultVADConfig)
	pipeline := audio.NewPipeline(src, vad, audio.DefaultPipelineConfig)

	_, err := pipeline.ReadUtterance(context.Background())
	if err != io.EOF {
		t.Fatalf("expected io.EOF for empty speech source, got: %v", err)
	}
}

// TestPipeline_PartialSpeechReturnedOnEOF verifies that speech accumulated
// before an unexpected EOF is returned rather than discarded.
func TestPipeline_PartialSpeechReturnedOnEOF(t *testing.T) {
	// Enough speech to pass MinSpeechFrames but no trailing silence.
	speechFrames := audio.DefaultVADConfig.MinSpeechFrames + 5
	src := audio.NewSliceSource(makeSpeech(speechFrames))
	vad := audio.NewVAD(audio.DefaultVADConfig)
	pipeline := audio.NewPipeline(src, vad, audio.DefaultPipelineConfig)

	utterance, err := pipeline.ReadUtterance(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(utterance) == 0 {
		t.Error("expected partial utterance to be returned on EOF, got empty slice")
	}
}

// TestPipeline_ContextCancellation verifies that ReadUtterance returns the
// context error when the context is cancelled before any audio is received.
func TestPipeline_ContextCancellation(t *testing.T) {
	// Use a source that never returns (nil-blocking is simulated by an empty slice
	// that is immediately EOF, but we cancel before anything arrives).
	// A more direct test: cancel an already-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	// We need at least one frame read attempt to hit the ctx.Done() path,
	// so provide real silence but a cancelled context.
	src := audio.NewSliceSource(makeSilence(1))
	// Override ReadFrame to block and wait for ctx.
	// SliceSource returns immediately, so we wrap it.
	blockingSrc := &blockOnFirstRead{inner: src}
	vad := audio.NewVAD(audio.DefaultVADConfig)
	pipeline := audio.NewPipeline(blockingSrc, vad, audio.DefaultPipelineConfig)

	_, err := pipeline.ReadUtterance(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

// blockOnFirstRead wraps an AudioSource and blocks ReadFrame until the context
// is done, then delegates to the inner source.  Used to force context cancellation.
type blockOnFirstRead struct {
	inner   audio.AudioSource
	blocked bool
}

func (b *blockOnFirstRead) ReadFrame(ctx context.Context, buf []int16) error {
	if !b.blocked {
		b.blocked = true
		<-ctx.Done()
		return ctx.Err()
	}
	return b.inner.ReadFrame(ctx, buf)
}

func (b *blockOnFirstRead) Close() error { return b.inner.Close() }
