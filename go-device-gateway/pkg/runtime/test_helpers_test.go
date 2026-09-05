package runtime

import (
	"context"
	"io"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func hasNonZeroSamples(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}

type recordingRTCInboundMedia struct {
	frames []audio.PCMFrame
	index  int
	err    error
}

func (m *recordingRTCInboundMedia) ReadFrame(context.Context) (audio.PCMFrame, error) {
	if m.index < len(m.frames) {
		frame := m.frames[m.index]
		m.index++
		return frame, nil
	}
	if m.err != nil {
		return audio.PCMFrame{}, m.err
	}
	return audio.PCMFrame{}, io.EOF
}

func (m *recordingRTCInboundMedia) Close() error { return nil }

var _ audio.InboundMedia = (*recordingRTCInboundMedia)(nil)
