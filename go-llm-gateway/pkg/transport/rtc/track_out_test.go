package rtc

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestOutboundTrackResamplesAndPreservesRTPTimeline(t *testing.T) {
	encoder := &captureOutboundEncoder{}
	writer := &captureOutboundWriter{}
	pacer := &captureOutboundPacer{}
	track := newTestOutboundTrack(t, encoder, writer, pacer)

	first := pcmTone(320, 3)
	second := pcmTone(320, 97)
	firstBefore, secondBefore := append([]int16(nil), first...), append([]int16(nil), second...)
	if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: first}); err != nil {
		t.Fatalf("first WriteFrame: %v", err)
	}
	if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: second}); err != nil {
		t.Fatalf("second WriteFrame: %v", err)
	}
	if !reflect.DeepEqual(first, firstBefore) || !reflect.DeepEqual(second, secondBefore) {
		t.Fatal("WriteFrame mutated caller-owned samples")
	}
	first[0], second[0] = -32768, 32767
	if err := track.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	frames := encoder.Frames()
	if len(frames) != 2 || len(frames[0]) != 960 || len(frames[1]) != 960 {
		t.Fatalf("encoded frame shapes = %v, want two 960-sample frames", frameLengths(frames))
	}
	wantFirst, err := wavio.Resample(firstBefore, wavio.Rate16kHz, OutboundRTPClockRate)
	if err != nil {
		t.Fatalf("expected first resample: %v", err)
	}
	wantSecond, err := wavio.Resample(secondBefore, wavio.Rate16kHz, OutboundRTPClockRate)
	if err != nil {
		t.Fatalf("expected second resample: %v", err)
	}
	if !reflect.DeepEqual(frames[0], wantFirst) || !reflect.DeepEqual(frames[1], wantSecond) {
		t.Fatal("encoder did not receive wavio's 48 kHz samples")
	}

	packets := writer.Packets()
	if len(packets) != 2 {
		t.Fatalf("captured packets = %d, want 2", len(packets))
	}
	if got, want := packets[0].SequenceNumber, uint16(41); got != want {
		t.Fatalf("first sequence = %d, want %d", got, want)
	}
	if got, want := packets[1].SequenceNumber, uint16(42); got != want {
		t.Fatalf("second sequence = %d, want %d", got, want)
	}
	if got, want := packets[0].Timestamp, uint32(9000); got != want {
		t.Fatalf("first timestamp = %d, want %d", got, want)
	}
	if got, want := packets[1].Timestamp-packets[0].Timestamp, uint32(960); got != want {
		t.Fatalf("timestamp delta = %d, want %d", got, want)
	}
	if packets[0].PayloadType != 111 || packets[0].SSRC != 77 || !packets[0].Marker || packets[1].Marker {
		t.Fatalf("RTP headers = %#v / %#v, want Opus defaults, first marker only", packets[0].Header, packets[1].Header)
	}
	if got, want := pacer.Offsets(), []uint64{0, 960}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pacer offsets = %v, want %v", got, want)
	}
	for index, packet := range packets {
		if len(packet.Payload) == 0 {
			t.Fatalf("packet %d has an empty encoded payload", index)
		}
	}
	if got, want := packets[0].Payload[1], byte(wantFirst[0]); got != want {
		t.Fatalf("first payload changed after caller buffer reuse: got %d, want %d", got, want)
	}
}

func TestOutboundTrackSuccessfulWriteCommitsAfterCancellation(t *testing.T) {
	writer := &captureOutboundWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	writer.cancelAfterWrite = cancel
	track := newTestOutboundTrack(t, &captureOutboundEncoder{}, writer, &captureOutboundPacer{})

	if err := track.WriteFrame(ctx, sharedaudio.PCMFrame{Samples: pcmTone(320, 1)}); err != nil {
		t.Fatalf("successful canceled WriteFrame: %v", err)
	}
	if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, 2)}); err != nil {
		t.Fatalf("follow-up WriteFrame: %v", err)
	}
	if err := track.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	packets := writer.Packets()
	if len(packets) != 2 {
		t.Fatalf("captured packets = %d, want 2", len(packets))
	}
	if got, want := packets[0].SequenceNumber, uint16(41); got != want {
		t.Fatalf("first sequence = %d, want %d", got, want)
	}
	if got, want := packets[1].SequenceNumber, uint16(42); got != want {
		t.Fatalf("second sequence = %d, want %d", got, want)
	}
	if got, want := packets[1].Timestamp-packets[0].Timestamp, uint32(960); got != want {
		t.Fatalf("timestamp delta = %d, want %d", got, want)
	}
}

