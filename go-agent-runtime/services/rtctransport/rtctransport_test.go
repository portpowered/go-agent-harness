package rtctransport_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	rtctransportwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport/wire"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestInboundTransportOrdersPacketsAndUsesOnePLCFrameForOneGap(t *testing.T) {
	decoder := &testDecoder{}
	source := &testPacketSource{packets: []*rtp.Packet{
		testPacket(10, 1000, 1, 111, 1),
		testPacket(12, 2920, 1, 111, 3),
	}}
	track, err := rtctransportwire.NewService().NewInboundTrack(source, decoder, rtctransport.DefaultInboundTrackConfig())
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	defer track.Close()

	ctx := context.Background()
	for index, want := range []int16{1, -7, 3} {
		frame, readErr := track.ReadFrame(ctx)
		if readErr != nil {
			t.Fatalf("ReadFrame(%d) error = %v", index, readErr)
		}
		if len(frame.Samples) != 960 || frame.Samples[0] != want {
			t.Fatalf("ReadFrame(%d) = %d samples starting %d, want 960 starting %d", index, len(frame.Samples), frame.Samples[0], want)
		}
	}
	if decoder.plcCalls != 1 {
		t.Fatalf("DecodePLC calls = %d, want exactly one", decoder.plcCalls)
	}
	if _, readErr := track.ReadFrame(ctx); !errors.Is(readErr, rtctransport.ErrInboundTrackSource) {
		t.Fatalf("terminal ReadFrame() error = %v, want source identity", readErr)
	}
}

func TestInboundTransportRejectsNonV2AndImpossibleProgress(t *testing.T) {
	tests := []struct {
		name   string
		packet *rtp.Packet
		want   error
	}{
		{name: "non-v2", packet: testPacket(1, 1000, 1, 111, 1), want: rtctransport.ErrInvalidInboundRTPPacket},
		{name: "timestamp jump", packet: testPacket(2, 3000, 1, 111, 2), want: rtctransport.ErrImpossibleRTPProgress},
		{name: "ssrc change", packet: testPacket(2, 1960, 2, 111, 2), want: rtctransport.ErrInvalidInboundRTPPacket},
		{name: "payload change", packet: testPacket(2, 1960, 1, 112, 2), want: rtctransport.ErrInvalidInboundRTPPacket},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := testPacket(1, 1000, 1, 111, 1)
			if test.name == "non-v2" {
				first = test.packet
				first.Version = 1
			}
			packets := []*rtp.Packet{first}
			if test.name != "non-v2" {
				packets = append(packets, test.packet)
			}
			track, err := rtctransportwire.NewService().NewInboundTrack(
				&testPacketSource{packets: packets}, &testDecoder{}, rtctransport.DefaultInboundTrackConfig(),
			)
			if err != nil {
				t.Fatalf("NewInboundTrack() error = %v", err)
			}
			defer track.Close()
			for {
				_, readErr := track.ReadFrame(context.Background())
				if readErr == nil {
					continue
				}
				if !errors.Is(readErr, test.want) {
					t.Fatalf("terminal error = %v, want errors.Is(..., %v)", readErr, test.want)
				}
				break
			}
		})
	}
}

func TestOutboundTransportResamplesOwnsPacketsAndUsesMediaTimeline(t *testing.T) {
	var packets []*rtp.Packet
	var offsets []uint64
	var mu sync.Mutex
	encoder := rtctransport.OpusEncoderFunc(func(_ context.Context, samples []int16) ([]byte, error) {
		if len(samples) != 960 {
			t.Fatalf("encoder received %d samples, want 960", len(samples))
		}
		return []byte{byte(samples[0]), 0x42}, nil
	})
	writer := rtctransport.RTPWriterFunc(func(_ context.Context, packet *rtp.Packet) error {
		mu.Lock()
		defer mu.Unlock()
		packets = append(packets, packet)
		return nil
	})
	pacer := rtctransport.PacerFunc(func(_ context.Context, offset uint64) error {
		mu.Lock()
		defer mu.Unlock()
		offsets = append(offsets, offset)
		return nil
	})
	track, err := rtctransportwire.NewService().NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 16000, Encoder: encoder, Writer: writer, Pacer: pacer,
		InitialSequenceNumber: 9, InitialTimestamp: 700,
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack() error = %v", err)
	}
	defer track.Close()

	input := make([]int16, 320)
	input[0] = 11
	input[1] = -12
	input[2] = 13
	wantInput := slices.Clone(input)
	if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: input}); err != nil {
		t.Fatalf("first WriteFrame() error = %v", err)
	}
	if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: input}); err != nil {
		t.Fatalf("second WriteFrame() error = %v", err)
	}
	if !slices.Equal(input, wantInput) {
		t.Fatalf("WriteFrame mutated caller samples: got %v want %v", input, wantInput)
	}
	if len(packets) != 2 {
		t.Fatalf("packets = %d, want 2", len(packets))
	}
	if packets[0].Version != 2 || !packets[0].Marker || packets[0].SequenceNumber != 9 || packets[0].Timestamp != 700 {
		t.Fatalf("first RTP header = %+v, want v2 marker seq=9 timestamp=700", packets[0].Header)
	}
	if packets[1].Marker || packets[1].SequenceNumber != 10 || packets[1].Timestamp != 1660 {
		t.Fatalf("second RTP header = %+v, want no marker seq=10 timestamp=1660", packets[1].Header)
	}
	if !slices.Equal(offsets, []uint64{0, 960}) {
		t.Fatalf("pacer offsets = %v, want [0 960]", offsets)
	}

	packets[0].Payload[0] = 0
	if packets[1].Payload[0] == packets[0].Payload[0] {
		t.Fatalf("packet payloads unexpectedly alias each other")
	}
}

