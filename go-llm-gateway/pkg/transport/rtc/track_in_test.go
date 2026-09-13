package rtc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const inboundTrackRaceTimeout = 2 * time.Second

// TestInboundTrackS8ConcurrentIngestReadCancelClose keeps the legacy race-gate
// name while exercising the remaining Pion-facing inbound adapter. RTP policy
// and transport lifecycle coverage lives in the runtime transport package.
func TestInboundTrackS8ConcurrentIngestReadCancelClose(t *testing.T) {
	t.Parallel()

	inbound := newPionInbound(nil, "race")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	produced := make(chan struct{})
	var producedOnce sync.Once
	failures := make(chan error, 3)
	var workers sync.WaitGroup
	workers.Add(3)

	go runInboundRaceProducer(inbound, started, produced, &producedOnce, &workers)
	go runInboundRaceReader(inbound, ctx, started, failures, &workers)
	go runInboundRaceCloser(inbound, started, produced, failures, cancel, &workers)

	close(started)
	waitForInboundRaceWorkers(t, &workers)
	assertInboundRaceFailures(t, failures)
	drainClosedInbound(t, inbound)
}

func runInboundRaceProducer(inbound *pionInbound, started, produced chan struct{}, producedOnce *sync.Once, workers *sync.WaitGroup) {
	defer workers.Done()
	defer producedOnce.Do(func() { close(produced) })
	<-started
	for index := 0; index < 24; index++ {
		frame := sharedaudio.PCMFrame{Samples: []int16{int16(index)}}
		select {
		case inbound.frames <- frame:
			if index == 8 {
				producedOnce.Do(func() { close(produced) })
			}
		case <-inbound.done:
			return
		}
	}
}

func runInboundRaceReader(inbound *pionInbound, ctx context.Context, started chan struct{}, failures chan<- error, workers *sync.WaitGroup) {
	defer workers.Done()
	<-started
	for {
		frame, err := inbound.ReadFrame(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				failures <- fmt.Errorf("read frame after lifecycle stop: %w", err)
			}
			return
		}
		if len(frame.Samples) != 1 {
			failures <- fmt.Errorf("read frame has %d samples, want 1", len(frame.Samples))
			return
		}
	}
}

func runInboundRaceCloser(inbound *pionInbound, started, produced chan struct{}, failures chan<- error, cancel context.CancelFunc, workers *sync.WaitGroup) {
	defer workers.Done()
	<-started
	timer := time.NewTimer(inboundTrackRaceTimeout)
	defer timer.Stop()
	select {
	case <-produced:
	case <-timer.C:
		failures <- errors.New("inbound producer did not reach the close point")
	}
	cancel()
	if err := inbound.Close(); err != nil {
		failures <- fmt.Errorf("close inbound adapter: %w", err)
	}
}

func waitForInboundRaceWorkers(t *testing.T, workers *sync.WaitGroup) {
	t.Helper()
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
	case <-time.After(inboundTrackRaceTimeout):
		t.Fatal("inbound adapter workers did not stop")
	}
}

func assertInboundRaceFailures(t *testing.T, failures <-chan error) {
	t.Helper()
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
}

func drainClosedInbound(t *testing.T, inbound *pionInbound) {
	t.Helper()
	for {
		select {
		case <-inbound.frames:
		default:
			frame, err := inbound.ReadFrame(context.Background())
			if !errors.Is(err, io.EOF) {
				t.Fatalf("read after draining closed adapter: frame=%#v err=%v", frame, err)
			}
			return
		}
	}
}

type compatibilityInboundRead struct {
	packet *rtp.Packet
	err    error
}

type compatibilityInboundSource struct {
	reads    []compatibilityInboundRead
	index    int
	onRead   func()
	closeErr error
}

func (s *compatibilityInboundSource) ReadRTP() (*rtp.Packet, error) {
	if s.onRead != nil {
		s.onRead()
	}
	if s.index >= len(s.reads) {
		return nil, io.EOF
	}
	read := s.reads[s.index]
	s.index++
	return read.packet, read.err
}

