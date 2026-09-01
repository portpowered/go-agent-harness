//go:build !nomicrophone && cgo

package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

// MicrophoneSource captures PCM audio from the default system capture device
// at SampleRate Hz, mono, int16 little-endian.
//
// Frames are buffered in a channel; ReadFrame blocks until a frame is
// available or the context is cancelled.
type MicrophoneSource struct {
	malgoCtx *malgo.AllocatedContext
	device   *malgo.Device
	frameCh  chan []int16
	mu       sync.Mutex
	pending  []int16
	closed   bool
	stats    CaptureQueueStats
}

// NewMicrophoneSource opens the default capture device and begins recording.
// Callers must call Close when done.
func NewMicrophoneSource() (*MicrophoneSource, error) {
	malgoCtx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init audio context: %w", err)
	}

	m := &MicrophoneSource{
		malgoCtx: malgoCtx,
		// Buffer up to 64 frames (~1.9 s) before dropping.
		frameCh: make(chan []int16, 64),
		stats:   CaptureQueueStats{DropPolicy: "drop_oldest"},
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = uint32(Channels)
	cfg.SampleRate = uint32(SampleRate)
	cfg.Alsa.NoMMap = 1 // avoids mmap issues on some ALSA configurations

	device, err := malgo.InitDevice(malgoCtx.Context, cfg, malgo.DeviceCallbacks{
		Data: func(_, input []byte, frames uint32) {
			m.onCapture(input, int(frames))
		},
	})
	if err != nil {
		_ = malgoCtx.Uninit()
		malgoCtx.Free()
		return nil, fmt.Errorf("init capture device: %w", err)
	}
	m.device = device

	if err := device.Start(); err != nil {
		device.Uninit()
		_ = malgoCtx.Uninit()
		malgoCtx.Free()
		return nil, fmt.Errorf("start microphone: %w", err)
	}
	return m, nil
}

// onCapture is called by malgo's audio thread with raw captured bytes.
// It assembles complete FrameSize frames and dispatches them to frameCh.
func (m *MicrophoneSource) onCapture(raw []byte, frames int) {
	if frames > len(raw)/2 {
		frames = len(raw) / 2
	}
	if frames <= 0 {
		return
	}
	samples := make([]int16, frames)
	for i := 0; i < frames; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return
	}
	m.stats.CapturedSamples += uint64(frames)

	m.pending = append(m.pending, samples...)
	for len(m.pending) >= FrameSize {
		frame := make([]int16, FrameSize)
		copy(frame, m.pending[:FrameSize])
		m.pending = m.pending[FrameSize:]
		m.stats.CompletedFrames++
		select {
		case m.frameCh <- frame:
		default:
			// Preserve real-time freshness: evict one stale queued frame, then
			// admit the newly completed frame. Every loss remains observable.
			select {
			case <-m.frameCh:
			default:
			}
			m.frameCh <- frame
			m.stats.DroppedFrames++
			m.stats.DroppedSamples += FrameSize
			m.stats.SequenceGaps++
		}
		m.stats.QueuedSamples = len(m.frameCh) * FrameSize
		if m.stats.QueuedSamples > m.stats.HighWaterSamples {
			m.stats.HighWaterSamples = m.stats.QueuedSamples
		}
	}
}

// CaptureStats returns the explicit native capture overflow policy and exact
// cumulative loss counters.
func (m *MicrophoneSource) CaptureStats() CaptureQueueStats {
	if m == nil {
		return CaptureQueueStats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := m.stats
	stats.QueuedSamples = len(m.frameCh) * FrameSize
	return stats
}

// ReadFrame blocks until FrameSize samples are ready or ctx is cancelled.
func (m *MicrophoneSource) ReadFrame(ctx context.Context, buf []int16) error {
	select {
	case frame, ok := <-m.frameCh:
		if !ok {
			return fmt.Errorf("microphone closed")
		}
		copy(buf, frame)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the capture device and releases all resources.
// It is safe to call Close more than once.
func (m *MicrophoneSource) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()

	// Stop() is synchronous: waits for any in-flight callback to complete,
	// so it is safe to close the channel immediately after.
	_ = m.device.Stop()
	m.device.Uninit()
	close(m.frameCh)
	_ = m.malgoCtx.Uninit()
	m.malgoCtx.Free()
	return nil
}
