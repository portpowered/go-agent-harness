package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	test7OpenAILongOutputFixture = "testdata/test7-openai-long-output.base64"
	test7SeedChunks              = 4
	test7OutputChunks            = 127
	test7FullChunkBytes          = 19200
	test7FinalChunkBytes         = 12000
	test7ProviderBytes           = 2431200
	test7SeedSHA256              = "2660b60334df1c3ea7c6d2c8419e5c861eeb9d8a54187d12878e715ec1d1bc11"
)

// TestSessionCommandReplaysTest7LongOpenAIAudioTo16kLoopback exercises the
// longest response shape captured in test7.json at the session/device edges.
// The real response contained 126 full 400 ms deltas plus a 250 ms tail: 50.65
// seconds of PCM16/24 kHz audio delivered much faster than playback. Cycling
// four consecutive captured deltas keeps the fixture compact while preserving
// real provider PCM, provider chunk sizing, total duration, and burst pressure.
// Exact 16 kHz loopback equality proves that conversion and device pacing lose,
// duplicate, and reorder zero samples across the long response.
const test7ReplayHangBound = 2 * time.Minute

func TestSessionCommandReplaysTest7LongOpenAIAudioTo16kLoopback(t *testing.T) {
	deltas, providerPCM := loadTest7LongOpenAIAudio(t)
	providerSamples := decodeTest7PCM16(t, providerPCM)
	providerChunks := make([][]int16, 0, len(deltas))
	for index, delta := range deltas {
		chunk, err := base64.StdEncoding.DecodeString(delta)
		if err != nil {
			t.Fatalf("decode test7 delta %d for reference: %v", index, err)
		}
		providerChunks = append(providerChunks, decodeTest7PCM16(t, chunk))
	}
	want := mustResampleProviderToDevice(t, providerChunks)

	capturePath := filepath.Join(t.TempDir(), "test7-long-openai-edge-replay.session.json")
	writeTest7LongOpenAICapture(t, capturePath, deltas)

	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	openedObserver, err := registry.OpenWithFormat("virtual:input", audio.PCM16DeviceFormat(audio.SampleRate))
	if err != nil {
		t.Fatalf("open 16 kHz loopback observer: %v", err)
	}
	observer, ok := openedObserver.(*devicegw.VirtualStream)
	if !ok {
		t.Fatalf("loopback observer = %T, want *audio.VirtualStream", openedObserver)
	}
	defer closeForTest(t, observer.Close)

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := newTestReplaySessionCommand(globalFlags, registry).Generate()
	command.SetOut(io.Discard)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{"--replay", capturePath, "--audio-out-device", "virtual:output"})

	// Hang bound only: nothing below asserts elapsed time. The replay is
	// CPU-bound JSON/base64 decoding of ~50s of 24 kHz provider audio; under
	// -race it took ~20s end to end at load average ~90 (4s without -race), so
	// the former 15s bound failed every race run on a busy host.
	ctx, cancel := context.WithTimeout(context.Background(), test7ReplayHangBound)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	got := make([]int16, 0, len(want))
	for len(got) < len(want) {
		batch := make([]int16, min(audio.FrameSize, len(want)-len(got)))
		if err := observer.ReadSamples(ctx, batch); err != nil {
			t.Fatalf("read test7 loopback at sample %d/%d; context=%v playback=%+v stderr=%q: %v", len(got), len(want), context.Cause(ctx), observer.PlaybackStats(), stderr.String(), err)
		}
		got = append(got, batch...)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("execute test7 OpenAI edge replay: %v; stderr=%q", err, stderr.String())
	}
	if !equalPCM16(got, want) {
		t.Fatalf("test7 loopback PCM differs: got %d samples, want %d exact samples", len(got), len(want))
	}
	stats := observer.PlaybackStats()
	if stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("test7 device lost or retained samples: %+v", stats)
	}
	t.Logf("test7 long replay preserved %d provider samples as %d exact 16 kHz device samples; playback=%+v", len(providerSamples), len(got), stats)
}

func decodeTest7PCM16(t *testing.T, pcm []byte) []int16 {
	t.Helper()
	if len(pcm)%2 != 0 {
		t.Fatalf("test7 OpenAI PCM has odd byte count %d", len(pcm))
	}
	return codec.PCM16Samples(pcm)
}

func loadTest7LongOpenAIAudio(t *testing.T) ([]string, []byte) {
	t.Helper()
	file, err := os.Open(test7OpenAILongOutputFixture)
	if err != nil {
		t.Fatalf("open test7 OpenAI audio fixture: %v", err)
	}
	defer closeForTest(t, file.Close)

	var seeds [][]byte
	var seedPCM []byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		encoded := strings.TrimSpace(scanner.Text())
		if encoded == "" || strings.HasPrefix(encoded, "#") {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode test7 OpenAI seed delta %d: %v", len(seeds), err)
		}
		if len(decoded) != test7FullChunkBytes {
			t.Fatalf("test7 OpenAI seed delta %d bytes = %d, want %d", len(seeds), len(decoded), test7FullChunkBytes)
		}
		seeds = append(seeds, decoded)
		seedPCM = append(seedPCM, decoded...)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan test7 OpenAI audio fixture: %v", err)
	}
	if len(seeds) != test7SeedChunks {
		t.Fatalf("test7 OpenAI seed deltas = %d, want %d", len(seeds), test7SeedChunks)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(seedPCM)); got != test7SeedSHA256 {
		t.Fatalf("test7 OpenAI seed PCM SHA-256 = %s, want %s", got, test7SeedSHA256)
	}

	deltas := make([]string, 0, test7OutputChunks)
	providerPCM := make([]byte, 0, test7ProviderBytes)
	for index := range test7OutputChunks {
		chunk := seeds[index%len(seeds)]
		if index == test7OutputChunks-1 {
			chunk = chunk[:test7FinalChunkBytes]
		}
		deltas = append(deltas, base64.StdEncoding.EncodeToString(chunk))
		providerPCM = append(providerPCM, chunk...)
	}
	if len(providerPCM) != test7ProviderBytes {
		t.Fatalf("test7 synthesized provider PCM bytes = %d, want %d", len(providerPCM), test7ProviderBytes)
	}
	return deltas, providerPCM
}

func writeTest7LongOpenAICapture(t *testing.T, path string, deltas []string) {
	t.Helper()
	writeOpenAIAudioReplayCapture(t, path, openAIAudioReplayCapture{
		label: "test7", model: "gpt-realtime-2.1", sessionID: "sess-test7-long", responseID: "resp-test7-long",
		itemID: "item-test7-long", prompt: "replay test7 long audio", startedAtUTC: "2026-09-02T19:41:55.000000Z",
	}, deltas)
}
