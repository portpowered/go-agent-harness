package rtc

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// These payloads are the first three 20 ms, 48 kHz Opus packets from a
// deterministic libopus-encoded 440 Hz fixture. The PCM is independently
// recorded from decoding that fixture, not synthesized from RTP identifiers.
var recordedOpusPayloads = []string{
	"+LSvyqrltbCmHLF66Fca8g3O26Ne2AmRzMF+xSKd0GhEewJMR9Fl2aTqEgyzPsGglmQPTrFtxkwyxsu0G8P57WI6BMB0dnYMSsBUwZmdhVimQyIc+jFvVIlVttg9LPuW0gD7A+pVGUPgFwdjGw23HPJhIGfs5ccocQPs44WD62utQwmmgK7aaEW98bNIax4bfkzW1CC7kJhWjJmFAHWDbQ==",
	"+LFymmozjx0fNV9JZztVY5sPZ7nnWLCGNzfiFoj/SjRkWQsVSzVJfggSuJsXX6tBS4K7ZYrUMEsxV4OidR7nJj6ZyqFJDwjhNgwVHhTntC55UO4S9Q5LM4RCZtw1Amm3haie6LjN2jtkZxpmlNrItYx2inUqiwt4NvAw3iEo6YZ/kdEy8En2dSvVuckpnQwK3IRpMkE1O/L6fcLM1IIdrg==",
	"+LBNzyoEDmbxki8tQvQWhwM3dC6pEzRKUcmbX2HgfvJm/irwT0QZt498a2adYa4YD741kWZYS4AiNgR5x1LferxqhS2x0aWmPsLSuH6Pd0UynneWOTAvi9o69ST4ulZQUY+sTY76zW8Cm+xTpPpXp4sbMWM7ruAsHInBCUAjO7oltM+xVAdaStDLEvwt1LjZcogp8epi9sGUpw+i6H9Prg==",
}
var recordedOpusPCM = []string{
	"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",
}

type recordedOpusDecoder struct {
	mu      sync.Mutex
	frames  map[string][]int16
	indexes map[string]byte
	decoded []byte
}

func newRecordedOpusDecoder(t testing.TB) *recordedOpusDecoder {
	t.Helper()
	d := &recordedOpusDecoder{frames: make(map[string][]int16), indexes: make(map[string]byte)}
	for index := range recordedOpusPayloads {
		payload, err := base64.StdEncoding.DecodeString(recordedOpusPayloads[index])
		if err != nil {
			t.Fatalf("decode recorded Opus payload %d: %v", index, err)
		}
		encodedPCM, err := base64.StdEncoding.DecodeString(recordedOpusPCM[index])
		if err != nil {
			t.Fatalf("decode recorded PCM %d: %v", index, err)
		}
		if len(encodedPCM) != 2*960 {
			t.Fatalf("recorded PCM %d has %d bytes, want %d", index, len(encodedPCM), 2*960)
		}
		pcm := make([]int16, 960)
		for sample := range pcm {
			pcm[sample] = int16(binary.LittleEndian.Uint16(encodedPCM[2*sample:]))
		}
		d.frames[string(payload)] = pcm
		d.indexes[string(payload)] = byte(index) //nolint:gosec // fixture has three entries
	}
	return d
}

func (d *recordedOpusDecoder) Decode(payload []byte) ([]int16, error) {
	d.mu.Lock()
	frame, ok := d.frames[string(payload)]
	if ok {
		d.decoded = append(d.decoded, d.indexes[string(payload)])
	}
	d.mu.Unlock()
	if !ok {
		return nil, errors.New("recorded decoder rejected non-fixture Opus payload")
	}
	return append([]int16(nil), frame...), nil
}
func (d *recordedOpusDecoder) DecodePLC() ([]int16, error) { return opusSourceFrame(0, 960), nil }
func (d *recordedOpusDecoder) decodedIndexes() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]byte(nil), d.decoded...)
}

func fixtureRTPPacket(sequence uint16, timestamp uint32, index int) *rtp.Packet {
	payload, _ := base64.StdEncoding.DecodeString(recordedOpusPayloads[index%len(recordedOpusPayloads)])
	packet := testRTPPacket(sequence, timestamp, 0)
	packet.Payload = payload
	return packet
}

