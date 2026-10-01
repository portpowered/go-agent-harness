package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// fakeClock records each pacer wait and advances its own time by it, so the
// pacer's schedule is observable without real sleeping.
type fakeClock struct {
	now   time.Time
	waits []time.Duration
	err   error
}

func (c *fakeClock) pacer() *wallClockPacer {
	return &wallClockPacer{now: func() time.Time { return c.now }, wait: c.wait}
}

func (c *fakeClock) wait(_ context.Context, d time.Duration) error {
	if c.err != nil {
		return c.err
	}
	c.waits = append(c.waits, d)
	if d > 0 {
		c.now = c.now.Add(d)
	}
	return nil
}

const twentyMS = rtctransport.OutboundRTPClockRate / 50 // samples per 20 ms

func TestWallClockPacerSchedulesFromFirstPacket(t *testing.T) {
	clock := &fakeClock{now: time.Unix(10, 0)}
	pacer := clock.pacer()
	for frame := range uint64(4) {
		if err := pacer.Wait(t.Context(), frame*twentyMS); err != nil {
			t.Fatal(err)
		}
	}
	want := []time.Duration{0, 20 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond}
	for i := range want {
		if clock.waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", clock.waits, want)
		}
	}
}

// TestWallClockPacerRebasesAfterStallInsteadOfBursting proves that a writer
// that falls behind is not allowed to send its backlog at line rate: the
// schedule restarts from now.
func TestWallClockPacerRebasesAfterStallInsteadOfBursting(t *testing.T) {
	clock := &fakeClock{now: time.Unix(10, 0)}
	pacer := clock.pacer()
	if err := pacer.Wait(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Second) // the source stalled for 1 s
	if err := pacer.Wait(t.Context(), twentyMS); err != nil {
		t.Fatal(err)
	}
	if err := pacer.Wait(t.Context(), 2*twentyMS); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{0, 0, 20 * time.Millisecond}
	for i := range want {
		if clock.waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", clock.waits, want)
		}
	}
}

func TestWallClockPacerCancellation(t *testing.T) {
	cause := errors.New("track closed")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	clock := &fakeClock{now: time.Unix(10, 0)}
	if err := clock.pacer().Wait(ctx, 0); !errors.Is(err, cause) {
		t.Fatalf("Wait(canceled) = %v, want cause", err)
	}
	waitErr := errors.New("wait failed")
	clock.err = waitErr
	pacer := clock.pacer()
	if err := pacer.Wait(t.Context(), 0); !errors.Is(err, waitErr) {
		t.Fatalf("Wait = %v, want wait failure", err)
	}
	if pacer.started {
		t.Fatal("a failed wait must not start the schedule")
	}
}

func TestWaitWallClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if err := waitWallClock(t.Context(), 0); err != nil {
			t.Fatal(err)
		}
		if err := waitWallClock(t.Context(), 20*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 20*time.Millisecond {
			t.Fatalf("waited %v, want 20ms", elapsed)
		}
		cause := errors.New("closed")
		ctx, cancel := context.WithCancelCause(t.Context())
		result := make(chan error, 1)
		go func() { result <- waitWallClock(ctx, time.Hour) }()
		synctest.Wait()
		cancel(cause)
		if err := <-result; !errors.Is(err, cause) {
			t.Fatalf("canceled wait = %v", err)
		}
		if err := waitWallClock(ctx, time.Hour); !errors.Is(err, cause) {
			t.Fatalf("wait on done ctx = %v", err)
		}
	})
}

func TestSampleOffsetDuration(t *testing.T) {
	if got := sampleOffsetDuration(rtctransport.OutboundRTPClockRate + twentyMS); got != time.Second+20*time.Millisecond {
		t.Fatalf("duration = %v", got)
	}
	if got := sampleOffsetDuration(^uint64(0)); got != time.Duration(1<<63-1) {
		t.Fatalf("overflow duration = %v, want saturation", got)
	}
}

type fakeEncoder struct {
	err      error
	empty    bool
	closeErr error
	closed   int
}

func (e *fakeEncoder) Encode(_ context.Context, samples []int16) ([]byte, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.empty {
		return nil, nil
	}
	return []byte{byte(len(samples))}, nil
}

func (e *fakeEncoder) Close() error {
	e.closed++
	return e.closeErr
}

type fakeWriter struct {
	mu      sync.Mutex
	packets []*rtp.Packet
	err     error
	block   chan struct{}
	entered chan struct{}
}

