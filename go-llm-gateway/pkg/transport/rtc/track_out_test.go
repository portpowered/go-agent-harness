package rtc

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
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