func opusSourceFrame(frame, samples int) []int16 {
	result := make([]int16, samples)
	for index := range result {
		phase := 2 * math.Pi * 440 * float64(frame*960+index) / 48000
		result[index] = int16(4095 * math.Sin(phase)) //nolint:gosec // bounded fixture amplitude
	}
	return result
}

func TestDefaultInboundTrackConfigUsesThreeTwentyMillisecondPackets(t *testing.T) {
	config := DefaultInboundTrackConfig()
	if config.FrameDuration != 20*time.Millisecond || config.JitterDepth != 60*time.Millisecond {
		t.Fatalf("default timing = frame %v, depth %v; want 20 ms, 60 ms", config.FrameDuration, config.JitterDepth)
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
	decoder := newRecordedOpusDecoder(t)
	track := newTestTrack(t, source, decoder, InboundTrackConfig{})

	baseTimestamp := uint32(1000)
	source.push(fixtureRTPPacket(100, baseTimestamp, 0))
	source.push(fixtureRTPPacket(102, baseTimestamp+2*960, 2))
	source.push(fixtureRTPPacket(101, baseTimestamp+960, 1))
	source.close()

	frames := readTestFrames(t, track, 3)
	for index, frame := range frames {
		want := opusSourceFrame(index, len(frame.Samples))
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
	if got := decoder.decodedIndexes(); !equalBytes(got, []byte{0, 1, 2}) {
		t.Fatalf("decoded Opus payload order = %v, want [0 1 2]", got)
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
	const totalFrames = 20
	const missingIndex = 5 // deterministic 5% loss: one packet out of twenty
	source := newTestPacketSource(totalFrames)
	decoder := &testOpusDecoder{samples: 960}
	track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 60 * time.Millisecond})
	for index := range totalFrames {
		if index != missingIndex {
			source.push(testRTPPacket(uint16(300+index), uint32(8000+index*960), byte(index))) //nolint:gosec // bounded fixture
		}
	}
	source.close()

	frames := readTestFrames(t, track, totalFrames)
	wantDecoded := make([]byte, 0, totalFrames-1)
	for index := range totalFrames {
		if index != missingIndex {
			wantDecoded = append(wantDecoded, byte(index))
		}
	} //nolint:gosec // bounded fixture
	if got := decoder.decodedIDs(); !equalBytes(got, wantDecoded) {
		t.Fatalf("ordinary decode calls = %v, want %v without timestamp %d", got, wantDecoded, 8000+missingIndex*960)
	}
	if got := decoder.plcCount(); got != 1 {
		t.Fatalf("PLC calls = %d, want exactly one", got)
	}
	for index, frame := range frames {
		if len(frame.Samples) != 960 {
			t.Fatalf("frame %d length = %d, want 960", index, len(frame.Samples))
		}
		if index == missingIndex {
			if isSilent(frame.Samples) || !finiteRMS(frame.Samples) {
				t.Fatal("concealed frame is silent or non-finite")
			}
			continue
		}
		if frame.Samples[10] != voicedTestFrame(index, 960)[10] {
			t.Fatalf("ordinary decode at timestamp index %d did not preserve order", index)
		}
	}
	if frames[missingIndex+1].Samples[10] != voicedTestFrame(missingIndex+1, 960)[10] {
		t.Fatal("ordinary decode did not resume after the concealed timestamp")
	}
}

func TestInboundTrackDeadlineDrivesPLCWithoutEOF(t *testing.T) {
	timers := &manualTimerFactory{requests: make(chan timerRequest, 8)}
	source := newTestPacketSource(4)
	decoder := &testOpusDecoder{samples: 960}
	track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 40 * time.Millisecond, NewTimer: timers.newTimer})
	source.push(testRTPPacket(400, 12000, 0))
	initial := timers.next(t)
	if initial.duration != 40*time.Millisecond {
		t.Fatalf("initial timer = %v, want jitter depth 40 ms", initial.duration)
	}
	initial.fire()
	_ = readTestFrame(t, track)
	frameTimer := timers.next(t)
	if frameTimer.duration != 20*time.Millisecond {
		t.Fatalf("frame timer = %v, want frame duration 20 ms", frameTimer.duration)
	}
	frameTimer.fire()
	concealed := readTestFrame(t, track)
	if decoder.plcCount() != 1 || isSilent(concealed.Samples) || !finiteRMS(concealed.Samples) {
		t.Fatalf("deadline PLC without a later packet = calls %d, rms-valid %v; want one non-silent frame", decoder.plcCount(), finiteRMS(concealed.Samples))
	}
	if got := decoder.decodedIDs(); !equalBytes(got, []byte{0}) {
		t.Fatalf("decoded IDs = %v, want [0] before any later packet", got)
	}
}

