package service

import (
	"errors"
	"testing"
	"time"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
)

// playoutDecoder returns codec-sized frames; Decode marks samples with the
// payload's first byte so the emitted order is observable, and PLC frames
// are marked -1.
type playoutDecoder struct {
	samples int
	err     error
}

func (d playoutDecoder) Decode(payload []byte) ([]int16, error) {
	frame := make([]int16, d.samples)
	if len(payload) > 0 {
		frame[0] = int16(payload[0])
	}
	return frame, d.err
}

func (d playoutDecoder) DecodePLC() ([]int16, error) {
	frame := make([]int16, d.samples)
	frame[0] = -1
	return frame, d.err
}

// newPlayout builds the playout state machine directly, without the read
// loop goroutine, so every packet decision is deterministic.
func newPlayout(t *testing.T, config rtctransport.InboundTrackConfig, decoder playoutDecoder) (*inboundPlayout, *InboundTrack) {
	t.Helper()
	cfg, err := normalizeInboundConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if decoder.samples == 0 {
		decoder.samples = cfg.codecSamples
	}
	track := &InboundTrack{decoder: decoder, config: cfg, frames: make(chan frameResult, cfg.jitterPackets+1), done: make(chan struct{})}
	return &inboundPlayout{track: track, packets: make(map[int64]*rtp.Packet)}, track
}

const playoutSSRC, playoutPT = 7, 111

func rtpPacket(sequence uint16, marker byte) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 960, SSRC: playoutSSRC, PayloadType: playoutPT},
		Payload: []byte{marker},
	}
}

func emitted(track *InboundTrack) []int16 {
	var order []int16
	for {
		select {
		case result := <-track.frames:
			order = append(order, result.frame.Samples[0])
		default:
			return order
		}
	}
}

func requirePush(t *testing.T, state *inboundPlayout, packet *rtp.Packet) {
	t.Helper()
	if err := state.push(packet); err != nil {
		t.Fatalf("push(%d) = %v", packet.SequenceNumber, err)
	}
}

func requireOrder(t *testing.T, got []int16, want ...int16) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("emitted %v, want %v", got, want)
		}
	}
}

// Packets reordered inside the initial jitter window play out in sequence
// order; duplicates are ignored and a missing packet is concealed.
func TestPlayoutReordersInitialWindowAndConcealsGaps(t *testing.T) {
	state, track := newPlayout(t, rtctransport.InboundTrackConfig{}, playoutDecoder{})
	requirePush(t, state, rtpPacket(11, 11))
	requirePush(t, state, rtpPacket(10, 10)) // earlier than the first packet
	requirePush(t, state, rtpPacket(11, 99)) // duplicate is ignored
	requirePush(t, state, rtpPacket(13, 13)) // leaves a gap at 12
	for range 4 {
		if err := state.tick(); err != nil {
			t.Fatal(err)
		}
	}
	requireOrder(t, emitted(track), 10, 11, -1, 13)
	// Once playing, a late packet is obsolete and a later one extends the window.
	requirePush(t, state, rtpPacket(12, 12))
	requirePush(t, state, rtpPacket(14, 14))
	requirePush(t, state, rtpPacket(15, 15))
	if err := state.flush(); err != nil {
		t.Fatal(err)
	}
	requireOrder(t, emitted(track), 14, 15)
}

