package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	rtctransportwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport/wire"
)

func TestPublicWireTransportIsEmbeddableWithoutCLI(t *testing.T) {
	service := rtctransportwire.NewService()
	decoder := consumerDecoder{}
	source := &consumerSource{packet: &rtp.Packet{Header: rtp.Header{
		Version: 2, SequenceNumber: 8, Timestamp: 99, SSRC: 7, PayloadType: 111,
	}, Payload: []byte{1}}}
	track, err := service.NewInboundTrack(source, decoder, rtctransport.InboundTrackConfig{})
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	defer track.Close()
	frame, err := track.ReadFrame(context.Background())
	if err != nil || len(frame.Samples) != 960 {
		t.Fatalf("ReadFrame() = %d samples, %v; want one 20ms frame", len(frame.Samples), err)
	}
	if _, err := service.NewInboundTrack(nil, decoder, rtctransport.InboundTrackConfig{}); !errors.Is(err, rtctransport.ErrNilInboundRTPTrack) {
		t.Fatalf("nil source error = %v", err)
	}
}

type consumerSource struct {
	packet *rtp.Packet
	done   bool
}

func (s *consumerSource) ReadRTP() (*rtp.Packet, error) {
	if s.done {
		return nil, errors.New("consumer source reached EOF")
	}
	s.done = true
	return s.packet, nil
}

type consumerDecoder struct{}

func (consumerDecoder) Decode([]byte) ([]int16, error) { return make([]int16, 960), nil }

func (consumerDecoder) DecodePLC() ([]int16, error) { return make([]int16, 960), nil }