func TestInboundTrackPostStartGapDrivesPLCBeforeBufferedPacket(t *testing.T) {
	source := newTestPacketSource(1)
	decoder := &testOpusDecoder{samples: 960}
	track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 20 * time.Millisecond})
	state := playout{track: track, packets: make(map[int64]*rtp.Packet)}

	if err := state.push(testRTPPacket(100, 2000, 0)); err != nil {
		t.Fatalf("push initial packet: %v", err)
	}
	if err := state.tick(); err != nil {
		t.Fatalf("initial playout tick: %v", err)
	}
	readTestFrame(t, track)

	if err := state.push(testRTPPacket(102, 3920, 2)); err != nil {
		t.Fatalf("push packet after playout started: %v", err)
	}
	if err := state.tick(); err != nil {
		t.Fatalf("gap playout tick: %v", err)
	}
	concealed := readTestFrame(t, track)
	if decoder.plcCount() != 1 || !finiteRMS(concealed.Samples) {
		t.Fatalf("post-start PLC = calls %d, valid RMS %v; want one valid concealed frame", decoder.plcCount(), finiteRMS(concealed.Samples))
	}

	if err := state.tick(); err != nil {
		t.Fatalf("resume playout tick: %v", err)
	}
	resumed := readTestFrame(t, track)
	if resumed.Samples[10] != voicedTestFrame(2, 960)[10] {
		t.Fatal("buffered packet did not resume ordinary decode after post-start PLC")
	}
	if got := decoder.decodedIDs(); !equalBytes(got, []byte{0, 2}) {
		t.Fatalf("decoded IDs = %v, want [0 2] with one post-start PLC", got)
	}
}