// Packets beyond the jitter window, or that change the stream identity or
// RTP clock, are rejected as impossible progress.
func TestPlayoutRejectsImpossibleProgress(t *testing.T) {
	window := int(rtctransport.DefaultInboundJitterDepth / rtctransport.DefaultInboundFrameDuration)
	far := uint16(100 + window + 1)
	changed := func(mutate func(*rtp.Packet)) *rtp.Packet {
		packet := rtpPacket(101, 1)
		mutate(packet)
		return packet
	}
	cases := map[string]struct {
		started bool
		packet  *rtp.Packet
		want    error
	}{
		"initial window ahead":  {packet: rtpPacket(far, 1), want: rtctransport.ErrImpossibleRTPProgress},
		"initial window behind": {packet: rtpPacket(100-uint16(window)-1, 1), want: rtctransport.ErrImpossibleRTPProgress},
		"playing window ahead":  {started: true, packet: rtpPacket(far+1, 1), want: rtctransport.ErrImpossibleRTPProgress},
		"version":               {packet: changed(func(p *rtp.Packet) { p.Version = 1 }), want: rtctransport.ErrInvalidInboundRTPPacket},
		"ssrc":                  {packet: changed(func(p *rtp.Packet) { p.SSRC++ }), want: rtctransport.ErrInvalidInboundRTPPacket},
		"payload type":          {packet: changed(func(p *rtp.Packet) { p.PayloadType++ }), want: rtctransport.ErrInvalidInboundRTPPacket},
		"timestamp":             {packet: changed(func(p *rtp.Packet) { p.Timestamp++ }), want: rtctransport.ErrImpossibleRTPProgress},
	}
	for name, tc := range cases {
		state, _ := newPlayout(t, rtctransport.InboundTrackConfig{}, playoutDecoder{})
		requirePush(t, state, rtpPacket(100, 0))
		if tc.started {
			if err := state.tick(); err != nil {
				t.Fatal(err)
			}
		}
		if err := state.push(tc.packet); !errors.Is(err, tc.want) {
			t.Errorf("%s: push = %v, want %v", name, err, tc.want)
		}
	}
}

func TestPlayoutDecodeAndDeliveryFailures(t *testing.T) {
	decodeErr := errors.New("bad opus")
	cases := map[string]struct {
		config  rtctransport.InboundTrackConfig
		decoder playoutDecoder
		full    bool
		want    error
	}{
		"decode":       {decoder: playoutDecoder{err: decodeErr}, want: rtctransport.ErrInboundTrackDecode},
		"codec size":   {decoder: playoutDecoder{samples: 3}, want: rtctransport.ErrInboundTrackFrame},
		"resample":     {config: rtctransport.InboundTrackConfig{SampleRate: 24000, Resample: func([]int16, int, int) ([]int16, error) { return nil, decodeErr }}, want: rtctransport.ErrInboundTrackResample},
		"resampled sz": {config: rtctransport.InboundTrackConfig{SampleRate: 24000, Resample: func([]int16, int, int) ([]int16, error) { return []int16{1}, nil }}, want: rtctransport.ErrInboundTrackFrame},
		"queue full":   {full: true, want: rtctransport.ErrInboundTrackQueueOverflow},
	}
	for name, tc := range cases {
		state, track := newPlayout(t, tc.config, tc.decoder)
		if tc.full {
			for range cap(track.frames) {
				track.frames <- frameResult{}
			}
		}
		requirePush(t, state, rtpPacket(1, 1))
		if err := state.flush(); !errors.Is(err, tc.want) {
			t.Errorf("%s: flush = %v, want %v", name, err, tc.want)
		}
	}
	state, track := newPlayout(t, rtctransport.InboundTrackConfig{SampleRate: 16000}, playoutDecoder{})
	requirePush(t, state, rtpPacket(1, 1))
	if err := state.flush(); err != nil {
		t.Fatalf("resampled flush = %v", err)
	}
	if frame := <-track.frames; len(frame.frame.Samples) != track.config.outputSamples {
		t.Fatalf("resampled frame has %d samples, want %d", len(frame.frame.Samples), track.config.outputSamples)
	}
	if err := (&inboundPlayout{track: track, packets: map[int64]*rtp.Packet{}}).flush(); err != nil {
		t.Fatalf("empty flush = %v", err)
	}
}

func TestUnwrapSequenceAcrossWrap(t *testing.T) {
	if got := unwrapSequence(2, 65534); got != 65538 {
		t.Fatalf("forward wrap = %d", got)
	}
	if got := unwrapSequence(65534, 65538); got != 65534 {
		t.Fatalf("backward wrap = %d", got)
	}
	if got := unwrapSequence(10, 12); got != 10 {
		t.Fatalf("no wrap = %d", got)
	}
}

func TestInboundConfigValidation(t *testing.T) {
	for name, config := range map[string]rtctransport.InboundTrackConfig{
		"rate":     {SampleRate: 44100},
		"duration": {FrameDuration: 3 * time.Millisecond},
		"depth":    {JitterDepth: 30 * time.Millisecond},
	} {
		if _, err := normalizeInboundConfig(config); !errors.Is(err, rtctransport.ErrInvalidInboundTrackConfig) {
			t.Errorf("%s: normalizeInboundConfig = %v", name, err)
		}
	}
}
