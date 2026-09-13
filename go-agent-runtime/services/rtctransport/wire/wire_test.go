package wire

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestInboundTransportOrdersPacketsAndUsesOnePLCFrameForOneGap(t *testing.T) {
	decoder := &testDecoder{}
	source := &testPacketSource{packets: []*rtp.Packet{
		testPacket(10, 1000, 1, 111, 1),
		testPacket(12, 2920, 1, 111, 3),
	}}
	track, err := NewService().NewInboundTrack(source, decoder, rtctransport.InboundTrackConfig{})
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

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
		t.Run(test.name, func(t *testing.T) { testInboundRejection(t, test.name, test.packet, test.want) })
	}
}

func testInboundRejection(t *testing.T, name string, packet *rtp.Packet, want error) {
	t.Helper()
	first := testPacket(1, 1000, 1, 111, 1)
	if name == "non-v2" {
		first = packet
		first.Version = 1
	}
	packets := []*rtp.Packet{first}
	if name != "non-v2" {
		packets = append(packets, packet)
	}
	track, err := NewService().NewInboundTrack(
		&testPacketSource{packets: packets}, &testDecoder{}, rtctransport.InboundTrackConfig{},
	)
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()
	for {
		_, readErr := track.ReadFrame(context.Background())
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, want) {
			t.Fatalf("terminal error = %v, want errors.Is(..., %v)", readErr, want)
		}
		var typedErr *rtctransport.InboundTrackError
		if !errors.As(readErr, &typedErr) {
			t.Fatalf("terminal error = %T, want *InboundTrackError", readErr)
		}
		if !errors.Is(typedErr.Kind, want) {
			t.Fatalf("terminal error kind = %v, want %v", typedErr.Kind, want)
		}
		return
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
	track, err := NewService().NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 16000, Encoder: encoder, Writer: writer, Pacer: pacer,
		InitialSequenceNumber: 9, InitialTimestamp: 700,
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

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
	track, err := NewService().NewOutboundTrack(rtctransport.OutboundTrackConfig{
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
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()
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

func TestOutboundTransportRejectsPartialFramesAndBoundsConcurrentWriters(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	track, err := NewService().NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000,
		QueueDepth: 1,
		Encoder: rtctransport.OpusEncoderFunc(func(context.Context, []int16) ([]byte, error) {
			return []byte{1}, nil
		}),
		Writer: rtctransport.RTPWriterFunc(func(context.Context, *rtp.Packet) error { return nil }),
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
	if writeErr := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: make([]int16, 959)}); !errors.Is(writeErr, rtctransport.ErrOutboundFrameSize) {
		t.Fatalf("partial WriteFrame() error = %v, want frame-size identity", writeErr)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- track.WriteFrame(context.Background(), frame) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first writer did not reach the pacer")
	}
	if writeErr := track.WriteFrame(context.Background(), frame); !errors.Is(writeErr, rtctransport.ErrOutboundQueueOverflow) {
		t.Fatalf("saturated WriteFrame() error = %v, want queue overflow identity", writeErr)
	}
	if closeErr := track.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	if writeErr := <-firstDone; !errors.Is(writeErr, rtctransport.ErrOutboundClosed) {
		t.Fatalf("canceled first WriteFrame() error = %v, want closed identity", writeErr)
	}
}

func TestTransportConstructionRejectsInvalidDependencies(t *testing.T) {
	service := NewService()
	if _, err := service.NewInboundTrack(nil, &testDecoder{}, rtctransport.InboundTrackConfig{}); !errors.Is(err, rtctransport.ErrNilInboundRTPTrack) {
		t.Fatalf("nil inbound source error = %v", err)
	}
	if _, err := service.NewOutboundTrack(rtctransport.OutboundTrackConfig{}); !errors.Is(err, rtctransport.ErrOutboundNilEncoder) {
		t.Fatalf("nil outbound encoder error = %v", err)
	}
}

func TestInboundTerminalErrorDoesNotBlockWhenFrameQueueIsFull(t *testing.T) {
	sourceErr := errors.New("packet source stopped")
	source := &terminalPacketSource{
		packets: []*rtp.Packet{
			testPacket(1, 1000, 1, 111, 1),
			testPacket(2, 1960, 1, 111, 2),
		},
		err: sourceErr,
	}
	track, err := NewService().NewInboundTrack(source, &testDecoder{}, rtctransport.InboundTrackConfig{
		FrameDuration: 20 * time.Millisecond,
		JitterDepth:   20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for index := 0; index < 2; index++ {
		if _, readErr := track.ReadFrame(ctx); readErr != nil {
			t.Fatalf("ReadFrame(%d) error = %v", index, readErr)
		}
	}
	_, terminalErr := track.ReadFrame(ctx)
	if !errors.Is(terminalErr, sourceErr) || !errors.Is(terminalErr, rtctransport.ErrInboundTrackSource) {
		t.Fatalf("terminal error = %v, want source cause and identity", terminalErr)
	}
}

func TestInboundTerminalFailureClosesBlockedPacketSource(t *testing.T) {
	source := newBlockedAfterInvalidSource()
	track, err := NewService().NewInboundTrack(source, &testDecoder{}, rtctransport.InboundTrackConfig{})
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

	select {
	case <-source.blocked:
	case <-time.After(time.Second):
		t.Fatal("packet source did not reach its blocked read")
	}
	select {
	case <-source.closed:
	case <-time.After(time.Second):
		t.Fatal("terminal failure did not close the packet source")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, terminalErr := track.ReadFrame(ctx)
	assertInboundKind(t, terminalErr, rtctransport.ErrInvalidInboundRTPPacket)
}

func TestInboundObsoletePacketStillValidatesTrackIdentity(t *testing.T) {
	firstTimer := make(chan time.Time)
	timerCreated := make(chan struct{})
	secondPacket := make(chan struct{})
	timerCalls := 0
	source := &stagedPacketSource{
		first:         testPacket(10, 1000, 1, 111, 1),
		second:        testPacket(10, 1000, 2, 111, 1),
		releaseSecond: secondPacket,
	}
	track, err := NewService().NewInboundTrack(source, &testDecoder{}, rtctransport.InboundTrackConfig{
		NewTimer: func(time.Duration) <-chan time.Time {
			timerCalls++
			if timerCalls == 1 {
				close(timerCreated)
				return firstTimer
			}
			return make(chan time.Time)
		},
	})
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

	select {
	case <-timerCreated:
	case <-time.After(time.Second):
		t.Fatal("inbound playout timer was not created")
	}
	close(firstTimer)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, readErr := track.ReadFrame(ctx); readErr != nil {
		t.Fatalf("first ReadFrame() error = %v", readErr)
	}
	close(secondPacket)
	_, terminalErr := track.ReadFrame(ctx)
	assertInboundKind(t, terminalErr, rtctransport.ErrInvalidInboundRTPPacket)
}

func TestOutboundConstructionRejectsTypedNilDependencies(t *testing.T) {
	service := NewService()
	validWriter := rtctransport.RTPWriterFunc(func(context.Context, *rtp.Packet) error { return nil })
	validEncoder := rtctransport.OpusEncoderFunc(func(context.Context, []int16) ([]byte, error) { return []byte{1}, nil })
	validPacer := rtctransport.PacerFunc(func(context.Context, uint64) error { return nil })

	var encoder *typedNilEncoder
	if _, err := service.NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000, Encoder: encoder, Writer: validWriter, Pacer: validPacer,
	}); !errors.Is(err, rtctransport.ErrOutboundNilEncoder) {
		t.Fatalf("typed nil encoder error = %v, want encoder identity", err)
	}

	var writer *typedNilWriter
	if _, err := service.NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000, Encoder: validEncoder, Writer: writer, Pacer: validPacer,
	}); !errors.Is(err, rtctransport.ErrOutboundNilWriter) {
		t.Fatalf("typed nil writer error = %v, want writer identity", err)
	}

	var pacer *typedNilPacer
	if _, err := service.NewOutboundTrack(rtctransport.OutboundTrackConfig{
		SourceRate: 48000, Encoder: validEncoder, Writer: validWriter, Pacer: pacer,
	}); !errors.Is(err, rtctransport.ErrOutboundNilPacer) {
		t.Fatalf("typed nil pacer error = %v, want pacer identity", err)
	}
}

