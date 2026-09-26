package audio

import (
	"context"
	"sync"
)

// BufferedOutbound is the adapter for consumers of the frame media contract.
// WriteFrame only admits a copied frame to a bounded memory buffer.
type BufferedOutbound struct{ Producer FrameProducer }

func (b BufferedOutbound) WriteFrame(ctx context.Context, frame PCMFrame) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return b.Producer.Submit(ctx, frame)
}
func (b BufferedOutbound) Close() error { b.Producer.Close(); return nil }

type sessionOutboundMedia struct {
	writer    SessionMediaWriter
	done      chan struct{}
	closeOnce sync.Once
}

func newSessionOutboundMedia(writer SessionMediaWriter) *sessionOutboundMedia {
	return &sessionOutboundMedia{
		writer: writer,
		done:   make(chan struct{}),
	}
}

func (m *sessionOutboundMedia) WriteFrame(ctx context.Context, frame PCMFrame) error { //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	if ctx == nil {
		ctx = context.Background() //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	}
	if len(frame.Samples) == 0 {
		return ErrSessionMediaEmptyFrame
	}
	select {
	case <-m.done:
		return ErrSessionMediaClosed
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if m.writer == nil {
		return ErrSessionMediaNoWriter
	}

	// Do not let a provider retain or mutate the caller's frame buffer.
	samples := append([]int16(nil), frame.Samples...)
	frame.Samples = samples
	return m.writer(ctx, frame)
}

func (m *sessionOutboundMedia) Close() error {
	m.close()
	return nil
}

func (m *sessionOutboundMedia) close() {
	m.closeOnce.Do(func() {
		close(m.done)
	})
}
