package rtc

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const outboundTrackRaceTimeout = 2 * time.Second

type pionTrackRacePacket struct {
	sequence uint16
	payload  []byte
}

type pionTrackRaceWriter struct {
	mu          sync.Mutex
	packets     []pionTrackRacePacket
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func (w *pionTrackRaceWriter) WriteRTP(header *rtp.Header, payload []byte) (int, error) {
	if w.entered != nil {
		w.enteredOnce.Do(func() { close(w.entered) })
		<-w.release
	}
	w.mu.Lock()
	w.packets = append(w.packets, pionTrackRacePacket{
		sequence: header.SequenceNumber,
		payload:  append([]byte(nil), payload...),
	})
	w.mu.Unlock()
	return len(payload), nil
}

func (w *pionTrackRaceWriter) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func (w *pionTrackRaceWriter) snapshot() []pionTrackRacePacket {
	w.mu.Lock()
	defer w.mu.Unlock()
	packets := make([]pionTrackRacePacket, len(w.packets))
	copy(packets, w.packets)
	return packets
}

type pionTrackRaceContext struct {
	writer *pionTrackRaceWriter
}

func (c *pionTrackRaceContext) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  1,
		},
		PayloadType: 111,
	}}
}

func (c *pionTrackRaceContext) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter {
	return nil
}

func (c *pionTrackRaceContext) SSRC() webrtc.SSRC {
	return 42
}

func (c *pionTrackRaceContext) SSRCRetransmission() webrtc.SSRC {
	return 0
}

func (c *pionTrackRaceContext) SSRCForwardErrorCorrection() webrtc.SSRC {
	return 0
}

func (c *pionTrackRaceContext) WriteStream() webrtc.TrackLocalWriter {
	return c.writer
}

func (c *pionTrackRaceContext) ID() string {
	return "rtc-race"
}

func (c *pionTrackRaceContext) RTCPReader() interceptor.RTCPReader {
	return nil
}

func newPionTrackRace(t *testing.T, writer *pionTrackRaceWriter) (*webrtc.TrackLocalStaticRTP, *pionTrackRaceContext) {
	t.Helper()
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 1},
		"rtc-race",
		"stream",
	)
	if err != nil {
		t.Fatalf("create Pion local track: %v", err)
	}
	trackContext := &pionTrackRaceContext{writer: writer}
	if _, err := track.Bind(trackContext); err != nil {
		t.Fatalf("bind Pion local track: %v", err)
	}
	return track, trackContext
}

// TestOutboundTrackSerializesConcurrentWrites keeps the legacy race-gate name
// while covering the Pion local-track edge. Runtime framing and write ordering
// are tested in the runtime transport package.
func TestOutboundTrackSerializesConcurrentWrites(t *testing.T) {
	t.Parallel()

	writer := &pionTrackRaceWriter{}
	track, trackContext := newPionTrackRace(t, writer)
	t.Cleanup(func() {
		if err := track.Unbind(trackContext); err != nil {
			t.Errorf("unbind Pion local track during cleanup: %v", err)
		}
	})

	const writes = 8
	results := make(chan error, writes)
	var workers sync.WaitGroup
	workers.Add(writes)
	for index := 0; index < writes; index++ {
		go func(index int) {
			defer workers.Done()
			results <- track.WriteRTP(&rtp.Packet{
				Header:  rtp.Header{Version: 2, SequenceNumber: uint16(index)},
				Payload: []byte{byte(index), 0xa5},
			})
		}(index)
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Pion write: %v", err)
		}
	}

	packets := writer.snapshot()
	if len(packets) != writes {
		t.Fatalf("Pion writer received %d packets, want %d", len(packets), writes)
	}
	seen := make(map[uint16]bool, len(packets))
	for _, packet := range packets {
		if seen[packet.sequence] {
			t.Fatalf("duplicate sequence number %d", packet.sequence)
		}
		seen[packet.sequence] = true
		if len(packet.payload) != 2 || packet.payload[0] != byte(packet.sequence) {
			t.Fatalf("packet %d payload was %#v", packet.sequence, packet.payload)
		}
	}
}