func TestOutboundTrackPacingUsesMediaTimelineAcrossIrregularArrival(t *testing.T) {
	clock := &outboundFakeClock{now: time.Unix(0, 0)}
	pacer := &wallClockPacer{now: clock.Now, wait: clock.Wait}
	writer := &captureOutboundWriter{now: clock.Now}
	track := newTestOutboundTrack(t, &captureOutboundEncoder{}, writer, pacer)

	for index := 0; index < 3; index++ {
		if index == 1 {
			// Simulate a late caller without sleeping. The next packet is
			// emitted immediately, but a following packet keeps the media
			// interval instead of bursting alongside it.
			clock.Advance(5 * sampleOffsetDuration(960))
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, index+1)}); err != nil {
			t.Fatalf("WriteFrame %d: %v", index, err)
		}
	}
	if err := track.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got, want := clock.Waits(), []time.Duration{0, 0, sampleOffsetDuration(960)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pacer waits = %v, want %v", got, want)
	}
	emissions := writer.EmissionTimes()
	if len(emissions) != 3 {
		t.Fatalf("emission times = %d, want 3", len(emissions))
	}
	if got, want := emissions[1].Sub(emissions[0]), 5*sampleOffsetDuration(960); got != want {
		t.Fatalf("late-arrival emission gap = %v, want %v", got, want)
	}
	if got, want := emissions[2].Sub(emissions[1]), sampleOffsetDuration(960); got != want {
		t.Fatalf("post-late emission gap = %v, want %v", got, want)
	}
}

func TestOutboundTrackErrorsPreserveIdentity(t *testing.T) {
	codecErr := &outboundTestError{operation: "codec"}
	writerErr := &outboundTestError{operation: "writer"}
	pacerErr := &outboundTestError{operation: "pacer"}

	tests := []struct {
		name  string
		track func() *OutboundTrack
		want  error
	}{
		{
			name: "encoder",
			track: func() *OutboundTrack {
				return newTestOutboundTrack(t, &captureOutboundEncoder{encodeErr: codecErr}, &captureOutboundWriter{}, &captureOutboundPacer{})
			},
			want: codecErr,
		},
		{
			name: "writer",
			track: func() *OutboundTrack {
				return newTestOutboundTrack(t, &captureOutboundEncoder{}, &captureOutboundWriter{writeErr: writerErr}, &captureOutboundPacer{})
			},
			want: writerErr,
		},
		{
			name: "pacer",
			track: func() *OutboundTrack {
				return newTestOutboundTrack(t, &captureOutboundEncoder{}, &captureOutboundWriter{}, &captureOutboundPacer{waitErr: pacerErr})
			},
			want: pacerErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			track := test.track()
			err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, 1)})
			if !errors.Is(err, test.want) {
				t.Fatalf("WriteFrame error = %v, want errors.Is(..., %v)", err, test.want)
			}
			var typed *outboundTestError
			if !errors.As(err, &typed) || typed != test.want {
				t.Fatalf("WriteFrame error = %v, want typed cause %v", err, test.want)
			}
			_ = track.Close()
		})
	}

	t.Run("invalid configuration and frame", func(t *testing.T) {
		_, err := NewOutboundTrack(OutboundTrackConfig{SourceRate: 11025, Encoder: &captureOutboundEncoder{}, Writer: &captureOutboundWriter{}})
		if !errors.Is(err, wavio.ErrUnsupportedResampleRate) {
			t.Fatalf("unsupported source rate error = %v, want wavio identity", err)
		}
		track := newTestOutboundTrack(t, &captureOutboundEncoder{}, &captureOutboundWriter{}, &captureOutboundPacer{})
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{}); !errors.Is(err, ErrOutboundEmptyFrame) {
			t.Fatalf("empty frame error = %v, want %v", err, ErrOutboundEmptyFrame)
		}
		if err := track.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, 1)}); !errors.Is(err, ErrOutboundClosed) {
			t.Fatalf("write after close = %v, want %v", err, ErrOutboundClosed)
		}
	})

	t.Run("empty encoder payload", func(t *testing.T) {
		track := newTestOutboundTrack(t, &captureOutboundEncoder{emptyPayload: true}, &captureOutboundWriter{}, &captureOutboundPacer{})
		err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, 1)})
		if !errors.Is(err, ErrOutboundEmptyPayload) {
			t.Fatalf("empty payload error = %v, want %v", err, ErrOutboundEmptyPayload)
		}
		_ = track.Close()
	})

	t.Run("encoder close", func(t *testing.T) {
		closeErr := &outboundTestError{operation: "close"}
		encoder := &captureOutboundEncoder{closeErr: closeErr}
		track := newTestOutboundTrack(t, encoder, &captureOutboundWriter{}, &captureOutboundPacer{})
		if err := track.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("Close error = %v, want errors.Is(..., %v)", err, closeErr)
		}
		if err := track.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("second Close error = %v, want same cause", err)
		}
	})
}

