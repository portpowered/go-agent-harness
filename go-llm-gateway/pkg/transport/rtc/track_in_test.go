package rtc

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestDefaultInboundTrackConfigUsesThreeTwentyMillisecondPackets(t *testing.T) {
	config := DefaultInboundTrackConfig()
	if config.FrameDuration != 20*time.Millisecond || config.JitterDepth != 60*time.Millisecond || config.JitterBufferDepth != 60*time.Millisecond {
		t.Fatalf("default timing = frame %v, depth %v/%v; want 20 ms, 60 ms, 60 ms", config.FrameDuration, config.JitterDepth, config.JitterBufferDepth)
	}
	normalized, err := config.normalize()
	if err != nil {
		t.Fatalf("default config normalize() error = %v", err)
	}
	if normalized.jitterPackets != 3 {
		t.Fatalf("default jitter packets = %d, want 3", normalized.jitterPackets)
	}
}

func TestInboundTrackOrderedDisorderFidelityAndOwnership(t *testing.T) {
	source := newTestPacketSource(8)
	decoder := &testOpusDecoder{samples: 48000 / 50}
	track := newTestTrack(t, source, decoder, InboundTrackConfig{})

	baseTimestamp := uint32(1000)
	source.push(testRTPPacket(100, baseTimestamp, 0))
	source.push(testRTPPacket(102, baseTimestamp+2*960, 2))
	source.push(testRTPPacket(101, baseTimestamp+960, 1))
	source.close()

	frames := readTestFrames(t, track, 3)
	for index, frame := range frames {
		want := voicedTestFrame(index, len(frame.Samples))
		if got := normalizedRMSError(frame.Samples, want); got > 0.35 {
			t.Fatalf("frame %d normalized RMS error = %v, want <= 0.35", index, got)
		}
		if rmsDBDifference(frame.Samples, want) > 3 {
			t.Fatalf("frame %d RMS energy differs by more than 3 dB", index)
		}
		if isSilent(frame.Samples) {
			t.Fatalf("frame %d is silent", index)
		}
	}
	if got := decoder.decodedIDs(); !equalBytes(got, []byte{0, 1, 2}) {
		t.Fatalf("decoded payload order = %v, want [0 1 2]", got)
	}
	if len(frames[0].Samples) != 960 || len(frames[1].Samples) != 960 || len(frames[2].Samples) != 960 {
		t.Fatalf("frame lengths = %d, %d, %d; want 960 each", len(frames[0].Samples), len(frames[1].Samples), len(frames[2].Samples))
	}

	original := frames[0].Samples[10]
	frames[0].Samples[10] = original + 1
	if frames[1].Samples[10] == frames[0].Samples[10] {
		t.Fatal("successive ReadFrame calls share sample storage")
	}
}

func TestInboundTrackResamplesSupportedLoopRates(t *testing.T) {
	for _, rate := range []int{wavio.Rate16kHz, wavio.Rate24kHz, wavio.Rate48kHz} {
		t.Run(rateName(rate), func(t *testing.T) {
			source := newTestPacketSource(8)
			decoder := &testOpusDecoder{samples: 960}
			track := newTestTrack(t, source, decoder, InboundTrackConfig{
				SampleRate:    rate,
				FrameDuration: 20 * time.Millisecond,
				JitterDepth:   60 * time.Millisecond,
			})
			for index := range 3 {
				source.push(testRTPPacket(uint16(200+index), uint32(5000+index*960), byte(index))) //nolint:gosec // test sequence is bounded
			}
			source.close()
			frame := readTestFrame(t, track)
			want, err := wavio.Resample(voicedTestFrame(0, 960), 48000, rate)
			if err != nil {
				t.Fatalf("reference Resample() error = %v", err)
			}
			if len(frame.Samples) != len(want) {
				t.Fatalf("frame length = %d, want %d", len(frame.Samples), len(want))
			}
			for index := range want {
				if frame.Samples[index] != want[index] {
					t.Fatalf("sample %d = %d, want %d", index, frame.Samples[index], want[index])
				}
			}
		})
	}
}