func (s *compatibilityInboundSource) Close() error { return s.closeErr }

type compatibilityInboundDecoder struct {
	samples   []int16
	decodeErr error
}

func (d *compatibilityInboundDecoder) Decode([]byte) ([]int16, error) {
	if d.decodeErr != nil {
		return nil, d.decodeErr
	}
	return append([]int16(nil), d.samples...), nil
}

func (d *compatibilityInboundDecoder) DecodePLC() ([]int16, error) { return nil, nil }

func compatibilityInboundPacket(sequence uint16, timestamp uint32, ssrc uint32, payloadType uint8) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{
		Version:        2,
		SequenceNumber: sequence,
		Timestamp:      timestamp,
		SSRC:           ssrc,
		PayloadType:    payloadType,
	}, Payload: []byte{0x01}}
}

func newCompatibilityInboundTrack(t testing.TB, source *compatibilityInboundSource, decoder OpusDecoder, config InboundTrackConfig) *InboundTrack {
	t.Helper()
	track, err := NewInboundTrack(source, decoder, config)
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	return track
}

func TestInboundTrackCompatibilityAdapter(t *testing.T) {
	t.Run("defaults and resampled ownership", func(t *testing.T) {
		defaults := DefaultInboundTrackConfig()
		if defaults.SampleRate != DefaultInboundLoopSampleRate || defaults.FrameDuration != DefaultInboundFrameDuration || defaults.JitterDepth != DefaultInboundJitterDepth {
			t.Fatalf("default config = %+v, want rate=%d frame=%v depth=%v", defaults, DefaultInboundLoopSampleRate, DefaultInboundFrameDuration, DefaultInboundJitterDepth)
		}

		decoded := make([]int16, 960)
		decoded[0] = 7
		decoder := &compatibilityInboundDecoder{samples: decoded}
		resampled := make([]int16, 480)
		resampled[0] = 11
		var resampleInput []int16
		source := &compatibilityInboundSource{reads: []compatibilityInboundRead{{
			packet: compatibilityInboundPacket(10, 100, 20, 111),
		}}}
		track := newCompatibilityInboundTrack(t, source, decoder, InboundTrackConfig{
			SampleRate:    wavio.Rate24kHz,
			FrameDuration: 20 * time.Millisecond,
			JitterDepth:   60 * time.Millisecond,
			Resample: func(samples []int16, from, to int) ([]int16, error) {
				if len(samples) != 960 || from != wavio.Rate48kHz || to != wavio.Rate24kHz {
					t.Fatalf("resample arguments = len %d, %d -> %d", len(samples), from, to)
				}
				resampleInput = samples
				return resampled, nil
			},
		})
		frame, err := track.ReadFrame(nil)
		if err != nil {
			t.Fatalf("ReadFrame(nil) error = %v", err)
		}
		if len(frame.Samples) != 480 || frame.Samples[0] != 11 {
			t.Fatalf("resampled frame = len %d first %d, want 480/11", len(frame.Samples), frame.Samples[0])
		}
		frame.Samples[0] = 99
		if resampled[0] != 11 || resampleInput[0] != 7 {
			t.Fatalf("ReadFrame exposed caller-owned resample storage: result=%d input=%d", resampled[0], resampleInput[0])
		}
		if err := track.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("identity rate and close identity", func(t *testing.T) {
		closeErr := errors.New("source close")
		source := &compatibilityInboundSource{closeErr: closeErr, reads: []compatibilityInboundRead{{
			packet: compatibilityInboundPacket(10, 100, 20, 111),
		}}}
		track := newCompatibilityInboundTrack(t, source, &compatibilityInboundDecoder{samples: make([]int16, 960)}, InboundTrackConfig{})
		frame, err := track.ReadFrame(context.Background())
		if err != nil || len(frame.Samples) != 960 {
			t.Fatalf("identity-rate ReadFrame() = len %d, %v; want 960/nil", len(frame.Samples), err)
		}
		if err := track.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("first Close() error = %v, want %v", err, closeErr)
		}
		if err := track.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("second Close() error = %v, want same identity", err)
		}
		if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrInboundTrackClosed) {
			t.Fatalf("ReadFrame after Close() = %v, want %v", err, ErrInboundTrackClosed)
		}
	})
}

