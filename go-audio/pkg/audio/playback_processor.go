package audio

// PlaybackProcessor owns DSP state between response and interruption boundaries.
// A blocked generation discards its pending tail; a normally completed response
// flushes its exact tail. Its owner serializes calls on the media worker.
type PlaybackProcessor struct {
	processor  *Processor
	generation uint64
	blocked    bool
	last       PCMFrame
	started    bool
}

func NewPlaybackProcessor(input, output DeviceFormat, quantum int) (*PlaybackProcessor, error) {
	p, err := NewProcessor(input, output, quantum)
	if err != nil {
		return nil, err
	}
	return &PlaybackProcessor{processor: p}, nil
}

func (p *PlaybackProcessor) Process(frame PCMFrame, generation uint64, blocked bool) ([]PCMFrame, error) {
	if generation != p.generation || blocked || p.blocked || p.processor.ended {
		if _, err := p.processor.Reset(); err != nil {
			return nil, err
		}
		p.started = false
	}
	p.generation, p.blocked = generation, blocked
	if blocked {
		return nil, nil
	}
	frame.Epoch = generation
	p.last, p.started = frame, true
	return p.processor.Process(frame)
}

func (p *PlaybackProcessor) Flush(generation uint64, blocked bool) ([]PCMFrame, error) {
	if blocked || p.blocked || generation != p.generation {
		_, err := p.processor.Reset()
		p.generation, p.blocked, p.started = generation, blocked, false
		return nil, err
	}
	if !p.started || p.processor.ended {
		return nil, nil
	}
	frame := p.last
	frame.Samples, frame.EndOfResponse = nil, true
	return p.processor.Process(frame)
}
