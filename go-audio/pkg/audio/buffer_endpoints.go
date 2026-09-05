package audio

import "context"

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