func TestInboundTrackCompatibilityConstructionErrors(t *testing.T) {
	validDecoder := &compatibilityInboundDecoder{samples: make([]int16, 960)}
	validSource := &compatibilityInboundSource{}
	var nilSource *compatibilityInboundSource
	var nilDecoder *compatibilityInboundDecoder
	tests := []struct {
		name   string
		source any
		opus   any
		config InboundTrackConfig
		want   error
	}{
		{name: "sample rate", source: validSource, opus: validDecoder, config: InboundTrackConfig{SampleRate: 11025}, want: ErrInvalidInboundTrackConfig},
		{name: "frame duration", source: validSource, opus: validDecoder, config: InboundTrackConfig{FrameDuration: 7 * time.Millisecond}, want: ErrInvalidInboundTrackConfig},
		{name: "jitter depth", source: validSource, opus: validDecoder, config: InboundTrackConfig{JitterDepth: 30 * time.Millisecond, FrameDuration: 20 * time.Millisecond}, want: ErrInvalidInboundTrackConfig},
		{name: "nil source", source: nilSource, opus: validDecoder, want: ErrNilInboundRTPTrack},
		{name: "wrong source", source: struct{}{}, opus: validDecoder, want: ErrInvalidInboundTrackConfig},
		{name: "nil decoder", source: validSource, opus: nilDecoder, want: ErrNilOpusDecoder},
		{name: "wrong decoder", source: validSource, opus: struct{}{}, want: ErrUnsupportedOpusDecoder},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewInboundTrack(test.source, test.opus, test.config)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewInboundTrack() error = %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}

	_, err := NewInboundTrack(validSource, validDecoder, InboundTrackConfig{SampleRate: 11025})
	var typed *InboundTrackError
	if !errors.As(err, &typed) || typed.Error() == "" || typed.Unwrap() == nil || !typed.Is(ErrInvalidInboundTrackConfig) || typed.Is(errors.New("other")) {
		t.Fatalf("invalid configuration error methods not preserved: %v", err)
	}
}