func TestInboundTrackLossUsesExactlyOnePLCFrame(t *testing.T) {
	source := newTestPacketSource(8)
	decoder := &testOpusDecoder{samples: 960}
	track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 60 * time.Millisecond})
	source.push(testRTPPacket(300, 8000, 0))
	source.push(testRTPPacket(301, 8960, 1))
	source.push(testRTPPacket(303, 10880, 3))
	source.close()

	frames := readTestFrames(t, track, 4)
	if got := decoder.decodedIDs(); !equalBytes(got, []byte{0, 1, 3}) {
		t.Fatalf("ordinary decode calls = %v, want [0 1 3]", got)
	}
	if got := decoder.plcCount(); got != 1 {
		t.Fatalf("PLC calls = %d, want exactly one", got)
	}
	if isSilent(frames[2].Samples) || !finiteRMS(frames[2].Samples) {
		t.Fatal("concealed frame is silent or non-finite")
	}
	if frames[3].Samples[10] != voicedTestFrame(3, 960)[10] {
		t.Fatal("ordinary decode did not resume after concealed frame")
	}
}

func TestInboundTrackSuppressesDuplicatesLatePacketsAndOrdersWraparound(t *testing.T) {
	t.Run("duplicate-and-late", func(t *testing.T) {
		source := newTestPacketSource(16)
		decoder := &testOpusDecoder{samples: 960}
		track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 60 * time.Millisecond})
		source.push(testRTPPacket(10, 1000, 0))
		source.push(testRTPPacket(12, 2920, 2))
		source.push(testRTPPacket(11, 1960, 1))
		source.push(testRTPPacket(10, 1000, 0))
		source.push(testRTPPacket(9, 40, 9))
		source.close()
		_ = readTestFrames(t, track, 3)
		if got := decoder.decodedIDs(); !equalBytes(got, []byte{0, 1, 2}) {
			t.Fatalf("decoded IDs = %v, want [0 1 2]", got)
		}
	})

	t.Run("sequence-wrap", func(t *testing.T) {
		source := newTestPacketSource(8)
		decoder := &testOpusDecoder{samples: 960}
		track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 60 * time.Millisecond})
		base := uint32(0xfffff000)
		source.push(testRTPPacket(65534, base, 0))
		source.push(testRTPPacket(0, base+2*960, 2))
		source.push(testRTPPacket(65535, base+960, 1))
		source.close()
		_ = readTestFrames(t, track, 3)
		if got := decoder.decodedIDs(); !equalBytes(got, []byte{0, 1, 2}) {
			t.Fatalf("wraparound decoded IDs = %v, want [0 1 2]", got)
		}
	})
}

func TestInboundTrackValidationAndErrorIdentity(t *testing.T) {
	source := newTestPacketSource(2)
	decoder := &testOpusDecoder{samples: 960}
	for _, test := range []struct {
		name string
		cfg  InboundTrackConfig
		want error
	}{
		{name: "rate", cfg: InboundTrackConfig{SampleRate: 44100}, want: ErrInvalidInboundTrackConfig},
		{name: "frame", cfg: InboundTrackConfig{FrameDuration: 15 * time.Millisecond}, want: ErrInvalidInboundTrackConfig},
		{name: "depth", cfg: InboundTrackConfig{JitterDepth: 25 * time.Millisecond}, want: ErrInvalidInboundTrackConfig},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewInboundTrack(source, decoder, test.cfg); !errors.Is(err, test.want) {
				t.Fatalf("NewInboundTrack() error = %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}
	if _, err := NewInboundTrack(nil, decoder, DefaultInboundTrackConfig()); !errors.Is(err, ErrNilInboundRTPTrack) {
		t.Fatalf("nil source error = %v, want ErrNilInboundRTPTrack", err)
	}
	if _, err := NewInboundTrack(source, nil, DefaultInboundTrackConfig()); !errors.Is(err, ErrNilOpusDecoder) {
		t.Fatalf("nil decoder error = %v, want ErrNilOpusDecoder", err)
	}

	source.push(testRTPPacket(1, 100, 0))
	source.push(testRTPPacket(2, 999, 1))
	track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 20 * time.Millisecond})
	defer track.Close()
	readTestFrame(t, track)
	if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrImpossibleRTPProgress) {
		t.Fatalf("timestamp progress error = %v, want ErrImpossibleRTPProgress", err)
	}
}

func TestInboundTrackCancellationAndCloseUnblockReads(t *testing.T) {
	source := newTestPacketSource(1)
	track := newTestTrack(t, source, &testOpusDecoder{samples: 960}, InboundTrackConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := track.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ReadFrame() error = %v, want context.Canceled", err)
	}
	if err := track.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := track.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrInboundTrackClosed) {
		t.Fatalf("post-close ReadFrame() error = %v, want ErrInboundTrackClosed", err)
	}
}