// TestOutboundTrackConcurrentWriteCancelClose keeps the legacy race-gate name
// while covering concurrent Pion write and unbind teardown. Cancellation,
// ownership, and close semantics are owned by the runtime transport service.
func TestOutboundTrackConcurrentWriteCancelClose(t *testing.T) {
	t.Parallel()

	writer := &pionTrackRaceWriter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	track, trackContext := newPionTrackRace(t, writer)

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{1}})
	}()

	select {
	case <-writer.entered:
	case <-time.After(outboundTrackRaceTimeout):
		t.Fatal("Pion writer did not enter")
	}

	secondResult := make(chan error, 1)
	go func() {
		secondResult <- track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2}, Payload: []byte{2}})
	}()
	unbindResult := make(chan error, 1)
	go func() {
		unbindResult <- track.Unbind(trackContext)
	}()

	close(writer.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first concurrent Pion write: %v", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second concurrent Pion write: %v", err)
	}

	select {
	case err := <-unbindResult:
		if err != nil {
			t.Fatalf("unbind Pion local track: %v", err)
		}
	case <-time.After(outboundTrackRaceTimeout):
		t.Fatal("Pion unbind did not complete after concurrent writes")
	}
}

type compatibilityOutboundEncoder struct {
	frames    [][]int16
	encoded   []byte
	encodeErr error
	closeErr  error
}

func (e *compatibilityOutboundEncoder) Encode(_ context.Context, samples []int16) ([]byte, error) {
	e.frames = append(e.frames, append([]int16(nil), samples...))
	if e.encodeErr != nil {
		return nil, e.encodeErr
	}
	return append([]byte(nil), e.encoded...), nil
}

func (e *compatibilityOutboundEncoder) Close() error { return e.closeErr }

type compatibilityOutboundWriter struct {
	packets []*rtp.Packet
	err     error
	onWrite func()
}

func (w *compatibilityOutboundWriter) WriteRTP(_ context.Context, packet *rtp.Packet) error {
	clone := *packet
	clone.Payload = append([]byte(nil), packet.Payload...)
	w.packets = append(w.packets, &clone)
	if w.onWrite != nil {
		w.onWrite()
	}
	return w.err
}

type compatibilityOutboundPacer struct {
	offsets []uint64
	err     error
}

func (p *compatibilityOutboundPacer) Wait(_ context.Context, offset uint64) error {
	p.offsets = append(p.offsets, offset)
	return p.err
}

