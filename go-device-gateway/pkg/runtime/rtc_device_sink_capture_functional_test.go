package runtime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	crackleCapturePCMFixture = "testdata/rtc-device-crackle-first-turn.pcm.gz.b64"
	crackleCapturePCMBytes   = 232800
	crackleCaptureSHA256     = "71b64581388d5e053f26a5dc0abe0e980969149113c797fe16e62f09c2ac2f71"
	crackleCaptureFullChunks = 12
	crackleCaptureChunkBytes = 19200
)

// TestRTCDeviceSinkCapturedFirstTurnPreservesPacedVirtualPlayback replays the
// first assistant audio turn from test7.json through the real RTC-to-device
// conversion boundary and a 16 kHz virtual loopback device. The provider turn
// is much larger than the hard queue capacity, so preserving every converted
// sample proves producer backpressure prevents both overflow loss and a
// drain-to-zero write cadence.
func TestRTCDeviceSinkCapturedFirstTurnPreservesPacedVirtualPlayback(t *testing.T) {
	providerSamples := loadCrackleCaptureFirstTurn(t)
	providerFrames := capturedFirstTurnFrames(t, providerSamples)
	reference, err := wavio.NewPCM16Resampler(wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	expectedDeviceSamples, err := reference.Process(providerSamples, true)
	if err != nil {
		t.Fatalf("resample captured provider turn: %v", err)
	}

	registry := newRTCDeviceSinkRateRegistry(t, audio.SampleRate)
	openedObserver, err := registry.OpenWithFormat("virtual:input", audio.PCM16DeviceFormat(audio.SampleRate))
	if err != nil {
		t.Fatalf("open 16 kHz loopback observer: %v", err)
	}
	observer, ok := openedObserver.(*devicegw.VirtualStream)
	if !ok {
		t.Fatalf("loopback observer = %T, want *audio.VirtualStream", openedObserver)
	}
	defer func() { _ = observer.Close() }()

	sink, err := NewRTCDeviceSinkAtRate(registry, "virtual:output", wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("open 24 kHz provider -> 16 kHz device sink: %v", err)
	}
	defer func() { _ = sink.Close() }()

	pumpErr := make(chan error, 1)
	go func() {
		pumpErr <- sink.Pump(context.Background(), &recordingRTCInboundMedia{frames: providerFrames})
	}()

	low, high, err := audio.PlaybackQueueWatermarks(audio.PCM16DeviceFormat(audio.SampleRate))
	if err != nil {
		t.Fatalf("resolve playback watermarks: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for sink.PlaybackStats().QueuedSamples < high && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := sink.PlaybackStats().QueuedSamples; got != high {
		t.Fatalf("provider burst queued %d samples before pacing, want high watermark %d", got, high)
	}
	select {
	case err := <-pumpErr:
		t.Fatalf("provider burst completed before the virtual device consumed audio: %v", err)
	default:
	}

	if got, want := len(expectedDeviceSamples), 77600; got != want {
		t.Fatalf("duration-equivalent device sample count = %d, want %d", got, want)
	}

	// The observer is the virtual device clock. Each read advances one native
	// callback-sized frame; the exact final remainder models the shorter last
	// callback payload without inventing or discarding PCM samples.
	completeFrames := len(expectedDeviceSamples) / audio.FrameSize
	observed := make([]int16, 0, len(expectedDeviceSamples))
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for index := 0; index < completeFrames; index++ {
		frame := make([]int16, audio.FrameSize)
		if err := observer.ReadFrame(readCtx, frame); err != nil {
			t.Fatalf("read retained loopback frame %d: %v", index, err)
		}
		observed = append(observed, frame...)
	}
	if remainder := len(expectedDeviceSamples) - len(observed); remainder > 0 {
		final := make([]int16, remainder)
		if err := observer.ReadSamples(readCtx, final); err != nil {
			t.Fatalf("read final loopback remainder: %v", err)
		}
		observed = append(observed, final...)
	}
	if err := <-pumpErr; err != nil {
		t.Fatalf("replay captured first audio turn: %v", err)
	}
	if !reflect.DeepEqual(observed, expectedDeviceSamples) {
		t.Fatal("paced loopback audio differs from the complete duration-equivalent conversion")
	}
	stats := sink.PlaybackStats()
	if stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("paced captured-turn playback stats = %+v, want no drops, overflow, or queued tail", stats)
	}
	if stats.PeakQueuedSamples > high || stats.PeakQueuedSamples <= low {
		t.Fatalf("paced peak queue = %d, want (%d, %d]", stats.PeakQueuedSamples, low, high)
	}
	t.Logf("preserved captured turn: provider=%d samples at 24 kHz, played=%d samples at 16 kHz, peak_queue=%d, dropped=0",
		len(providerSamples), len(observed), stats.PeakQueuedSamples)
}

func loadCrackleCaptureFirstTurn(t *testing.T) []int16 {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(crackleCapturePCMFixture))
	if err != nil {
		t.Fatalf("read captured first-turn fixture: %v", err)
	}
	compressed, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, strings.NewReader(string(encoded))))
	if err != nil {
		t.Fatalf("decode captured first-turn fixture: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("open captured first-turn fixture: %v", err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompress captured first-turn fixture: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close captured first-turn fixture: %v", err)
	}
	if len(raw) != crackleCapturePCMBytes {
		t.Fatalf("captured first-turn PCM bytes = %d, want %d", len(raw), crackleCapturePCMBytes)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != crackleCaptureSHA256 {
		t.Fatalf("captured first-turn SHA-256 = %s, want %s", got, crackleCaptureSHA256)
	}
	samples := make([]int16, len(raw)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(raw[index*2:]))
	}
	return samples
}

func capturedFirstTurnFrames(t *testing.T, samples []int16) []audio.PCMFrame {
	t.Helper()
	chunkSamples := crackleCaptureChunkBytes / 2
	wantSamples := crackleCaptureFullChunks*chunkSamples + 1200
	if len(samples) != wantSamples {
		t.Fatalf("captured first-turn samples = %d, want %d", len(samples), wantSamples)
	}
	frames := make([]audio.PCMFrame, 0, crackleCaptureFullChunks+1)
	for offset := 0; offset < len(samples); offset += chunkSamples {
		end := min(offset+chunkSamples, len(samples))
		frames = append(frames, audio.PCMFrame{
			Samples:       append([]int16(nil), samples[offset:end]...),
			EndOfResponse: end == len(samples),
		})
	}
	return frames
}
