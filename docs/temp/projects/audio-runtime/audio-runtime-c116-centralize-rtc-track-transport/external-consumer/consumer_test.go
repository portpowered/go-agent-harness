package consumer_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	rtctransportwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport/wire"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()
	frame, err := track.ReadFrame(context.Background())
	if err != nil || len(frame.Samples) != 960 {
		t.Fatalf("ReadFrame() = %d samples, %v; want one 20ms frame", len(frame.Samples), err)
	}
	if _, err := service.NewInboundTrack(nil, decoder, rtctransport.InboundTrackConfig{}); !errors.Is(err, rtctransport.ErrNilInboundRTPTrack) {
		t.Fatalf("nil source error = %v", err)
	}
}

func TestPublicWireTransportWritesOwnedOutboundRTP(t *testing.T) {
	var packets []*rtp.Packet
	var offsets []uint64
	encoded := []byte{0xa1, 0xb2}
	service := rtctransportwire.NewService()
	track, err := service.NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000,
		Encoder: rtctransport.OpusEncoderFunc(func(_ context.Context, samples []int16) ([]byte, error) {
			if len(samples) != 960 {
				t.Fatalf("encoder samples = %d, want 960", len(samples))
			}
			return encoded, nil
		}),
		Writer: rtctransport.RTPWriterFunc(func(_ context.Context, packet *rtp.Packet) error {
			packets = append(packets, packet)
			return nil
		}),
		Pacer: rtctransport.PacerFunc(func(_ context.Context, offset uint64) error {
			offsets = append(offsets, offset)
			return nil
		}),
		InitialSequenceNumber: 41,
		InitialTimestamp:      9000,
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

	input := make([]int16, 960)
	input[0], input[1] = 11, -12
	wantInput := slices.Clone(input)
	frame := sharedaudio.PCMFrame{Samples: input}
	if err := track.WriteFrame(context.Background(), frame); err != nil {
		t.Fatalf("first WriteFrame() error = %v", err)
	}
	encoded[0] = 0xff
	if err := track.WriteFrame(context.Background(), frame); err != nil {
		t.Fatalf("second WriteFrame() error = %v", err)
	}
	if !slices.Equal(input, wantInput) {
		t.Fatalf("WriteFrame mutated caller samples: got %v want %v", input, wantInput)
	}
	if len(packets) != 2 || !slices.Equal(offsets, []uint64{0, 960}) {
		t.Fatalf("packets/offsets = %d/%v, want 2/[0 960]", len(packets), offsets)
	}
	if packets[0].Version != 2 || !packets[0].Marker || packets[0].SequenceNumber != 41 || packets[0].Timestamp != 9000 {
		t.Fatalf("first RTP header = %+v, want v2 marker seq=41 timestamp=9000", packets[0].Header)
	}
	if packets[1].Marker || packets[1].SequenceNumber != 42 || packets[1].Timestamp != 9960 {
		t.Fatalf("second RTP header = %+v, want no marker seq=42 timestamp=9960", packets[1].Header)
	}
	if packets[0].Payload[0] != 0xa1 || packets[1].Payload[0] != 0xff {
		t.Fatalf("encoded payload ownership = %#v/%#v, want a1/ff", packets[0].Payload, packets[1].Payload)
	}
}

func TestPublicWireTransportCancellationPreservesTypedCause(t *testing.T) {
	started := make(chan struct{})
	var startedOnce bool
	track, err := rtctransportwire.NewService().NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000,
		Encoder:    rtctransport.OpusEncoderFunc(func(context.Context, []int16) ([]byte, error) { return []byte{1}, nil }),
		Writer:     rtctransport.RTPWriterFunc(func(context.Context, *rtp.Packet) error { return nil }),
		Pacer: rtctransport.PacerFunc(func(ctx context.Context, _ uint64) error {
			if !startedOnce {
				startedOnce = true
				close(started)
			}
			<-ctx.Done()
			return context.Cause(ctx)
		}),
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- track.WriteFrame(ctx, sharedaudio.PCMFrame{Samples: make([]int16, 960)}) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pacer did not start")
	}
	cancel()
	select {
	case writeErr := <-result:
		if !errors.Is(writeErr, context.Canceled) {
			t.Fatalf("canceled WriteFrame() error = %v, want context.Canceled", writeErr)
		}
		var operationErr *rtctransport.OutboundOperationError
		if !errors.As(writeErr, &operationErr) {
			t.Fatalf("canceled WriteFrame() error = %T, want *OutboundOperationError", writeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled WriteFrame() did not return")
	}
}

func TestPublicWireTransportBoundsQueueAndClose(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	track, err := rtctransportwire.NewService().NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000,
		QueueDepth: 1,
		Encoder:    rtctransport.OpusEncoderFunc(func(context.Context, []int16) ([]byte, error) { return []byte{1}, nil }),
		Writer:     rtctransport.RTPWriterFunc(func(context.Context, *rtp.Packet) error { return nil }),
		Pacer: rtctransport.PacerFunc(func(ctx context.Context, _ uint64) error {
			startedOnce.Do(func() { close(started) })
			<-ctx.Done()
			return context.Cause(ctx)
		}),
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack() error = %v", err)
	}
	frame := sharedaudio.PCMFrame{Samples: make([]int16, 960)}
	first := make(chan error, 1)
	go func() { first <- track.WriteFrame(context.Background(), frame) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first write did not reach pacer")
	}
	if writeErr := track.WriteFrame(context.Background(), frame); !errors.Is(writeErr, rtctransport.ErrOutboundQueueOverflow) {
		t.Fatalf("saturated WriteFrame() error = %v, want queue overflow", writeErr)
	}
	if closeErr := track.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	if writeErr := <-first; !errors.Is(writeErr, rtctransport.ErrOutboundClosed) {
		t.Fatalf("canceled first WriteFrame() error = %v, want closed identity", writeErr)
	}
	if writeErr := track.WriteFrame(context.Background(), frame); !errors.Is(writeErr, rtctransport.ErrOutboundClosed) {
		t.Fatalf("post-close WriteFrame() error = %v, want closed identity", writeErr)
	}
}

func TestPublicWireTransportPreservesTypedValidationAndWriterErrors(t *testing.T) {
	service := rtctransportwire.NewService()
	if _, err := service.NewOutboundTrack(rtctransport.OutboundTrackConfig{}); !errors.Is(err, rtctransport.ErrOutboundNilEncoder) {
		t.Fatalf("nil encoder error = %v, want encoder identity", err)
	}
	writerErr := errors.New("external consumer writer failure")
	track, err := service.NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000,
		Encoder:    rtctransport.OpusEncoderFunc(func(context.Context, []int16) ([]byte, error) { return []byte{1}, nil }),
		Writer:     rtctransport.RTPWriterFunc(func(context.Context, *rtp.Packet) error { return writerErr }),
		Pacer:      rtctransport.PacerFunc(func(context.Context, uint64) error { return nil }),
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()
	if writeErr := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: make([]int16, 959)}); !errors.Is(writeErr, rtctransport.ErrOutboundFrameSize) {
		t.Fatalf("invalid frame error = %v, want frame-size identity", writeErr)
	}
	writeErr := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: make([]int16, 960)})
	if !errors.Is(writeErr, writerErr) {
		t.Fatalf("writer error = %v, want underlying writer identity", writeErr)
	}
	var operationErr *rtctransport.OutboundOperationError
	if !errors.As(writeErr, &operationErr) {
		t.Fatalf("writer error = %T, want *OutboundOperationError", writeErr)
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