func TestOutboundTrackConfigurationDefaultsAndAdapters(t *testing.T) {
	validWriter := &captureOutboundWriter{}
	if _, err := NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate16kHz, Writer: validWriter}); !errors.Is(err, ErrOutboundNilEncoder) {
		t.Fatalf("nil encoder error = %v, want %v", err, ErrOutboundNilEncoder)
	}
	validEncoder := &captureOutboundEncoder{}
	if _, err := NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate16kHz, Encoder: validEncoder}); !errors.Is(err, ErrOutboundNilWriter) {
		t.Fatalf("nil writer error = %v, want %v", err, ErrOutboundNilWriter)
	}

	var encodedSamples []int16
	var packet *rtp.Packet
	encoder := OpusEncoderFunc(func(_ context.Context, samples []int16) ([]byte, error) {
		encodedSamples = append([]int16(nil), samples...)
		return []byte{0x01}, nil
	})
	writer := RTPWriterFunc(func(_ context.Context, value *rtp.Packet) error {
		clone := *value
		clone.Payload = append([]byte(nil), value.Payload...)
		packet = &clone
		return nil
	})
	track, err := NewOutboundTrack(OutboundTrackConfig{
		SourceRate: wavio.Rate48kHz,
		Encoder:    encoder,
		Writer:     writer,
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack: %v", err)
	}
	samples := pcmTone(3, 5)
	wantSamples := append([]int16(nil), samples...)
	if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: samples}); err != nil {
		t.Fatalf("identity-rate WriteFrame: %v", err)
	}
	if !reflect.DeepEqual(encodedSamples, wantSamples) {
		t.Fatalf("identity-rate samples = %v, want %v", encodedSamples, wantSamples)
	}
	if packet == nil {
		t.Fatal("RTP writer did not receive a packet")
	}
	if packet.PayloadType != defaultOpusPayloadType || packet.SSRC != defaultOutboundSSRC || packet.SequenceNumber != 0 || packet.Timestamp != 0 || !packet.Marker {
		t.Fatalf("default RTP header = %#v, want default payload, SSRC, sequence, timestamp, and marker", packet.Header)
	}
	if err := track.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cause := errors.New("codec failed")
	wrapped := &OutboundOperationError{Operation: "encode", Err: cause}
	if got, want := wrapped.Error(), "rtc outbound encode: codec failed"; got != want {
		t.Fatalf("OutboundOperationError.Error() = %q, want %q", got, want)
	}
}