func TestInboundTrackS8ConcurrentIngestReadCancelClose(t *testing.T) {
	source := newTestPacketSource(32)
	track := newTestTrack(t, source, &testOpusDecoder{samples: 960}, InboundTrackConfig{JitterDepth: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	start := make(chan struct{})
	producedHalf := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		for index := range 24 {
			source.push(testRTPPacket(uint16(100+index), uint32(2000+index*960), byte(index))) //nolint:gosec // bounded test value
			if index == 8 {
				close(producedHalf)
			}
		}
		source.close()
	}()
	go func() {
		defer wg.Done()
		<-start
		for {
			frame, err := track.ReadFrame(ctx)
			if err != nil {
				return
			}
			if len(frame.Samples) != 960 {
				t.Errorf("concurrent frame length = %d, want 960", len(frame.Samples))
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		<-producedHalf
		once.Do(cancel)
		_ = track.Close()
	}()
	close(start)
	wg.Wait()
	if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrInboundTrackClosed) {
		t.Fatalf("concurrent post-close ReadFrame() error = %v, want closed", err)
	}
}

func FuzzInboundTrackIngress(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4})
	f.Add([]byte{0xff, 0x00, 0x80, 0x7f})
	f.Fuzz(func(t *testing.T, data []byte) {
		source := newTestPacketSource(len(data) + 2)
		track, err := NewInboundTrack(source, &testOpusDecoder{samples: 960}, InboundTrackConfig{JitterDepth: 20 * time.Millisecond})
		if err != nil {
			t.Fatalf("NewInboundTrack() error = %v", err)
		}
		defer track.Close()
		limit := len(data)
		if limit > 32 {
			limit = 32
		}
		packets := make([]*rtp.Packet, 0, limit)
		for index := range limit {
			payload := []byte{data[index]}
			id := data[index]
			if data[index]&0x80 != 0 {
				payload = nil
			}
			packets = append(packets, testRTPPacket(uint16(1000+index), uint32(4000+index*960), id)) //nolint:gosec // bounded fuzz index
			packets[len(packets)-1].Payload = payload
		}
		if len(data) > 0 && data[0]&1 != 0 {
			reversePackets(packets)
		}
		for _, packet := range packets {
			source.push(packet)
		}
		source.close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for range limit + 2 {
			frame, readErr := track.ReadFrame(ctx)
			if readErr != nil {
				break
			}
			if len(frame.Samples) != 960 {
				t.Fatalf("fuzz frame length = %d, want 960", len(frame.Samples))
			}
		}
	})
}

const inboundTrackAllocationBudget = 6

var inboundTrackAllocationSink int16

func TestInboundTrackS10AllocationBudget(t *testing.T) {
	source := newTestPacketSource(2)
	track := newTestTrack(t, source, &testOpusDecoder{samples: 960}, InboundTrackConfig{JitterDepth: 20 * time.Millisecond})
	defer track.Close()
	var sequence uint16 = 10
	source.push(testRTPPacket(sequence, 1000, 1))
	readTestFrame(t, track)
	measured := testing.AllocsPerRun(100, func() {
		sequence++
		source.push(testRTPPacket(sequence, 1000+uint32(sequence-10)*960, 1)) //nolint:gosec // bounded benchmark sequence
		frame := readTestFrame(t, track)
		inboundTrackAllocationSink ^= frame.Samples[len(frame.Samples)-1]
	})
	if measured > inboundTrackAllocationBudget {
		t.Fatalf("steady-state allocations/frame = %v, want <= committed budget %d", measured, inboundTrackAllocationBudget)
	}
	if measured > 0 && testing.AllocsPerRun(100, func() {
		sequence++
		source.push(testRTPPacket(sequence, 1000+uint32(sequence-10)*960, 1)) //nolint:gosec // bounded benchmark sequence
		frame := readTestFrame(t, track)
		inboundTrackAllocationSink ^= frame.Samples[0]
	}) < measured-1 {
		t.Fatal("allocation gate comparison was not monotonic")
	}
}

func BenchmarkInboundTrackS10SteadyState(b *testing.B) {
	source := newTestPacketSource(2)
	track := newTestTrack(b, source, &testOpusDecoder{samples: 960}, InboundTrackConfig{JitterDepth: 20 * time.Millisecond})
	defer track.Close()
	var sequence uint16 = 10
	source.push(testRTPPacket(sequence, 1000, 1))
	readTestFrame(b, track)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sequence++
		source.push(testRTPPacket(sequence, 1000+uint32(sequence-10)*960, 1)) //nolint:gosec // bounded benchmark sequence
		frame := readTestFrame(b, track)
		inboundTrackAllocationSink ^= frame.Samples[0]
	}
}

