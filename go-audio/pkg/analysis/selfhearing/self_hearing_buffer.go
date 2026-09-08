package selfhearing

import "time"

type selfHearingBuffer struct {
	rate    int
	start   time.Duration
	end     time.Duration
	samples []int16
	have    bool
}

func (b *selfHearingBuffer) reset() {
	b.rate = 0
	b.start = 0
	b.end = 0
	b.samples = nil
	b.have = false
}

func (b *selfHearingBuffer) append(frame PCM16TimedFrame, end time.Duration, maxSamples int) (bool, error) {
	if maxSamples <= 0 {
		return false, invalidPCM16SelfHearingFrame("buffer", "maximum sample count must be positive")
	}
	discontinuous := false
	if b.have && b.rate != frame.SampleRate {
		b.reset()
		discontinuous = true
	}
	if b.have && frame.Start < b.end {
		return discontinuous, invalidPCM16SelfHearingFrame("stream", "frame overlaps a previous frame")
	}
	if b.have && frame.Start > b.end {
		b.reset()
		discontinuous = true
	}
	if !b.have {
		b.rate = frame.SampleRate
		b.start = frame.Start
		b.end = frame.Start
		b.have = true
		initialCapacity := len(frame.Samples)
		if initialCapacity > maxSamples {
			initialCapacity = maxSamples
		}
		b.samples = make([]int16, 0, initialCapacity)
	}

	if len(frame.Samples) >= maxSamples {
		// Copy only the bounded tail. The caller retains ownership of the
		// potentially much larger input slice.
		b.samples = make([]int16, maxSamples)
		copy(b.samples, frame.Samples[len(frame.Samples)-maxSamples:])
		b.start = end - samplesToDuration(maxSamples, frame.SampleRate)
		b.end = end
		return discontinuous, nil
	}
	if len(b.samples)+len(frame.Samples) > maxSamples {
		drop := len(b.samples) + len(frame.Samples) - maxSamples
		copy(b.samples, b.samples[drop:])
		b.samples = b.samples[:len(b.samples)-drop]
		b.start += samplesToDuration(drop, b.rate)
	}
	b.samples = appendBoundedPCM16(b.samples, frame.Samples, maxSamples)
	b.end = end
	return discontinuous, nil
}

func appendBoundedPCM16(dst, src []int16, maxSamples int) []int16 {
	if len(src) == 0 {
		return dst
	}
	needed := len(dst) + len(src)
	if needed > maxSamples {
		return dst
	}
	capacity := cap(dst)
	if capacity > maxSamples {
		capacity = maxSamples
	}
	if capacity < needed {
		if capacity == 0 {
			capacity = 1
		}
		for capacity < needed && capacity < maxSamples {
			next := capacity * 2
			if next <= capacity {
				capacity = maxSamples
				break
			}
			capacity = next
		}
		if capacity > maxSamples {
			capacity = maxSamples
		}
		grown := make([]int16, len(dst), capacity)
		copy(grown, dst)
		dst = grown
	} else if cap(dst) > maxSamples {
		normalized := make([]int16, len(dst), maxSamples)
		copy(normalized, dst)
		dst = normalized
	}
	return append(dst, src...)
}