func TestOutboundTrackCancellationInterruptsPacingAndWriting(t *testing.T) {
	t.Run("pacer", func(t *testing.T) {
		pacer := &captureOutboundPacer{block: make(chan struct{}), entered: make(chan struct{})}
		track := newTestOutboundTrack(t, &captureOutboundEncoder{}, &captureOutboundWriter{}, pacer)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- track.WriteFrame(ctx, sharedaudio.PCMFrame{Samples: pcmTone(320, 1)}) }()
		waitForSignal(t, pacer.entered)
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("paced WriteFrame error = %v, want context.Canceled", err)
		}
		if err := track.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("writer", func(t *testing.T) {
		writer := &captureOutboundWriter{block: make(chan struct{}), entered: make(chan struct{})}
		track := newTestOutboundTrack(t, &captureOutboundEncoder{}, writer, &captureOutboundPacer{})
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- track.WriteFrame(ctx, sharedaudio.PCMFrame{Samples: pcmTone(320, 1)}) }()
		waitForSignal(t, writer.entered)
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked WriteFrame error = %v, want context.Canceled", err)
		}
		if err := track.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("close cancels writer", func(t *testing.T) {
		writer := &captureOutboundWriter{block: make(chan struct{}), entered: make(chan struct{})}
		encoder := &captureOutboundEncoder{}
		track := newTestOutboundTrack(t, encoder, writer, &captureOutboundPacer{})
		result := make(chan error, 1)
		go func() {
			result <- track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, 1)})
		}()
		waitForSignal(t, writer.entered)
		closeResult := make(chan error, 1)
		go func() { closeResult <- track.Close() }()
		if err := <-result; !errors.Is(err, ErrOutboundClosed) {
			t.Fatalf("close-interrupted WriteFrame error = %v, want %v", err, ErrOutboundClosed)
		}
		if err := <-closeResult; err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := encoder.CloseCount(); got != 1 {
			t.Fatalf("encoder Close count = %d, want 1", got)
		}
	})
}

func TestOutboundTrackSerializesConcurrentWrites(t *testing.T) {
	track := newTestOutboundTrack(t, &captureOutboundEncoder{}, &captureOutboundWriter{}, &captureOutboundPacer{})
	const writes = 8
	results := make(chan error, writes)
	var group sync.WaitGroup
	for index := 0; index < writes; index++ {
		group.Add(1)
		go func(value int) {
			defer group.Done()
			results <- track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, value)})
		}(index)
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent WriteFrame: %v", err)
		}
	}
	if err := track.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOutboundTrackConcurrentWriteCancelClose(t *testing.T) {
	writer := &captureOutboundWriter{block: make(chan struct{}), entered: make(chan struct{})}
	track := newTestOutboundTrack(t, &captureOutboundEncoder{}, writer, &captureOutboundPacer{})
	writeContext, cancelWrite := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	closeResult := make(chan error, 1)

	go func() {
		firstResult <- track.WriteFrame(writeContext, sharedaudio.PCMFrame{Samples: pcmTone(320, 1)})
	}()
	waitForSignal(t, writer.entered)
	go func() {
		secondResult <- track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, 2)})
	}()

	shutdown := make(chan struct{})
	go func() {
		<-shutdown
		cancelWrite()
	}()
	go func() {
		<-shutdown
		closeResult <- track.Close()
	}()
	close(shutdown)

	assertOutboundShutdownError(t, <-firstResult)
	assertOutboundShutdownError(t, <-secondResult)
	if err := <-closeResult; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if packets := writer.Packets(); len(packets) != 0 {
		t.Fatalf("packets emitted during concurrent shutdown = %d, want 0", len(packets))
	}
}

func TestOutboundTrackRejectsUnrepresentableMediaTimeline(t *testing.T) {
	track := newTestOutboundTrack(t, &captureOutboundEncoder{}, &captureOutboundWriter{}, &captureOutboundPacer{})
	track.mediaSamples = ^uint64(0)
	if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: pcmTone(320, 1)}); !errors.Is(err, ErrOutboundFrameTooLarge) {
		t.Fatalf("oversized media timeline error = %v, want %v", err, ErrOutboundFrameTooLarge)
	}
	if err := track.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOutboundTrackWallClockPacerAndMediaOffsets(t *testing.T) {
	const maxDuration = uint64(1<<63 - 1)
	if got, want := sampleOffsetDuration(0), time.Duration(0); got != want {
		t.Fatalf("zero media offset duration = %v, want %v", got, want)
	}
	if got, want := sampleOffsetDuration(uint64(OutboundRTPClockRate)+1), time.Second+time.Duration(time.Second/OutboundRTPClockRate); got != want {
		t.Fatalf("one-second media offset duration = %v, want %v", got, want)
	}
	if got, want := sampleOffsetDuration(^uint64(0)), time.Duration(maxDuration); got != want {
		t.Fatalf("large media offset duration = %v, want %v", got, want)
	}
	boundarySamples := (maxDuration/uint64(time.Second))*uint64(OutboundRTPClockRate) + uint64(OutboundRTPClockRate-1)
	if got, want := sampleOffsetDuration(boundarySamples), time.Duration(maxDuration); got != want {
		t.Fatalf("duration addition overflow = %v, want %v", got, want)
	}

	pacer := newWallClockPacer()
	if err := pacer.Wait(context.Background(), 0); err != nil {
		t.Fatalf("zero-offset Wait: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pacer.Wait(ctx, OutboundRTPClockRate); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Wait error = %v, want context.Canceled", err)
	}
	if got, want := contextCause(context.Background()), context.Canceled; !errors.Is(got, want) {
		t.Fatalf("background context cause = %v, want %v", got, want)
	}
}

