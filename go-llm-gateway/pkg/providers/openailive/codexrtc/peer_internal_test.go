package codexrtc

import (
	"context"
	"math"
	"testing"

	"github.com/pion/rtp"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// A gap in the inbound RTP sequence is concealed with Opus PLC before the
// next decoded frame, at most MaxConcealedFrames per gap; a late or repeated
// packet is dropped; the 16-bit sequence wraps.
func TestInboundSequenceGapsAreConcealed(t *testing.T) {
	peer, err := NewPeer(PeerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := peer.Close(); err != nil {
			t.Errorf("close peer: %v", err)
		}
	})
	encoder, err := codec.NewOpusEncoder()
	if err != nil {
		t.Fatal(err)
	}
	packet := func(seq uint16, index int) *rtp.Packet {
		frame := make([]int16, FrameSamples)
		for i := range frame {
			frame[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(index*FrameSamples+i)/SampleRate))
		}
		payload, err := encoder.Encode(context.Background(), frame)
		if err != nil {
			t.Fatal(err)
		}
		return &rtp.Packet{Header: rtp.Header{SequenceNumber: seq}, Payload: payload}
	}
	steps := []struct {
		seq       uint16
		frames    int
		concealed int64
	}{
		{seq: 65533, frames: 1},           // first packet: no history to conceal from
		{seq: 65534, frames: 1},           // in order
		{seq: 1, frames: 3, concealed: 2}, // wraps past 65535 and 0: two lost
		{seq: 1, frames: 0, concealed: 2}, // repeated
		{seq: 0, frames: 0, concealed: 2}, // late
		{seq: 20, frames: 1 + MaxConcealedFrames, concealed: 2 + MaxConcealedFrames}, // long gap: capped
	}
	for i, step := range steps {
		if !peer.receive(packet(step.seq, i)) {
			t.Fatalf("step %d: receive reported a closed peer", i)
		}
		if got := len(peer.inbound); got != step.frames {
			t.Fatalf("step %d (seq %d): %d frames queued, want %d", i, step.seq, got, step.frames)
		}
		for range step.frames {
			if frame := <-peer.inbound; len(frame) != FrameSamples {
				t.Fatalf("step %d: frame of %d samples", i, len(frame))
			}
		}
		if got := peer.ConcealedFrames(); got != step.concealed {
			t.Fatalf("step %d: concealed %d, want %d", i, got, step.concealed)
		}
	}
	if peer.LatePackets() != 2 {
		t.Fatalf("late packets = %d, want 2", peer.LatePackets())
	}
}