func assertInboundKind(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want errors.Is(..., %v)", err, want)
	}
	var typedErr *rtctransport.InboundTrackError
	if !errors.As(err, &typedErr) {
		t.Fatalf("error = %T, want *InboundTrackError", err)
	}
	if !errors.Is(typedErr.Kind, want) {
		t.Fatalf("error kind = %v, want %v", typedErr.Kind, want)
	}
}

type testPacketSource struct {
	packets []*rtp.Packet
	index   int
}

type terminalPacketSource struct {
	packets []*rtp.Packet
	index   int
	err     error
	closed  chan struct{}
	once    sync.Once
}

func (s *terminalPacketSource) ReadRTP() (*rtp.Packet, error) {
	if s.index < len(s.packets) {
		packet := s.packets[s.index]
		s.index++
		return packet, nil
	}
	return nil, s.err
}

func (s *terminalPacketSource) Close() error {
	s.once.Do(func() {
		if s.closed != nil {
			close(s.closed)
		}
	})
	return nil
}

type blockedAfterInvalidSource struct {
	reads     int
	blocked   chan struct{}
	closed    chan struct{}
	blockOnce sync.Once
	closeOnce sync.Once
}

func newBlockedAfterInvalidSource() *blockedAfterInvalidSource {
	return &blockedAfterInvalidSource{
		blocked: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (s *blockedAfterInvalidSource) ReadRTP() (*rtp.Packet, error) {
	s.reads++
	switch s.reads {
	case 1:
		return testPacket(1, 1000, 1, 111, 1), nil
	case 2:
		return testPacket(1, 1000, 2, 111, 1), nil
	default:
		s.blockOnce.Do(func() { close(s.blocked) })
		<-s.closed
		return nil, errors.New("source closed")
	}
}

func (s *blockedAfterInvalidSource) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type stagedPacketSource struct {
	first         *rtp.Packet
	second        *rtp.Packet
	releaseSecond <-chan struct{}
	reads         int
}

func (s *stagedPacketSource) ReadRTP() (*rtp.Packet, error) {
	s.reads++
	switch s.reads {
	case 1:
		return s.first, nil
	case 2:
		<-s.releaseSecond
		return s.second, nil
	default:
		return nil, io.EOF
	}
}

type typedNilEncoder struct{}

func (*typedNilEncoder) Encode(context.Context, []int16) ([]byte, error) { return nil, nil }

type typedNilWriter struct{}

func (*typedNilWriter) WriteRTP(context.Context, *rtp.Packet) error { return nil }

type typedNilPacer struct{}

func (*typedNilPacer) Wait(context.Context, uint64) error { return nil }

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
