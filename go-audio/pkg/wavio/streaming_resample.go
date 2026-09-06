package wavio

import (
	"errors"
	"math"
)

var ErrResamplerEnded = errors.New("streaming PCM16 resampler already ended")

// PCM16Resampler preserves rational phase and filter history across arbitrary
// input chunks. Downsampling uses a causal windowed-sinc low-pass filter;
// identity conversion remains bit exact.
type PCM16Resampler interface {
	Process(input []int16, end bool) ([]int16, error)
	Reset(inputRate, outputRate int) error
	DelaySamples() int
}

type streamingPCM16Resampler struct {
	inputRate, outputRate int
	taps                  []float64
	history               []float64
	source                []int16
	sourceBase            uint64
	nextOutput            uint64
	totalInput            uint64
	ended                 bool
}

// NewPCM16Resampler creates a reusable converter for the supported rate set.
func NewPCM16Resampler(inputRate, outputRate int) (PCM16Resampler, error) {
	r := &streamingPCM16Resampler{}
	if err := r.Reset(inputRate, outputRate); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *streamingPCM16Resampler) Reset(inputRate, outputRate int) error {
	if err := validateResampleRate("input", inputRate); err != nil {
		return err
	}
	if err := validateResampleRate("output", outputRate); err != nil {
		return err
	}
	r.inputRate, r.outputRate = inputRate, outputRate
	r.history, r.source = nil, nil
	r.sourceBase, r.nextOutput, r.totalInput, r.ended = 0, 0, 0, false
	if outputRate < inputRate {
		r.taps = lowPassTaps(47, 0.46*float64(outputRate)/float64(inputRate))
	} else {
		r.taps = nil
	}
	return nil
}

func (r *streamingPCM16Resampler) DelaySamples() int {
	if len(r.taps) == 0 {
		return 0
	}
	return (len(r.taps) - 1) * r.outputRate / (2 * r.inputRate)
}

func (r *streamingPCM16Resampler) Process(input []int16, end bool) ([]int16, error) {
	if r.ended {
		return nil, ErrResamplerEnded
	}
	if r.inputRate == r.outputRate {
		out := append([]int16(nil), input...)
		r.totalInput += uint64(len(input))
		r.ended = end
		return out, nil
	}
	for _, sample := range input {
		r.pushSource(sample)
	}
	r.totalInput += uint64(len(input))
	if end {
		r.ended = true
	}
	target := ^uint64(0)
	if end {
		target = ceilRatio(r.totalInput, uint64(r.outputRate), uint64(r.inputRate))
	}
	out := make([]int16, 0, len(input)*r.outputRate/r.inputRate+2)
	for r.nextOutput < target {
		position := r.nextOutput * uint64(r.inputRate)
		index := position / uint64(r.outputRate)
		phase := position % uint64(r.outputRate)
		left, leftOK := r.sourceAt(index)
		right, rightOK := r.sourceAt(index + 1)
		if !leftOK {
			break
		}
		if !rightOK {
			if !end && phase != 0 {
				break
			}
			right = left
		}
		out = append(out, interpolatePCM16(left, right, phase, uint64(r.outputRate)))
		r.nextOutput++
	}
	r.trimSource()
	return out, nil
}

func (r *streamingPCM16Resampler) pushSource(sample int16) {
	if len(r.taps) == 0 {
		r.source = append(r.source, sample)
		return
	}
	r.history = append(r.history, float64(sample))
	if len(r.history) > len(r.taps) {
		copy(r.history, r.history[1:])
		r.history = r.history[:len(r.taps)]
	}
	var filtered float64
	for i := range r.history {
		filtered += r.history[len(r.history)-1-i] * r.taps[i]
	}
	r.source = append(r.source, saturatePCM16(int64(math.Round(filtered))))
}

func (r *streamingPCM16Resampler) sourceAt(index uint64) (int16, bool) {
	if index < r.sourceBase || index-r.sourceBase >= uint64(len(r.source)) {
		return 0, false
	}
	return r.source[index-r.sourceBase], true
}

func (r *streamingPCM16Resampler) trimSource() {
	if len(r.source) < 3 {
		return
	}
	needed := r.nextOutput * uint64(r.inputRate) / uint64(r.outputRate)
	if needed <= r.sourceBase {
		return
	}
	drop := int(needed - r.sourceBase)
	if drop > len(r.source)-2 {
		drop = len(r.source) - 2
	}
	r.source = append(r.source[:0], r.source[drop:]...)
	r.sourceBase += uint64(drop)
}

func lowPassTaps(count int, cutoff float64) []float64 {
	if count%2 == 0 {
		count++
	}
	taps := make([]float64, count)
	middle := float64(count-1) / 2
	var sum float64
	for i := range taps {
		x := float64(i) - middle
		var sinc float64
		if x == 0 {
			sinc = 2 * cutoff
		} else {
			sinc = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		window := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(count-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(count-1))
		taps[i] = sinc * window
		sum += taps[i]
	}
	for i := range taps {
		taps[i] /= sum
	}
	return taps
}

func ceilRatio(value, multiplier, divisor uint64) uint64 {
	if value == 0 {
		return 0
	}
	return (value*multiplier + divisor - 1) / divisor
}
