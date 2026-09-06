package file

import (
	"fmt"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func (c *fileCapture) processSourceFrame(samples []int16) ([]sharedaudio.PCMFrame, error) {
	process := c.processor.Process
	if c.continuous {
		process = c.processor.ProcessAvailable
	}
	frames, err := process(sharedaudio.PCMFrame{Samples: samples, Epoch: c.epoch})
	if err != nil {
		return nil, fmt.Errorf("process finite audio input: %w", err)
	}
	if c.continuous && samplesAreSilent(samples) {
		for index := range frames {
			clear(frames[index].Samples)
		}
	}
	return frames, nil
}

func samplesAreSilent(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return false
		}
	}
	return true
}