type testPacketSource struct {
	packets chan *rtp.Packet
	closed  chan struct{}
	once    sync.Once
}

func newTestPacketSource(capacity int) *testPacketSource {
	return &testPacketSource{packets: make(chan *rtp.Packet, capacity), closed: make(chan struct{})}
}

func (s *testPacketSource) ReadRTP() (*rtp.Packet, error) {
	select {
	case packet := <-s.packets:
		return packet, nil
	default:
	}
	select {
	case packet := <-s.packets:
		return packet, nil
	case <-s.closed:
		return nil, io.EOF
	}
}

func (s *testPacketSource) push(packet *rtp.Packet) {
	select {
	case s.packets <- packet:
	case <-s.closed:
	}
}

func (s *testPacketSource) close() {
	s.once.Do(func() { close(s.closed) })
}

type testOpusDecoder struct {
	samples int

	mu      sync.Mutex
	decoded []byte
	plc     int
}

func (d *testOpusDecoder) Decode(payload []byte) ([]int16, error) {
	if len(payload) != 1 {
		return nil, errors.New("test decoder rejected payload")
	}
	d.mu.Lock()
	d.decoded = append(d.decoded, payload[0])
	d.mu.Unlock()
	return voicedTestFrame(int(payload[0]), d.samples), nil
}

func (d *testOpusDecoder) DecodePLC() ([]int16, error) {
	d.mu.Lock()
	d.plc++
	id := 100 + d.plc
	d.mu.Unlock()
	return voicedTestFrame(id, d.samples), nil
}

func (d *testOpusDecoder) decodedIDs() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]byte(nil), d.decoded...)
}

func (d *testOpusDecoder) plcCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.plc
}

func newTestTrack(t testing.TB, source *testPacketSource, decoder *testOpusDecoder, config InboundTrackConfig) *InboundTrack {
	t.Helper()
	track, err := NewInboundTrack(source, decoder, config)
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	t.Cleanup(func() {
		_ = track.Close()
	})
	return track
}

func testRTPPacket(sequence uint16, timestamp uint32, id byte) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: timestamp, SSRC: 7, PayloadType: 111},
		Payload: []byte{id},
	}
}

func readTestFrame(t testing.TB, track *InboundTrack) PCMFrame {
	t.Helper()
	frame, err := track.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	return frame
}

func readTestFrames(t testing.TB, track *InboundTrack, count int) []PCMFrame {
	t.Helper()
	frames := make([]PCMFrame, count)
	for index := range frames {
		frames[index] = readTestFrame(t, track)
	}
	return frames
}

func voicedTestFrame(id, samples int) []int16 {
	frame := make([]int16, samples)
	for index := range frame {
		phase := float64(index%160) / 160 * 2 * math.Pi
		frame[index] = int16(8000*math.Sin(phase) + float64((id%7)+1)*300) //nolint:gosec // bounded test PCM
	}
	return frame
}

func normalizedRMSError(got, want []int16) float64 {
	if len(got) != len(want) || len(got) == 0 {
		return math.Inf(1)
	}
	var sumError, sumEnergy float64
	for index := range got {
		difference := float64(got[index] - want[index])
		sumError += difference * difference
		value := float64(want[index])
		sumEnergy += value * value
	}
	return math.Sqrt(sumError / sumEnergy)
}

func rmsDBDifference(got, want []int16) float64 {
	return math.Abs(20 * math.Log10(rms(got)/rms(want)))
}

func rms(samples []int16) float64 {
	var energy float64
	for _, sample := range samples {
		value := float64(sample)
		energy += value * value
	}
	return math.Sqrt(energy / float64(len(samples)))
}

func finiteRMS(samples []int16) bool {
	value := rms(samples)
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

func isSilent(samples []int16) bool { return rms(samples) == 0 }

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func reversePackets(packets []*rtp.Packet) {
	for left, right := 0, len(packets)-1; left < right; left, right = left+1, right-1 {
		packets[left], packets[right] = packets[right], packets[left]
	}
}

func rateName(rate int) string {
	switch rate {
	case wavio.Rate16kHz:
		return "16kHz"
	case wavio.Rate24kHz:
		return "24kHz"
	default:
		return "48kHz"
	}
}
