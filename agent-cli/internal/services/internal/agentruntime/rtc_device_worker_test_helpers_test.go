package agentruntime

import (
	"context"
	"sync"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type recordingRTCOutboundMedia struct {
	mu               sync.Mutex
	frames           []audio.PCMFrame
	writeErr         error
	cancelAfterFirst context.CancelFunc
}

func (m *recordingRTCOutboundMedia) WriteFrame(_ context.Context, frame audio.PCMFrame) error {
	m.mu.Lock()
	copyFrame := audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}
	m.frames = append(m.frames, copyFrame)
	cancel := m.cancelAfterFirst
	if len(m.frames) == 1 {
		m.cancelAfterFirst = nil
	} else {
		cancel = nil
	}
	err := m.writeErr
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return err
}

func (m *recordingRTCOutboundMedia) Close() error { return nil }

var _ audio.OutboundMedia = (*recordingRTCOutboundMedia)(nil)