func (w *fakeWriter) WriteRTP(ctx context.Context, packet *rtp.Packet) error {
	if w.block != nil {
		close(w.entered)
		select {
		case <-w.block:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.packets = append(w.packets, packet)
	return nil
}

type noWaitPacer struct{ offsets []uint64 }

func (p *noWaitPacer) Wait(_ context.Context, offset uint64) error {
	p.offsets = append(p.offsets, offset)
	return nil
}

func newTestTrack(t *testing.T, config rtctransport.OutboundTrackConfig) *OutboundTrack {
	t.Helper()
	if config.SourceRate == 0 {
		config.SourceRate = rtctransport.OutboundRTPClockRate
	}
	if config.Encoder == nil {
		config.Encoder = &fakeEncoder{}
	}
	if config.Writer == nil {
		config.Writer = &fakeWriter{}
	}
	if config.Pacer == nil {
		config.Pacer = &noWaitPacer{}
	}
	track, err := New().NewOutboundTrack(config)
	if err != nil {
		t.Fatalf("NewOutboundTrack = %v", err)
	}
	outbound, ok := track.(*OutboundTrack)
	if !ok {
		t.Fatalf("track type %T", track)
	}
	return outbound
}

func frame20ms() sharedaudio.PCMFrame {
	return sharedaudio.PCMFrame{Samples: make([]int16, twentyMS)}
}

func TestOutboundTrackPacketizesSequentially(t *testing.T) {
	writer := &fakeWriter{}
	pacer := &noWaitPacer{}
	track := newTestTrack(t, rtctransport.OutboundTrackConfig{Writer: writer, Pacer: pacer, InitialSequenceNumber: 65535, InitialTimestamp: 7})
	for range 3 {
		if err := track.WriteFrame(t.Context(), frame20ms()); err != nil {
			t.Fatal(err)
		}
	}
	if len(writer.packets) != 3 {
		t.Fatalf("wrote %d packets", len(writer.packets))
	}
	first := writer.packets[0].Header
	if !first.Marker || first.PayloadType != defaultOpusPayloadType || first.SSRC != defaultOutboundSSRC || first.SequenceNumber != 65535 || first.Timestamp != 7 {
		t.Fatalf("first header = %+v", first)
	}
	second := writer.packets[1].Header
	if second.Marker || second.SequenceNumber != 0 || second.Timestamp != 7+twentyMS {
		t.Fatalf("second header = %+v (sequence must wrap)", second)
	}
	if pacer.offsets[2] != 2*twentyMS {
		t.Fatalf("pacer offsets = %v", pacer.offsets)
	}
	if err := track.Close(); err != nil {
		t.Fatal(err)
	}
	if err := track.WriteFrame(t.Context(), frame20ms()); !errors.Is(err, rtctransport.ErrOutboundClosed) {
		t.Fatalf("WriteFrame after Close = %v", err)
	}
}

func TestOutboundTrackOperationFailures(t *testing.T) {
	encodeErr, writeErr := errors.New("encode"), errors.New("write")
	cases := []struct {
		name   string
		config rtctransport.OutboundTrackConfig
		frame  sharedaudio.PCMFrame
		want   error
		op     string
	}{
		{name: "empty frame", frame: sharedaudio.PCMFrame{}, want: rtctransport.ErrOutboundEmptyFrame},
		{name: "wrong size", frame: sharedaudio.PCMFrame{Samples: []int16{1}}, want: rtctransport.ErrOutboundFrameSize, op: "frame"},
		{name: "encoder error", config: rtctransport.OutboundTrackConfig{Encoder: &fakeEncoder{err: encodeErr}}, frame: frame20ms(), want: encodeErr, op: "encode"},
		{name: "empty payload", config: rtctransport.OutboundTrackConfig{Encoder: &fakeEncoder{empty: true}}, frame: frame20ms(), want: rtctransport.ErrOutboundEmptyPayload, op: "encode"},
		{name: "writer error", config: rtctransport.OutboundTrackConfig{Writer: &fakeWriter{err: writeErr}}, frame: frame20ms(), want: writeErr, op: "write RTP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			track := newTestTrack(t, tc.config)
			err := track.WriteFrame(t.Context(), tc.frame)
			if !errors.Is(err, tc.want) {
				t.Fatalf("WriteFrame = %v, want %v", err, tc.want)
			}
			var opErr *rtctransport.OutboundOperationError
			if tc.op != "" && (!errors.As(err, &opErr) || opErr.Operation != tc.op) {
				t.Fatalf("WriteFrame = %v, want operation %q", err, tc.op)
			}
			// A failed frame must not advance the RTP clock.
			if track.sequence != 0 || track.mediaSamples != 0 {
				t.Fatalf("failed write advanced sequence=%d samples=%d", track.sequence, track.mediaSamples)
			}
		})
	}
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := newTestTrack(t, rtctransport.OutboundTrackConfig{}).WriteFrame(ctx, frame20ms()); !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteFrame(canceled) = %v", err)
		}
	})
	t.Run("pacer failure", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(1, 0), err: errors.New("pace")}
		track := newTestTrack(t, rtctransport.OutboundTrackConfig{Pacer: clock.pacer()})
		var opErr *rtctransport.OutboundOperationError
		if err := track.WriteFrame(t.Context(), frame20ms()); !errors.As(err, &opErr) || opErr.Operation != "pace" {
			t.Fatalf("WriteFrame = %v, want pace failure", err)
		}
	})
}