func TestOutboundTransportCommitsTimelineOnlyAfterSuccessfulWrite(t *testing.T) {
	var packets []*rtp.Packet
	writeCount := 0
	writerErr := errors.New("writer failed")
	track, err := rtctransportwire.NewService().NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000,
		Encoder: rtctransport.OpusEncoderFunc(func(context.Context, []int16) ([]byte, error) {
			return []byte{0x7f}, nil
		}),
		Writer: rtctransport.RTPWriterFunc(func(_ context.Context, packet *rtp.Packet) error {
			packets = append(packets, packet)
			writeCount++
			if writeCount == 1 {
				return writerErr
			}
			return nil
		}),
		Pacer:                 rtctransport.PacerFunc(func(context.Context, uint64) error { return nil }),
		InitialSequenceNumber: 30, InitialTimestamp: 4000,
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack() error = %v", err)
	}
	defer track.Close()
	frame := sharedaudio.PCMFrame{Samples: make([]int16, 960)}
	if writeErr := track.WriteFrame(context.Background(), frame); !errors.Is(writeErr, writerErr) {
		t.Fatalf("first WriteFrame() error = %v, want writer identity", writeErr)
	}
	if writeErr := track.WriteFrame(context.Background(), frame); writeErr != nil {
		t.Fatalf("second WriteFrame() error = %v", writeErr)
	}
	if len(packets) != 2 {
		t.Fatalf("packets = %d, want 2", len(packets))
	}
	if packets[1].SequenceNumber != 30 || packets[1].Timestamp != 4000 || !packets[1].Marker {
		t.Fatalf("retry RTP header = %+v, want initial committed state", packets[1].Header)
	}
}

func TestTransportConstructionRejectsInvalidDependencies(t *testing.T) {
	service := rtctransportwire.NewService()
	if _, err := service.NewInboundTrack(nil, &testDecoder{}, rtctransport.DefaultInboundTrackConfig()); !errors.Is(err, rtctransport.ErrNilInboundRTPTrack) {
		t.Fatalf("nil inbound source error = %v", err)
	}
	if _, err := service.NewOutboundTrack(rtctransport.OutboundTrackConfig{}); !errors.Is(err, rtctransport.ErrOutboundNilEncoder) {
		t.Fatalf("nil outbound encoder error = %v", err)
	}
}

type testPacketSource struct {
	packets []*rtp.Packet
	index   int
}

func (s *testPacketSource) ReadRTP() (*rtp.Packet, error) {
	if s.index == len(s.packets) {
		return nil, io.EOF
	}
	packet := s.packets[s.index]
	s.index++
	return packet, nil
}

type testDecoder struct {
	plcCalls int
}

func (d *testDecoder) Decode(payload []byte) ([]int16, error) {
	value := int16(payload[0])
	return makeSamples(value), nil
}

func (d *testDecoder) DecodePLC() ([]int16, error) {
	d.plcCalls++
	return makeSamples(-7), nil
}

func makeSamples(value int16) []int16 {
	samples := make([]int16, 960)
	for index := range samples {
		samples[index] = value
	}
	return samples
}

func testPacket(sequence uint16, timestamp uint32, ssrc uint32, payloadType uint8, value byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{
		Version: 2, SequenceNumber: sequence, Timestamp: timestamp,
		SSRC: ssrc, PayloadType: payloadType,
	}, Payload: []byte{value}}
}
