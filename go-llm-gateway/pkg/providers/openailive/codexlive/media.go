package codexlive

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
)

// Media constants.
const (
	// pacedQueueFrames bounds the input frames waiting for their 20 ms slot:
	// two seconds. A source that runs further ahead than that loses its
	// oldest frames, which are counted.
	pacedQueueFrames = 100
	// silencePeak is the sample magnitude under which a decoded output frame
	// counts as silence (about -54 dBFS). Opus decodes digital silence to
	// small non-zero samples, so zero is too strict.
	silencePeak = 64
)

// inputFramer turns session-rate PCM into 48 kHz frames of
// codexrtc.FrameSamples samples. It is not safe for concurrent use.
type inputFramer struct {
	resampler wavio.PCM16Resampler
	carry     []int16
}

func newInputFramer(rate int) (*inputFramer, error) {
	resampler, err := wavio.NewPCM16Resampler(rate, codexrtc.SampleRate)
	if err != nil {
		return nil, fmt.Errorf("codexlive: input resampler: %w", err)
	}
	return &inputFramer{resampler: resampler}, nil
}

// frames resamples samples and returns every whole frame now available; the
// remainder waits for the next call.
func (f *inputFramer) frames(samples []int16) ([][]int16, error) {
	resampled, err := f.resampler.Process(samples, false)
	if err != nil {
		return nil, fmt.Errorf("codexlive: resample input: %w", err)
	}
	f.carry = append(f.carry, resampled...)
	var out [][]int16
	for len(f.carry) >= codexrtc.FrameSamples {
		out = append(out, append([]int16(nil), f.carry[:codexrtc.FrameSamples]...))
		f.carry = f.carry[codexrtc.FrameSamples:]
	}
	if len(f.carry) == 0 {
		f.carry = nil
	}
	return out, nil
}

// flush returns the held partial frame padded with silence to a whole
// frame, or nil when nothing is held.
func (f *inputFramer) flush() []int16 {
	if len(f.carry) == 0 {
		return nil
	}
	frame := make([]int16, codexrtc.FrameSamples)
	copy(frame, f.carry)
	f.carry = nil
	return frame
}

// outputConverter turns 48 kHz peer frames into session-rate PCM. It is not
// safe for concurrent use.
type outputConverter struct {
	resampler wavio.PCM16Resampler
}

func newOutputConverter(rate int) (*outputConverter, error) {
	resampler, err := wavio.NewPCM16Resampler(codexrtc.SampleRate, rate)
	if err != nil {
		return nil, fmt.Errorf("codexlive: output resampler: %w", err)
	}
	return &outputConverter{resampler: resampler}, nil
}

// convert resamples one peer frame and reports whether it is silent.
func (c *outputConverter) convert(frame []int16) (samples []int16, silent bool, err error) {
	samples, err = c.resampler.Process(frame, false)
	if err != nil {
		return nil, false, fmt.Errorf("codexlive: resample output: %w", err)
	}
	return samples, isSilent(frame), nil
}

func isSilent(frame []int16) bool {
	for _, sample := range frame {
		if sample >= silencePeak || sample <= -silencePeak {
			return false
		}
	}
	return true
}

// pacer sends queued frames one per codexrtc.FrameDuration on its clock: the
// first frame after an idle period goes at once, each later one at the next
// 20 ms slot of that run. An idle pacer holds no timer.
type pacer struct {
	clock clock.TimerSource
	write func(context.Context, []int16) error
	fail  func(error)

	mu      sync.Mutex
	queue   [][]int16
	dropped int64
	wake    chan struct{}
}

func newPacer(source clock.TimerSource, write func(context.Context, []int16) error, fail func(error)) *pacer {
	return &pacer{clock: source, write: write, fail: fail, wake: make(chan struct{}, 1)}
}

// push queues frames, dropping the oldest beyond pacedQueueFrames.
func (p *pacer) push(frames ...[]int16) {
	if len(frames) == 0 {
		return
	}
	p.mu.Lock()
	p.queue = append(p.queue, frames...)
	if over := len(p.queue) - pacedQueueFrames; over > 0 {
		p.queue = p.queue[over:]
		p.dropped += int64(over)
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// droppedFrames reports the input frames lost to a full queue.
func (p *pacer) droppedFrames() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

func (p *pacer) pop() ([]int16, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return nil, false
	}
	frame := p.queue[0]
	p.queue = p.queue[1:]
	return frame, true
}

// run sends frames until ctx ends. A write failure is reported once through
// fail and stops the pacer.
func (p *pacer) run(ctx context.Context) {
	for {
		select {
		case <-p.wake:
		case <-ctx.Done():
			return
		}
		if !p.drain(ctx) {
			return
		}
	}
}

// drain sends one run of queued frames on their 20 ms slots, and reports
// whether the pacer should keep running.
func (p *pacer) drain(ctx context.Context) bool {
	slot := p.clock.Now()
	for {
		frame, ok := p.pop()
		if !ok {
			return true
		}
		if err := p.write(ctx, frame); err != nil {
			if ctx.Err() == nil {
				p.fail(err)
			}
			return false
		}
		slot = slot.Add(codexrtc.FrameDuration)
		if !p.sleepUntil(ctx, slot) {
			return false
		}
	}
}

func (p *pacer) sleepUntil(ctx context.Context, at time.Time) bool {
	timer := p.clock.NewTimer(at.Sub(p.clock.Now()))
	defer timer.Stop()
	select {
	case <-timer.C():
		return true
	case <-ctx.Done():
		return false
	}
}