func TestOutboundTrackCompatibilityAdapter(t *testing.T) {
	t.Run("success preserves ownership and timeline", func(t *testing.T) {
		var encodedFrames [][]int16
		encoder := OpusEncoderFunc(func(_ context.Context, samples []int16) ([]byte, error) {
			encodedFrames = append(encodedFrames, append([]int16(nil), samples...))
			return []byte{0xa1, 0xb2}, nil
		})
		var packets []*rtp.Packet
		writer := RTPWriterFunc(func(_ context.Context, packet *rtp.Packet) error {
			clone := *packet
			clone.Payload = append([]byte(nil), packet.Payload...)
			packets = append(packets, &clone)
			return nil
		})
		var offsets []uint64
		pacer := PacerFunc(func(_ context.Context, offset uint64) error {
			offsets = append(offsets, offset)
			return nil
		})
		track, err := NewOutboundTrack(OutboundTrackConfig{
			SourceRate:            wavio.Rate16kHz,
			Encoder:               encoder,
			Writer:                writer,
			Pacer:                 pacer,
			InitialSequenceNumber: 41,
			InitialTimestamp:      9000,
		})
		if err != nil {
			t.Fatalf("NewOutboundTrack() error = %v", err)
		}
		samples := make([]int16, 320)
		samples[0] = 11
		before := append([]int16(nil), samples...)
		if err := track.WriteFrame(nil, sharedaudio.PCMFrame{Samples: samples}); err != nil {
			t.Fatalf("WriteFrame(nil) error = %v", err)
		}
		if !reflect.DeepEqual(samples, before) {
			t.Fatal("WriteFrame mutated caller samples")
		}
		if len(encodedFrames) != 1 || len(encodedFrames[0]) != 960 {
			t.Fatalf("encoded frames = %d/%d, want one 960-sample frame", len(encodedFrames), len(encodedFrames[0]))
		}
		if len(packets) != 1 || packets[0].SequenceNumber != 41 || packets[0].Timestamp != 9000 || packets[0].PayloadType != 111 || packets[0].SSRC != 1 || !packets[0].Marker {
			t.Fatalf("packet = %#v, want default payload/SSRC and initial marker", packets)
		}
		if !reflect.DeepEqual(offsets, []uint64{0}) || !reflect.DeepEqual(packets[0].Payload, []byte{0xa1, 0xb2}) {
			t.Fatalf("pacing/payload = %v/%#v", offsets, packets[0].Payload)
		}
		if err := track.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("defaults use the legacy pacer", func(t *testing.T) {
		track, err := NewOutboundTrack(OutboundTrackConfig{
			SourceRate: wavio.Rate48kHz,
			Encoder:    OpusEncoderFunc(func(context.Context, []int16) ([]byte, error) { return []byte{1}, nil }),
			Writer:     RTPWriterFunc(func(context.Context, *rtp.Packet) error { return nil }),
		})
		if err != nil {
			t.Fatalf("NewOutboundTrack() error = %v", err)
		}
		if _, ok := track.pacer.(*legacyWallClockPacer); !ok {
			t.Fatalf("default pacer type = %T, want legacyWallClockPacer", track.pacer)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := track.pacer.Wait(ctx, 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled default pacer = %v, want context.Canceled", err)
		}
		if err := track.pacer.Wait(context.Background(), 0); err != nil {
			t.Fatalf("first default pacing = %v", err)
		}
		if got, want := legacySampleOffsetDuration(OutboundRTPClockRate+1), time.Second+time.Second/OutboundRTPClockRate; got != want {
			t.Fatalf("sample offset duration = %v, want %v", got, want)
		}
		if err := track.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
}

func TestOutboundTrackCompatibilityConstructionAndErrors(t *testing.T) {
	var nilEncoder *compatibilityOutboundEncoder
	var nilWriter *compatibilityOutboundWriter
	validEncoder := &compatibilityOutboundEncoder{encoded: []byte{1}}
	validWriter := &compatibilityOutboundWriter{}
	if _, err := NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: nilEncoder, Writer: validWriter}); !errors.Is(err, ErrOutboundNilEncoder) {
		t.Fatalf("typed nil encoder error = %v, want %v", err, ErrOutboundNilEncoder)
	}
	if _, err := NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: validEncoder, Writer: nilWriter}); !errors.Is(err, ErrOutboundNilWriter) {
		t.Fatalf("typed nil writer error = %v, want %v", err, ErrOutboundNilWriter)
	}
	if _, err := NewOutboundTrack(OutboundTrackConfig{SourceRate: 11025, Encoder: validEncoder, Writer: validWriter}); !errors.Is(err, wavio.ErrUnsupportedResampleRate) {
		t.Fatalf("unsupported source rate error = %v, want %v", err, wavio.ErrUnsupportedResampleRate)
	}

	if ErrOutboundClosed.Error() == "" {
		t.Fatal("outbound error identity has empty text")
	}
	cause := errors.New("operation failed")
	wrapped := &OutboundOperationError{Operation: "test", Err: cause}
	if wrapped.Error() == "" || wrapped.Unwrap() != cause || !errors.Is(wrapped, cause) {
		t.Fatalf("operation error methods lost cause: %v", wrapped)
	}

	t.Run("write failures", func(t *testing.T) {
		encodeErr := errors.New("encode failed")
		track, err := NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: &compatibilityOutboundEncoder{encodeErr: encodeErr}, Writer: &compatibilityOutboundWriter{}})
		if err != nil {
			t.Fatalf("encoder-error construction = %v", err)
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: []int16{1}}); !errors.Is(err, encodeErr) {
			t.Fatalf("encoder error = %v, want %v", err, encodeErr)
		}

		track, err = NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: &compatibilityOutboundEncoder{encoded: nil}, Writer: &compatibilityOutboundWriter{}})
		if err != nil {
			t.Fatalf("empty-payload construction = %v", err)
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: []int16{1}}); !errors.Is(err, ErrOutboundEmptyPayload) {
			t.Fatalf("empty payload error = %v, want %v", err, ErrOutboundEmptyPayload)
		}

		paceErr := errors.New("pace failed")
		track, err = NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: validEncoder, Writer: &compatibilityOutboundWriter{}, Pacer: &compatibilityOutboundPacer{err: paceErr}})
		if err != nil {
			t.Fatalf("pacer-error construction = %v", err)
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: []int16{1}}); !errors.Is(err, paceErr) {
			t.Fatalf("pacer error = %v, want %v", err, paceErr)
		}

		writeErr := errors.New("write failed")
		writer := &compatibilityOutboundWriter{err: writeErr}
		track, err = NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: validEncoder, Writer: writer, Pacer: &compatibilityOutboundPacer{}})
		if err != nil {
			t.Fatalf("writer-error construction = %v", err)
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: []int16{1}}); !errors.Is(err, writeErr) {
			t.Fatalf("writer error = %v, want %v", err, writeErr)
		}
		writer.err = nil
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: []int16{2}}); err != nil {
			t.Fatalf("write after failed writer = %v", err)
		}
		if len(writer.packets) != 2 || writer.packets[1].SequenceNumber != writer.packets[0].SequenceNumber {
			t.Fatalf("failed write committed RTP state: packets = %#v", writer.packets)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		track, err = NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: validEncoder, Writer: &compatibilityOutboundWriter{}, Pacer: &compatibilityOutboundPacer{}})
		if err != nil {
			t.Fatalf("canceled construction = %v", err)
		}
		if err := track.WriteFrame(ctx, sharedaudio.PCMFrame{Samples: []int16{1}}); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled write = %v, want context.Canceled", err)
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{}); !errors.Is(err, ErrOutboundEmptyFrame) {
			t.Fatalf("empty write = %v, want %v", err, ErrOutboundEmptyFrame)
		}
	})

	t.Run("close during write and close errors", func(t *testing.T) {
		var track *OutboundTrack
		writer := &compatibilityOutboundWriter{onWrite: func() { _ = track.Close() }}
		track, err := NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: &compatibilityOutboundEncoder{encoded: []byte{1}}, Writer: writer, Pacer: &compatibilityOutboundPacer{}})
		if err != nil {
			t.Fatalf("close-during-write construction = %v", err)
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: []int16{1}}); !errors.Is(err, ErrOutboundClosed) {
			t.Fatalf("write after concurrent close = %v, want %v", err, ErrOutboundClosed)
		}

		closeErr := errors.New("encoder close failed")
		track, err = NewOutboundTrack(OutboundTrackConfig{SourceRate: wavio.Rate48kHz, Encoder: &compatibilityOutboundEncoder{encoded: []byte{1}, closeErr: closeErr}, Writer: &compatibilityOutboundWriter{}, Pacer: &compatibilityOutboundPacer{}})
		if err != nil {
			t.Fatalf("close-error construction = %v", err)
		}
		if err := track.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("Close() error = %v, want %v", err, closeErr)
		}
		if err := track.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("second Close() error = %v, want same identity", err)
		}
		if err := track.WriteFrame(context.Background(), sharedaudio.PCMFrame{Samples: []int16{1}}); !errors.Is(err, ErrOutboundClosed) {
			t.Fatalf("write after Close() = %v, want %v", err, ErrOutboundClosed)
		}
	})
}
