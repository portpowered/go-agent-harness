package mixer

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// MixedFrame pairs one mixed PCM frame with the source IDs that contributed
// samples or a terminal boundary to that cadence. The source list is kept
// outside audio.PCMFrame so source interruption epochs never become a shared
// playback epoch, while evidence owners can still attribute delivery.
type MixedFrame struct {
	Frame   audio.PCMFrame
	Sources []string
}

// MixedFrameReader is the optional diagnostic view of a mix output. Ordinary
// providers use Mixer.Output and receive only the canonical PCM endpoint.
type MixedFrameReader interface {
	ReadMixedFrame(context.Context) (MixedFrame, error)
}

func (m *Mixer) Output() audio.InboundMedia {
	if m == nil {
		return mixedFrameStream{}
	}
	return mixedFrameStream{frames: m.output}
}

// OutputWithSources retains source attribution for evidence and diagnostics.
// It shares the same bounded output queue as Output; callers must choose one
// reader for a mixer rather than consuming both views concurrently.
func (m *Mixer) OutputWithSources() MixedFrameReader {
	if m == nil {
		return mixedFrameStream{}
	}
	return mixedFrameStream{frames: m.output}
}

type mixedFrameStream struct{ frames <-chan MixedFrame }

func (s mixedFrameStream) ReadMixedFrame(ctx context.Context) (MixedFrame, error) {
	if ctx == nil {
		return MixedFrame{}, ErrContextUnavailable
	}
	select {
	case frame, ok := <-s.frames:
		if !ok {
			return MixedFrame{}, io.EOF
		}
		frame.Sources = append([]string(nil), frame.Sources...)
		return frame, nil
	case <-ctx.Done():
		return MixedFrame{}, ctx.Err()
	}
}

func (s mixedFrameStream) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	frame, err := s.ReadMixedFrame(ctx)
	return frame.Frame, err
}

func (mixedFrameStream) Close() error { return nil }