func TestInboundTrackCompatibilityReadErrors(t *testing.T) {
	validPacket := compatibilityInboundPacket(10, 100, 20, 111)
	newTrack := func(source *compatibilityInboundSource, decoder *compatibilityInboundDecoder, config InboundTrackConfig) *InboundTrack {
		return newCompatibilityInboundTrack(t, source, decoder, config)
	}

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		track := newTrack(&compatibilityInboundSource{reads: []compatibilityInboundRead{{packet: validPacket}}}, &compatibilityInboundDecoder{samples: make([]int16, 960)}, InboundTrackConfig{})
		if _, err := track.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadFrame(canceled) = %v, want context.Canceled", err)
		}
	})

	t.Run("source errors and cancellation during read", func(t *testing.T) {
		readErr := errors.New("read failed")
		track := newTrack(&compatibilityInboundSource{reads: []compatibilityInboundRead{{err: readErr}}}, &compatibilityInboundDecoder{samples: make([]int16, 960)}, InboundTrackConfig{})
		if _, err := track.ReadFrame(context.Background()); !errors.Is(err, readErr) || !errors.Is(err, ErrInboundTrackSource) {
			t.Fatalf("source error = %v, want source and cause identities", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		track = newTrack(&compatibilityInboundSource{
			onRead: func() { cancel() },
			reads:  []compatibilityInboundRead{{err: readErr}},
		}, &compatibilityInboundDecoder{samples: make([]int16, 960)}, InboundTrackConfig{})
		if _, err := track.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("read after context cancellation = %v, want context.Canceled", err)
		}
	})

	t.Run("packet validation", func(t *testing.T) {
		cases := []struct {
			name string
			read *rtp.Packet
			want error
		}{
			{name: "nil packet", want: ErrInvalidInboundRTPPacket},
			{name: "wrong version", read: &rtp.Packet{Header: rtp.Header{Version: 1}}, want: ErrInvalidInboundRTPPacket},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				track := newTrack(&compatibilityInboundSource{reads: []compatibilityInboundRead{{packet: test.read}}}, &compatibilityInboundDecoder{samples: make([]int16, 960)}, InboundTrackConfig{})
				_, err := track.ReadFrame(context.Background())
				if !errors.Is(err, test.want) {
					t.Fatalf("packet error = %v, want %v", err, test.want)
				}
			})
		}

		decoder := &compatibilityInboundDecoder{samples: make([]int16, 960)}
		source := &compatibilityInboundSource{reads: []compatibilityInboundRead{{packet: validPacket}, {
			packet: compatibilityInboundPacket(11, 1060, 21, 111),
		}}}
		track := newTrack(source, decoder, InboundTrackConfig{})
		if _, err := track.ReadFrame(context.Background()); err != nil {
			t.Fatalf("first packet = %v", err)
		}
		if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrInvalidInboundRTPPacket) {
			t.Fatalf("identity change = %v, want packet identity", err)
		}

		source = &compatibilityInboundSource{reads: []compatibilityInboundRead{{packet: validPacket}, {
			packet: compatibilityInboundPacket(12, 1060, 20, 111),
		}}}
		track = newTrack(source, decoder, InboundTrackConfig{})
		if _, err := track.ReadFrame(context.Background()); err != nil {
			t.Fatalf("first packet for progress = %v", err)
		}
		if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrImpossibleRTPProgress) {
			t.Fatalf("progress change = %v, want impossible progress", err)
		}
	})

	t.Run("decoder and resampler errors", func(t *testing.T) {
		decodeErr := errors.New("decode failed")
		track := newTrack(&compatibilityInboundSource{reads: []compatibilityInboundRead{{packet: validPacket}}}, &compatibilityInboundDecoder{decodeErr: decodeErr}, InboundTrackConfig{})
		if _, err := track.ReadFrame(context.Background()); !errors.Is(err, decodeErr) || !errors.Is(err, ErrInboundTrackDecode) {
			t.Fatalf("decode error = %v, want decode and cause identities", err)
		}

		track = newTrack(&compatibilityInboundSource{reads: []compatibilityInboundRead{{packet: validPacket}}}, &compatibilityInboundDecoder{samples: make([]int16, 1)}, InboundTrackConfig{})
		if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrInboundTrackFrame) {
			t.Fatalf("decoder frame size error = %v, want frame identity", err)
		}

		resampleErr := errors.New("resample failed")
		track = newTrack(&compatibilityInboundSource{reads: []compatibilityInboundRead{{packet: validPacket}}}, &compatibilityInboundDecoder{samples: make([]int16, 960)}, InboundTrackConfig{
			SampleRate: wavio.Rate24kHz,
			Resample:   func([]int16, int, int) ([]int16, error) { return nil, resampleErr },
		})
		if _, err := track.ReadFrame(context.Background()); !errors.Is(err, resampleErr) || !errors.Is(err, ErrInboundTrackResample) {
			t.Fatalf("resample error = %v, want resample and cause identities", err)
		}

		track = newTrack(&compatibilityInboundSource{reads: []compatibilityInboundRead{{packet: validPacket}}}, &compatibilityInboundDecoder{samples: make([]int16, 960)}, InboundTrackConfig{
			SampleRate: wavio.Rate24kHz,
			Resample:   func([]int16, int, int) ([]int16, error) { return make([]int16, 1), nil },
		})
		if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrInboundTrackFrame) {
			t.Fatalf("resampled frame size error = %v, want frame identity", err)
		}
	})
}
