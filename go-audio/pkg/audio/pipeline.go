package audio

import (
	"context"
	"fmt"
	"io"
)

const (
	// DefaultMaxUtteranceFrames caps utterance length before a forced flush.
	// 1000 frames × 480 samples/frame ÷ 16000 samples/sec = 30 seconds.
	DefaultMaxUtteranceFrames = 1000
)

// PipelineConfig controls utterance-detection behaviour in a Pipeline.
type PipelineConfig struct {
	// MaxUtteranceFrames is the maximum number of FrameSize-sized frames to
	// accumulate before flushing an utterance regardless of VAD state.
	// Zero means no limit (not recommended for production).
	MaxUtteranceFrames int
}

// DefaultPipelineConfig returns a PipelineConfig with sensible defaults.
var DefaultPipelineConfig = PipelineConfig{
	MaxUtteranceFrames: DefaultMaxUtteranceFrames,
}

// Pipeline reads audio frames from an AudioSource, feeds them through a
// VoiceActivityDetector, and emits complete utterances as contiguous int16
// PCM slices at SampleRate Hz mono.
type Pipeline struct {
	source AudioSource
	vad    *VoiceActivityDetector
	cfg    PipelineConfig
}

// NewPipeline creates a Pipeline that draws from source and detects speech
// with vad according to cfg.
func NewPipeline(source AudioSource, vad *VoiceActivityDetector, cfg PipelineConfig) *Pipeline {
	return &Pipeline{source: source, vad: vad, cfg: cfg}
}

// ReadUtterance blocks until a complete utterance is ready and returns its
// int16 PCM samples.
//
// Termination conditions (in priority order):
//  1. VAD signals end-of-utterance (MaxSilenceFrames of silence after speech).
//  2. Accumulated frame count reaches MaxUtteranceFrames (force flush).
//  3. The AudioSource returns io.EOF; any buffered speech is returned first,
//     then subsequent calls return io.EOF.
func (p *Pipeline) ReadUtterance(ctx context.Context) ([]int16, error) {
	var accumulated []int16
	buf := make([]int16, FrameSize)
	p.vad.Reset()

	for {
		if err := p.source.ReadFrame(ctx, buf); err != nil {
			if err == io.EOF {
				if len(accumulated) > 0 {
					// Return whatever speech was buffered before the stream ended.
					return accumulated, nil
				}
				return nil, io.EOF
			}
			return nil, fmt.Errorf("read audio frame: %w", err)
		}

		include, complete := p.vad.Process(buf)
		if include {
			frame := make([]int16, FrameSize)
			copy(frame, buf)
			accumulated = append(accumulated, frame...)
		}

		// Force-flush when the utterance has grown too long.
		if p.cfg.MaxUtteranceFrames > 0 {
			totalFrames := len(accumulated) / FrameSize
			if totalFrames >= p.cfg.MaxUtteranceFrames {
				result := accumulated
				p.vad.Reset()
				return result, nil
			}
		}

		if complete && len(accumulated) > 0 {
			return accumulated, nil
		}
	}
}