// TestOutboundTrackCloseCancelsActiveWriteAndRejectsOverflow covers the
// lifecycle: writes beyond the bounded queue fail fast, Close cancels the
// in-flight write with ErrOutboundClosed and waits for it before closing the
// encoder.
func TestOutboundTrackCloseCancelsActiveWriteAndRejectsOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		encoder := &fakeEncoder{closeErr: errors.New("encoder close")}
		writer := &fakeWriter{block: make(chan struct{}), entered: make(chan struct{})}
		track := newTestTrack(t, rtctransport.OutboundTrackConfig{Encoder: encoder, Writer: writer, QueueDepth: 2})
		results := make(chan error, 2)
		go func() { results <- track.WriteFrame(t.Context(), frame20ms()) }()
		<-writer.entered
		go func() { results <- track.WriteFrame(t.Context(), frame20ms()) }() // queued behind the gate
		synctest.Wait()
		if err := track.WriteFrame(t.Context(), frame20ms()); !errors.Is(err, rtctransport.ErrOutboundQueueOverflow) {
			t.Fatalf("overflow write = %v", err)
		}
		closeErr := track.Close()
		var opErr *rtctransport.OutboundOperationError
		if !errors.As(closeErr, &opErr) || opErr.Operation != "close encoder" {
			t.Fatalf("Close = %v, want encoder close failure", closeErr)
		}
		for range 2 {
			if err := <-results; !errors.Is(err, rtctransport.ErrOutboundClosed) {
				t.Fatalf("active write after Close = %v", err)
			}
		}
		if encoder.closed != 1 {
			t.Fatalf("encoder closed %d times", encoder.closed)
		}
		if err := track.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("second Close = %v, want first result", err)
		}
	})
}

func TestNewOutboundTrackValidation(t *testing.T) {
	var nilPacer *noWaitPacer
	cases := []struct {
		name   string
		config rtctransport.OutboundTrackConfig
		want   error
	}{
		{"nil encoder", rtctransport.OutboundTrackConfig{Writer: &fakeWriter{}}, rtctransport.ErrOutboundNilEncoder},
		{"nil writer", rtctransport.OutboundTrackConfig{Encoder: &fakeEncoder{}}, rtctransport.ErrOutboundNilWriter},
		{"bad rate", rtctransport.OutboundTrackConfig{Encoder: &fakeEncoder{}, Writer: &fakeWriter{}, SourceRate: -1}, rtctransport.ErrInvalidOutboundTrackConfig},
		{"bad duration", rtctransport.OutboundTrackConfig{Encoder: &fakeEncoder{}, Writer: &fakeWriter{}, SourceRate: 48000, FrameDuration: 3 * time.Millisecond}, rtctransport.ErrInvalidOutboundTrackConfig},
		{"bad depth", rtctransport.OutboundTrackConfig{Encoder: &fakeEncoder{}, Writer: &fakeWriter{}, SourceRate: 48000, QueueDepth: maxOutboundQueueDepth + 1}, rtctransport.ErrInvalidOutboundTrackConfig},
		{"typed nil pacer", rtctransport.OutboundTrackConfig{Encoder: &fakeEncoder{}, Writer: &fakeWriter{}, SourceRate: 48000, Pacer: nilPacer}, rtctransport.ErrOutboundNilPacer},
	}
	for _, tc := range cases {
		if _, err := New().NewOutboundTrack(tc.config); !errors.Is(err, tc.want) {
			t.Errorf("%s: NewOutboundTrack = %v, want %v", tc.name, err, tc.want)
		}
	}
	track, err := New().NewOutboundTrack(rtctransport.OutboundTrackConfig{Encoder: &fakeEncoder{}, Writer: &fakeWriter{}, SourceRate: 48000})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := track.(*OutboundTrack).pacer.(*wallClockPacer); !ok {
		t.Fatal("default pacer is not the wall-clock pacer")
	}
}