func TestOutboundTrackAllocationGateNegativeControl(t *testing.T) {
	if !outboundAllocationsWithinBudget(outboundMeasuredAllocsPerFrameCeiling, outboundMeasuredAllocsPerFrameCeiling) {
		t.Fatal("the committed allocation ceiling rejected its own measured value")
	}
	if outboundAllocationsWithinBudget(outboundMeasuredAllocsPerFrameCeiling+1, outboundMeasuredAllocsPerFrameCeiling) {
		t.Fatal("allocation gate accepted the deterministic over-budget control")
	}
}

func TestOutboundTrackSteadyStateAllocations(t *testing.T) {
	track := newTestOutboundTrack(t, noAllocOutboundEncoder{}, noAllocOutboundWriter{}, PacerFunc(func(context.Context, uint64) error { return nil }))
	frame := sharedaudio.PCMFrame{Samples: pcmTone(320, 1)}
	for warmup := 0; warmup < 10; warmup++ {
		if err := track.WriteFrame(context.Background(), frame); err != nil {
			t.Fatalf("warm-up WriteFrame: %v", err)
		}
	}
	got := testing.AllocsPerRun(100, func() {
		if err := track.WriteFrame(context.Background(), frame); err != nil {
			t.Fatalf("measured WriteFrame: %v", err)
		}
	})
	if !outboundAllocationsWithinBudget(got, outboundMeasuredAllocsPerFrameCeiling) {
		t.Fatalf("steady-state allocations/frame = %.2f, want <= %d (320 source samples at 16 kHz -> 960 at 48 kHz)", got, outboundMeasuredAllocsPerFrameCeiling)
	}
	if err := track.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// outboundMeasuredAllocsPerFrameCeiling is the measured Go 1.24.2 Windows
// ceiling for the steady-state 320-sample PCM16 frame path above. The setup,
// warm-up, and assertions are outside testing.AllocsPerRun.
const outboundMeasuredAllocsPerFrameCeiling = 12

func outboundAllocationsWithinBudget(got, ceiling float64) bool { return got <= ceiling }

func BenchmarkOutboundTrackFrame(b *testing.B) {
	track := newTestOutboundTrack(b, noAllocOutboundEncoder{}, noAllocOutboundWriter{}, PacerFunc(func(context.Context, uint64) error { return nil }))
	frame := sharedaudio.PCMFrame{Samples: pcmTone(320, 1)}
	for warmup := 0; warmup < 10; warmup++ {
		if err := track.WriteFrame(context.Background(), frame); err != nil {
			b.Fatalf("warm-up WriteFrame: %v", err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if err := track.WriteFrame(context.Background(), frame); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := track.Close(); err != nil {
		b.Fatalf("Close: %v", err)
	}
}

func newTestOutboundTrack(t testing.TB, encoder OpusEncoder, writer RTPWriter, pacer Pacer) *OutboundTrack {
	t.Helper()
	track, err := NewOutboundTrack(OutboundTrackConfig{
		SourceRate:            wavio.Rate16kHz,
		Encoder:               encoder,
		Writer:                writer,
		Pacer:                 pacer,
		SSRC:                  77,
		InitialSequenceNumber: 41,
		InitialTimestamp:      9000,
	})
	if err != nil {
		t.Fatalf("NewOutboundTrack: %v", err)
	}
	return track
}

func pcmTone(length, offset int) []int16 {
	samples := make([]int16, length)
	for index := range samples {
		samples[index] = int16(((index+offset)*97)%12000 - 6000)
	}
	return samples
}

func frameLengths(frames [][]int16) []int {
	lengths := make([]int, len(frames))
	for index, frame := range frames {
		lengths[index] = len(frame)
	}
	return lengths
}

func waitForSignal(t testing.TB, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for operation to block")
	}
}

type outboundTestError struct{ operation string }

func (e *outboundTestError) Error() string { return e.operation + " failed" }

type captureOutboundEncoder struct {
	mu           sync.Mutex
	frames       [][]int16
	encodeErr    error
	emptyPayload bool
	closeErr     error
	closeCount   int
}

func (e *captureOutboundEncoder) Encode(ctx context.Context, samples []int16) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, contextCause(ctx)
	default:
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.encodeErr != nil {
		return nil, e.encodeErr
	}
	e.frames = append(e.frames, append([]int16(nil), samples...))
	if e.emptyPayload {
		return nil, nil
	}
	return []byte{byte(len(samples)), byte(samples[0]), byte(samples[len(samples)-1])}, nil
}

func (e *captureOutboundEncoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeCount++
	return e.closeErr
}

func (e *captureOutboundEncoder) Frames() [][]int16 {
	e.mu.Lock()
	defer e.mu.Unlock()
	frames := make([][]int16, len(e.frames))
	for index, frame := range e.frames {
		frames[index] = append([]int16(nil), frame...)
	}
	return frames
}

func (e *captureOutboundEncoder) CloseCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closeCount
}

type noAllocOutboundEncoder struct{}

func (noAllocOutboundEncoder) Encode(context.Context, []int16) ([]byte, error) {
	return []byte{0xF8, 0xFF}, nil
}

type captureOutboundWriter struct {
	mu                   sync.Mutex
	packets              []*rtp.Packet
	emissionTimes        []time.Time
	now                  func() time.Time
	writeErr             error
	block                <-chan struct{}
	entered              chan struct{}
	once                 sync.Once
	cancelAfterWrite     context.CancelFunc
	cancelAfterWriteOnce sync.Once
}

func (w *captureOutboundWriter) WriteRTP(ctx context.Context, packet *rtp.Packet) error {
	if w.entered != nil {
		w.once.Do(func() { close(w.entered) })
	}
	if w.block != nil {
		select {
		case <-w.block:
		case <-ctx.Done():
			return contextCause(ctx)
		}
	}
	if err := contextCauseIfDone(ctx); err != nil {
		return err
	}
	if w.writeErr != nil {
		return w.writeErr
	}
	clone := *packet
	clone.Payload = append([]byte(nil), packet.Payload...)
	w.mu.Lock()
	w.packets = append(w.packets, &clone)
	if w.now != nil {
		w.emissionTimes = append(w.emissionTimes, w.now())
	}
	w.mu.Unlock()
	if w.cancelAfterWrite != nil {
		w.cancelAfterWriteOnce.Do(w.cancelAfterWrite)
	}
	return nil
}

func (w *captureOutboundWriter) Packets() []*rtp.Packet {
	w.mu.Lock()
	defer w.mu.Unlock()
	packets := make([]*rtp.Packet, len(w.packets))
	for index, packet := range w.packets {
		clone := *packet
		clone.Payload = append([]byte(nil), packet.Payload...)
		packets[index] = &clone
	}
	return packets
}

func (w *captureOutboundWriter) EmissionTimes() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.emissionTimes...)
}

