package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestServiceDelegatesCoreAndPreservesRuntimeFrameDefault(t *testing.T) {
	inbound, err := New().NewInboundTrack(
		&serviceTestSource{packet: &rtp.Packet{Header: rtp.Header{
			Version: 2, SequenceNumber: 1, Timestamp: 100, SSRC: 2, PayloadType: 111,
		}, Payload: []byte{1}}},
		serviceTestDecoder{}, rtctransport.InboundTrackConfig{},
	)
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	frame, err := inbound.ReadFrame(context.Background())
	if err != nil || len(frame.Samples) != 960 {
		t.Fatalf("inbound ReadFrame() = %d samples, %v; want 960/nil", len(frame.Samples), err)
	}
	if err := inbound.Close(); err != nil {
		t.Fatalf("inbound Close() error = %v", err)
	}

	outbound, err := New().NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000,
		Encoder: rtctransport.OpusEncoderFunc(func(context.Context, []int16) ([]byte, error) {
			return []byte{1}, nil
		}),
		Writer: rtctransport.RTPWriterFunc(func(context.Context, *rtp.Packet) error { return nil }),
		Pacer:  rtctransport.PacerFunc(func(context.Context, uint64) error { return nil }),
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := outbound.Close(); closeErr != nil {
			t.Errorf("outbound Close() error = %v", closeErr)
		}
	}()
	if err := outbound.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: make([]int16, 959)}); !errors.Is(err, rtctransport.ErrOutboundFrameSize) {
		t.Fatalf("runtime partial frame = %v, want frame-size identity", err)
	}
	if err := outbound.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: make([]int16, 960)}); err != nil {
		t.Fatalf("runtime full frame = %v", err)
	}
}

type serviceTestSource struct {
	packet *rtp.Packet
	done   bool
}

func (s *serviceTestSource) ReadRTP() (*rtp.Packet, error) {
	if s.done {
		return nil, io.EOF
	}
	s.done = true
	return s.packet, nil
}

type serviceTestDecoder struct{}

func (serviceTestDecoder) Decode([]byte) ([]int16, error) { return make([]int16, 960), nil }
func (serviceTestDecoder) DecodePLC() ([]int16, error)    { return make([]int16, 960), nil }