func TestInboundTrackSuppressesDuplicatesLatePacketsAndOrdersWraparound(t *testing.T) {
	t.Run("duplicate-and-late", func(t *testing.T) {
		source := newTestPacketSource(16)
		decoder := &testOpusDecoder{samples: 960}
		timers := &manualTimerFactory{requests: make(chan timerRequest, 8)}
		track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 60 * time.Millisecond, NewTimer: timers.newTimer})
		source.push(testRTPPacket(10, 1000, 0))
		source.push(testRTPPacket(12, 2920, 2))
		source.push(testRTPPacket(11, 1960, 1))
		source.push(testRTPPacket(10, 1000, 0))
		initial := timers.next(t)
		initial.fire()
		readTestFrame(t, track)
		timers.next(t).fire()
		readTestFrame(t, track)
		timers.next(t).fire()
		readTestFrame(t, track)
		source.push(testRTPPacket(9, 40, 9))
		source.close()
		if got := decoder.decodedIDs(); !equalBytes(got, []byte{0, 1, 2}) {
			t.Fatalf("decoded IDs = %v, want [0 1 2]", got)
		}
	})

	t.Run("first-arrival-is-not-earliest", func(t *testing.T) {
		source := newTestPacketSource(8)
		decoder := &testOpusDecoder{samples: 960}
		track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 60 * time.Millisecond})
		source.push(testRTPPacket(102, 2920, 2))
		source.push(testRTPPacket(100, 1000, 0))
		source.push(testRTPPacket(101, 1960, 1))
		source.close()
		_ = readTestFrames(t, track, 3)
		if got := decoder.decodedIDs(); !equalBytes(got, []byte{0, 1, 2}) {
			t.Fatalf("initial-window decoded IDs = %v, want [0 1 2]", got)
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
	var typedNilDecoder *testOpusDecoder
	if _, err := NewInboundTrack(source, typedNilDecoder, DefaultInboundTrackConfig()); !errors.Is(err, ErrNilOpusDecoder) {
		t.Fatalf("typed nil decoder error = %v, want ErrNilOpusDecoder", err)
	}
	if _, err := NewInboundTrack(struct{}{}, decoder, DefaultInboundTrackConfig()); !errors.Is(err, ErrInvalidInboundTrackConfig) {
		t.Fatalf("unsupported source error = %v, want ErrInvalidInboundTrackConfig", err)
	}
	if _, err := NewInboundTrack(source, struct{}{}, DefaultInboundTrackConfig()); !errors.Is(err, ErrUnsupportedOpusDecoder) {
		t.Fatalf("unsupported decoder error = %v, want ErrUnsupportedOpusDecoder", err)
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

func TestInboundTrackRejectsNilPacketEvent(t *testing.T) {
	track := newTestTrackWithSource(t, nilPacketSource{}, &testOpusDecoder{samples: 960}, InboundTrackConfig{})
	if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrInvalidInboundRTPPacket) {
		t.Fatalf("nil packet error = %v, want ErrInvalidInboundRTPPacket", err)
	}
}

func TestInboundTrackOperationErrorIdentity(t *testing.T) {
	sourceFailure := errors.New("source sentinel")
	t.Run("source", func(t *testing.T) {
		track := newTestTrackWithSource(t, &errorPacketSource{err: sourceFailure}, &testOpusDecoder{samples: 960}, InboundTrackConfig{})
		_, err := track.ReadFrame(context.Background())
		var typed *InboundTrackError
		if !errors.Is(err, ErrInboundTrackSource) || !errors.Is(err, sourceFailure) || !errors.As(err, &typed) {
			t.Fatalf("source error = %v, want source identity and InboundTrackError", err)
		}
		_ = err.Error()
		_ = errors.Unwrap(err)
	})

	decodeFailure := errors.New("decode sentinel")
	t.Run("decode", func(t *testing.T) {
		source := newTestPacketSource(1)
		track := newTestTrackWithSource(t, source, &controlledDecoder{decodeErr: decodeFailure}, InboundTrackConfig{})
		source.push(testRTPPacket(1, 1000, 0))
		source.close()
		_, err := track.ReadFrame(context.Background())
		if !errors.Is(err, ErrInboundTrackDecode) || !errors.Is(err, decodeFailure) {
			t.Fatalf("decode error = %v, want decode identity", err)
		}
	})

	frameTrackSource := newTestPacketSource(1)
	frameTrack := newTestTrackWithSource(t, frameTrackSource, &controlledDecoder{samples: 959}, InboundTrackConfig{})
	frameTrackSource.push(testRTPPacket(1, 1000, 0))
	frameTrackSource.close()
	if _, err := frameTrack.ReadFrame(context.Background()); !errors.Is(err, ErrInboundTrackFrame) {
		t.Fatalf("frame error = %v, want ErrInboundTrackFrame", err)
	}

	resampleFailure := errors.New("resample sentinel")
	resampleSource := newTestPacketSource(1)
	resampleTrack := newTestTrackWithSource(t, resampleSource, &controlledDecoder{samples: 960}, InboundTrackConfig{SampleRate: wavio.Rate16kHz, Resample: func([]int16, int, int) ([]int16, error) { return nil, resampleFailure }})
	resampleSource.push(testRTPPacket(1, 1000, 0))
	resampleSource.close()
	if _, err := resampleTrack.ReadFrame(context.Background()); !errors.Is(err, ErrInboundTrackResample) || !errors.Is(err, resampleFailure) {
		t.Fatalf("resample error = %v, want resample identity", err)
	}

	closeFailure := errors.New("close sentinel")
	closeTrack := newTestTrackWithSource(t, &errorCloseSource{err: closeFailure, closed: make(chan struct{})}, &testOpusDecoder{samples: 960}, InboundTrackConfig{})
	if err := closeTrack.Close(); !errors.Is(err, closeFailure) {
		t.Fatalf("close error = %v, want close identity", err)
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
	decoder := &testOpusDecoder{samples: 960}
	track := newTestTrack(t, source, decoder, InboundTrackConfig{JitterDepth: 20 * time.Millisecond})
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
	seen := make(map[byte]bool)
	for _, id := range decoder.decodedIDs() {
		if seen[id] {
			t.Fatalf("concurrent delivery decoded payload %d more than once", id)
		}
		seen[id] = true
	}
	if _, err := track.ReadFrame(context.Background()); !errors.Is(err, ErrInboundTrackClosed) {
		t.Fatalf("concurrent post-close ReadFrame() error = %v, want closed", err)
	}
}

func FuzzInboundTrackIngress(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4})
	f.Add([]byte{0xff, 0x00, 0x80, 0x7f})
	f.Fuzz(func(t *testing.T, data []byte) {
		const maxPackets = 32
		limit := len(data)
		if limit > maxPackets {
			limit = maxPackets
		}
		source := newTestPacketSource(2*maxPackets + 4)
		decoder := &testOpusDecoder{samples: 960}
		timers := &manualTimerFactory{requests: make(chan timerRequest, 8)}
		track, err := NewInboundTrack(source, decoder, InboundTrackConfig{JitterDepth: 20 * time.Millisecond, NewTimer: timers.newTimer})
		if err != nil {
			t.Fatalf("NewInboundTrack() error = %v", err)
		}
		defer track.Close()
		packets := make([]*rtp.Packet, 0, limit)
		baseSequence := uint16(65520)
		baseTimestamp := uint32(4000)
		if len(data) >= 2 {
			baseSequence = binary.LittleEndian.Uint16(data[:2])
		}
		if len(data) >= 6 {
			baseTimestamp = binary.LittleEndian.Uint32(data[2:6])
		}
		if len(data) > 0 && data[0]&0x01 != 0 {
			baseSequence = 65520 + uint16(data[0]&0x0f) //nolint:gosec // intentional wraparound input
		}
		for index := range limit {
			sequence := baseSequence + uint16(index)       //nolint:gosec // bounded fuzz index
			timestamp := baseTimestamp + uint32(index*960) //nolint:gosec // bounded fuzz index
			payload := []byte{byte(index)}                 //nolint:gosec // bounded fuzz index
			if data[index]&0x80 != 0 {
				payload = []byte{data[index], 0}
			}
			if data[index]&0x10 != 0 {
				timestamp++
			}
			packet := testRTPPacket(sequence, timestamp, data[index])
			packet.Payload = payload
			if data[index]&0x40 == 0 {
				packets = append(packets, packet)
			}
			if data[index]&0x20 != 0 {
				packets = append(packets, clonePacket(packet))
			}
		}
		if len(data) > 0 && data[0]&1 != 0 {
			reversePackets(packets)
		}
		if len(data) > 1 && data[1]&1 != 0 && len(packets) > 2 {
			packets[0], packets[1] = packets[1], packets[0]
		}
		if len(data) > 2 && len(packets) > 1 {
			for distance := int(data[2]) % len(packets); distance > 0; distance-- {
				first := packets[0]
				copy(packets, packets[1:])
				packets[len(packets)-1] = first
			}
		}
		for _, packet := range packets {
			source.push(packet)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if len(data) > 0 && data[0]&0x04 != 0 {
			cancel()
			_, readErr := track.ReadFrame(ctx)
			if !errors.Is(readErr, context.Canceled) {
				t.Fatalf("fuzz cancellation error = %v, want context.Canceled", readErr)
			}
			return
		}
		if len(data) > 0 && data[0]&0x08 != 0 {
			if err := track.Close(); err != nil {
				t.Fatalf("fuzz close error = %v", err)
			}
			if _, readErr := track.ReadFrame(context.Background()); !errors.Is(readErr, ErrInboundTrackClosed) {
				t.Fatalf("fuzz post-close error = %v, want ErrInboundTrackClosed", readErr)
			}
			return
		}
		emitted := 0
		if len(packets) > 0 {
			initial := timers.next(t)
			initial.fire()
			frame, readErr := track.ReadFrame(ctx)
			if readErr != nil {
				if !isDocumentedInboundTrackError(readErr) {
					t.Fatalf("fuzz initial ReadFrame() error = %v", readErr)
				}
				return
			}
			if len(frame.Samples) != 960 {
				t.Fatalf("fuzz frame length = %d, want 960", len(frame.Samples))
			}
			emitted++
			late := testRTPPacket(baseSequence-1, baseTimestamp-960, 0xfe)
			late.Payload = []byte{0xfe}
			source.push(late)
		}
		source.close()
		for {
			frame, readErr := track.ReadFrame(ctx)
			if readErr != nil {
				if !isDocumentedInboundTrackError(readErr) {
					t.Fatalf("fuzz ReadFrame() error = %v", readErr)
				}
				break
			}
			if len(frame.Samples) != 960 {
				t.Fatalf("fuzz frame length = %d, want 960", len(frame.Samples))
			}
			emitted++
		}
		seen := make(map[byte]struct{}, len(decoder.decodedIDs()))
		for _, id := range decoder.decodedIDs() {
			if _, duplicate := seen[id]; duplicate {
				t.Fatalf("fuzz decoded payload %d more than once", id)
			}
			seen[id] = struct{}{}
		}
	})
}

func isDocumentedInboundTrackError(err error) bool {
	var typed *InboundTrackError
	return errors.As(err, &typed) || errors.Is(err, ErrInboundTrackClosed) || errors.Is(err, io.EOF)
}

const inboundTrackAllocationBudget = 9

var inboundTrackAllocationSink int16

func TestInboundTrackS10AllocationBudget(t *testing.T) {
	source := newTestPacketSource(2)
	track := newTestTrack(t, source, &testOpusDecoder{samples: 960}, InboundTrackConfig{JitterDepth: 20 * time.Millisecond})
	defer track.Close()
	var sequence uint16 = 10
	timestamp := uint32(1000)
	source.push(testRTPPacket(sequence, timestamp, 1))
	readTestFrame(t, track)
	measured := testing.AllocsPerRun(100, func() {
		sequence++
		timestamp += 960
		source.push(testRTPPacket(sequence, timestamp, 1))
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
	timestamp := uint32(1000)
	source.push(testRTPPacket(sequence, timestamp, 1))
	readTestFrame(b, track)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sequence++
		timestamp += 960
		source.push(testRTPPacket(sequence, timestamp, 1))
		frame := readTestFrame(b, track)
		inboundTrackAllocationSink ^= frame.Samples[0]
	}
}

type testPacketSource struct {
	packets chan *rtp.Packet
	closed  chan struct{}
	once    sync.Once
}

type errorPacketSource struct{ err error }

func (s *errorPacketSource) ReadRTP() (*rtp.Packet, error) { return nil, s.err }

type nilPacketSource struct{}

func (nilPacketSource) ReadRTP() (*rtp.Packet, error) { return nil, nil }

type errorCloseSource struct {
	err    error
	closed chan struct{}
	once   sync.Once
}

func (s *errorCloseSource) ReadRTP() (*rtp.Packet, error) { <-s.closed; return nil, io.EOF }
func (s *errorCloseSource) Close() error                  { s.once.Do(func() { close(s.closed) }); return s.err }

type controlledDecoder struct {
	decodeErr error
	samples   int
}

func (d *controlledDecoder) Decode([]byte) ([]int16, error) {
	if d.decodeErr != nil {
		return nil, d.decodeErr
	}
	return make([]int16, d.samples), nil
}
func (d *controlledDecoder) DecodePLC() ([]int16, error) { return make([]int16, d.samples), nil }

type timerRequest struct {
	duration time.Duration
	channel  chan time.Time
}
type manualTimerFactory struct{ requests chan timerRequest }

func (f *manualTimerFactory) newTimer(duration time.Duration) <-chan time.Time {
	channel := make(chan time.Time, 1)
	f.requests <- timerRequest{duration, channel}
	return channel
}
func (f *manualTimerFactory) next(t testing.TB) timerRequest { t.Helper(); return <-f.requests }
func (r timerRequest) fire()                                 { r.channel <- time.Unix(0, 0) }

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
func (s *testPacketSource) Close() error { s.close(); return nil }

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

func newTestTrack(t testing.TB, source *testPacketSource, decoder OpusDecoder, config InboundTrackConfig) *InboundTrack {
	return newTestTrackWithSource(t, source, decoder, config)
}

func newTestTrackWithSource(t testing.TB, source any, decoder OpusDecoder, config InboundTrackConfig) *InboundTrack {
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