type noAllocOutboundWriter struct{}

func (noAllocOutboundWriter) WriteRTP(context.Context, *rtp.Packet) error { return nil }

type captureOutboundPacer struct {
	mu      sync.Mutex
	offsets []uint64
	waitErr error
	block   <-chan struct{}
	entered chan struct{}
	once    sync.Once
}

func assertOutboundShutdownError(t testing.TB, err error) {
	t.Helper()
	if err == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, ErrOutboundClosed)) {
		t.Fatalf("shutdown WriteFrame error = %v, want context.Canceled or %v", err, ErrOutboundClosed)
	}
}

func (p *captureOutboundPacer) Wait(ctx context.Context, offset uint64) error {
	if p.entered != nil {
		p.once.Do(func() { close(p.entered) })
	}
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return contextCause(ctx)
		}
	}
	if err := contextCauseIfDone(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	p.offsets = append(p.offsets, offset)
	p.mu.Unlock()
	return p.waitErr
}

func (p *captureOutboundPacer) Offsets() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.offsets...)
}

type outboundFakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *outboundFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *outboundFakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func (c *outboundFakeClock) Wait(ctx context.Context, duration time.Duration) error {
	if err := contextCauseIfDone(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.waits = append(c.waits, duration)
	c.now = c.now.Add(duration)
	c.mu.Unlock()
	return nil
}

func (c *outboundFakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}
