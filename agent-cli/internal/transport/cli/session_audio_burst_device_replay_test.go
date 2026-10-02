package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	openAIAudioBurstFixture       = "testdata/openai-audio-burst.base64"
	openAIFullProviderPacketBytes = 19_200
	openAIAudioBurstPacketCount   = 25
	openAIAudioBurstBytes         = 468_000
	openAIAudioBurstSeedSHA256    = "4178a70df57de1e70701ceb83553f611ebf041c7b501f1ff9d30d2e0af5a68ad"
	openAIAudioBurstSHA256        = "b00851d38de650d2a12ebd7b2bec4b4aef6de7a7ca0457962c3729d2b0914808"

	openAILongAudioFixture        = "testdata/openai-long-audio-output.base64"
	openAILongAudioSeedPackets    = 4
	openAILongAudioPacketCount    = 127
	openAILongAudioFinalPacketLen = 12_000
	openAILongAudioBytes          = 2_431_200
	openAILongAudioSeedSHA256     = "2660b60334df1c3ea7c6d2c8419e5c861eeb9d8a54187d12878e715ec1d1bc11"

	// openAIReplayHangBound bounds a hang only: nothing asserts elapsed time.
	// The long replay is CPU-bound JSON/base64 decoding of ~50 s of 24 kHz
	// audio, ~20 s end to end under -race at load average ~90.
	openAIReplayHangBound = 2 * time.Minute
)

// TestSessionReplayDeliversOpenAIAudioSampleExactToDevice replays captured
// OpenAI response audio through the shipped session command to the 16 kHz
// virtual output device. Both responses arrive much faster than playback:
// the burst is 24 400 ms packets plus a 150 ms tail delivered in about two
// seconds (9.75 s of audio), and the long response is 126 400 ms packets plus
// a 250 ms tail (50.65 s). Exact loopback equality and zero drops, overflows
// or retained samples prove conversion and device pacing lose, duplicate and
// reorder nothing at the provider-to-device boundary.
func TestSessionReplayDeliversOpenAIAudioSampleExactToDevice(t *testing.T) {
	t.Run("burst", func(t *testing.T) {
		deltas, providerPCM := loadOpenAIAudioBurstPackets(t)
		runCapturedOpenAIAudioToVirtualDevice(t, "burst", deltas, providerPCM)
	})
	t.Run("long_response", func(t *testing.T) {
		deltas, providerPCM := loadOpenAILongAudioPackets(t)
		runCapturedOpenAIAudioToVirtualDevice(t, "long-response", deltas, providerPCM)
	})
}

func runCapturedOpenAIAudioToVirtualDevice(t *testing.T, fixtureName string, deltas []string, providerPCM []byte) {
	t.Helper()

	providerSamples := pcm16Bytes(t, providerPCM)
	// The device sink owns one continuous playback processor. Build the
	// independent reference over the same packet boundaries so the streaming
	// resampler's phase/filter history and final tail are represented exactly.
	providerChunks := make([][]int16, 0, len(deltas))
	for _, delta := range deltas {
		packet, decodeErr := base64.StdEncoding.DecodeString(delta)
		if decodeErr != nil {
			t.Fatalf("decode %s provider packet: %v", fixtureName, decodeErr)
		}
		providerChunks = append(providerChunks, pcm16Bytes(t, packet))
	}
	want := mustResampleProviderToDevice(t, providerChunks)

	capturePath := filepath.Join(t.TempDir(), fixtureName+"-audio-device.session.json")
	writeOpenAIAudioBurstCapture(t, capturePath, fixtureName, deltas)

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
	command := newTestSessionCommand(flags.NewAskFlags(), globalFlags, testSessionDeps{Registry: registry}).Generate()
	command.SetOut(io.Discard)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{"--replay", capturePath, "--audio-out-device", "virtual:output"})

	ctx, cancel := context.WithTimeout(context.Background(), openAIReplayHangBound)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	got := make([]int16, 0, len(want))
	for len(got) < len(want) {
		batch := make([]int16, min(audio.FrameSize, len(want)-len(got)))
		if err := observer.ReadSamples(ctx, batch); err != nil {
			t.Fatalf("read %s loopback at sample %d; stderr=%q: %v", fixtureName, len(got), stderr.String(), err)
		}
		got = append(got, batch...)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("execute %s OpenAI edge replay: %v; stderr=%q", fixtureName, err, stderr.String())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s device PCM differs: got %d samples, want %d exact samples", fixtureName, len(got), len(want))
	}
	stats := observer.PlaybackStats()
	if stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("%s device lost or retained samples: %+v", fixtureName, stats)
	}
	t.Logf("%s preserved %d provider samples as %d exact 16 kHz device samples; playback=%+v", fixtureName, len(providerSamples), len(got), stats)
}

func loadOpenAIAudioBurstPackets(t *testing.T) ([]string, []byte) {
	t.Helper()

	file, err := os.Open(openAIAudioBurstFixture)
	if err != nil {
		t.Fatalf("open OpenAI burst audio fixture: %v", err)
	}
	defer closeForTest(t, file.Close)

	var seeds [][]byte
	var encoded strings.Builder
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "--packet--":
			packet, decodeErr := base64.StdEncoding.DecodeString(encoded.String())
			if decodeErr != nil {
				t.Fatalf("decode OpenAI burst packet %d: %v", len(seeds), decodeErr)
			}
			seeds = append(seeds, packet)
			encoded.Reset()
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		default:
			encoded.WriteString(line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan OpenAI burst audio fixture: %v", err)
	}
	if encoded.Len() != 0 {
		t.Fatal("OpenAI burst fixture ended without a packet delimiter")
	}
	if len(seeds) != 5 {
		t.Fatalf("OpenAI burst seed packets = %d, want four full packets and one tail", len(seeds))
	}
	for index, packet := range seeds {
		wantBytes := openAIFullProviderPacketBytes
		if index == len(seeds)-1 {
			wantBytes = 7_200
		}
		if len(packet) != wantBytes {
			t.Fatalf("OpenAI burst packet %d bytes = %d, want %d", index, len(packet), wantBytes)
		}
	}
	seedPCM := bytes.Join(seeds[:4], nil)
	if got := fmt.Sprintf("%x", sha256.Sum256(seedPCM)); got != openAIAudioBurstSeedSHA256 {
		t.Fatalf("OpenAI burst seed SHA-256 = %s, want %s", got, openAIAudioBurstSeedSHA256)
	}

	deltas := make([]string, 0, openAIAudioBurstPacketCount)
	providerPCM := make([]byte, 0, openAIAudioBurstBytes)
	for index := range openAIAudioBurstPacketCount - 1 {
		packet := seeds[index%4]
		deltas = append(deltas, base64.StdEncoding.EncodeToString(packet))
		providerPCM = append(providerPCM, packet...)
	}
	deltas = append(deltas, base64.StdEncoding.EncodeToString(seeds[4]))
	providerPCM = append(providerPCM, seeds[4]...)
	if len(providerPCM) != openAIAudioBurstBytes {
		t.Fatalf("OpenAI burst reconstructed provider PCM bytes = %d, want %d", len(providerPCM), openAIAudioBurstBytes)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(providerPCM)); got != openAIAudioBurstSHA256 {
		t.Fatalf("OpenAI burst reconstructed provider SHA-256 = %s, want %s", got, openAIAudioBurstSHA256)
	}
	return deltas, providerPCM
}

// loadOpenAILongAudioPackets cycles four consecutive captured 400 ms packets
// into the 127-packet long response, keeping real provider PCM and packet
// sizing while the committed fixture stays compact.
func loadOpenAILongAudioPackets(t *testing.T) ([]string, []byte) {
	t.Helper()
	file, err := os.Open(openAILongAudioFixture)
	if err != nil {
		t.Fatalf("open OpenAI long audio fixture: %v", err)
	}
	defer closeForTest(t, file.Close)

	var seeds [][]byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		encoded := strings.TrimSpace(scanner.Text())
		if encoded == "" || strings.HasPrefix(encoded, "#") {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode OpenAI long audio seed %d: %v", len(seeds), err)
		}
		if len(decoded) != openAIFullProviderPacketBytes {
			t.Fatalf("OpenAI long audio seed %d bytes = %d, want %d", len(seeds), len(decoded), openAIFullProviderPacketBytes)
		}
		seeds = append(seeds, decoded)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan OpenAI long audio fixture: %v", err)
	}
	if len(seeds) != openAILongAudioSeedPackets {
		t.Fatalf("OpenAI long audio seeds = %d, want %d", len(seeds), openAILongAudioSeedPackets)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(bytes.Join(seeds, nil))); got != openAILongAudioSeedSHA256 {
		t.Fatalf("OpenAI long audio seed SHA-256 = %s, want %s", got, openAILongAudioSeedSHA256)
	}

	deltas := make([]string, 0, openAILongAudioPacketCount)
	providerPCM := make([]byte, 0, openAILongAudioBytes)
	for index := range openAILongAudioPacketCount {
		packet := seeds[index%len(seeds)]
		if index == openAILongAudioPacketCount-1 {
			packet = packet[:openAILongAudioFinalPacketLen]
		}
		deltas = append(deltas, base64.StdEncoding.EncodeToString(packet))
		providerPCM = append(providerPCM, packet...)
	}
	if len(providerPCM) != openAILongAudioBytes {
		t.Fatalf("OpenAI long audio PCM bytes = %d, want %d", len(providerPCM), openAILongAudioBytes)
	}
	return deltas, providerPCM
}

func pcm16Bytes(t *testing.T, pcm []byte) []int16 {
	t.Helper()
	if len(pcm)%2 != 0 {
		t.Fatalf("PCM byte count = %d, want even", len(pcm))
	}
	return codec.PCM16Samples(pcm)
}

func writeOpenAIAudioBurstCapture(t *testing.T, path, fixtureName string, deltas []string) {
	t.Helper()
	sequence := 0
	var records []gwtesting.CapturedSessionEvent
	add := func(direction gwtesting.SessionEventDirection, payload any) {
		sequence++
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s replay event %d: %v", fixtureName, sequence, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("decode %s replay event type %d: %v", fixtureName, sequence, err)
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: int64(sequence), Type: envelope.Type,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: data,
		})
	}

	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "session.update", "session": map[string]any{
			"model": "gpt-realtime-2.1",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "session.created", "session": map[string]any{
			"id": "sess-" + fixtureName, "type": "realtime", "model": "gpt-realtime-2.1",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-" + fixtureName, "status": "in_progress"},
	})
	for _, delta := range deltas {
		add(gwtesting.DirectionServerToClient, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-" + fixtureName, "item_id": "item-" + fixtureName,
			"output_index": 0, "content_index": 0, "delta": delta,
		})
	}
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-" + fixtureName, "item_id": "item-" + fixtureName,
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{"id": "resp-" + fixtureName, "status": "completed"},
	})

	capture, err := gwtesting.SealSessionCapture(gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1"},
		Session:  gwtesting.SessionMetadata{ID: "sess-" + fixtureName, StartedAtUTC: "2026-09-02T00:00:00Z"},
		Records:  records,
	})
	if err != nil {
		t.Fatalf("seal %s replay capture: %v", fixtureName, err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal %s replay capture: %v", fixtureName, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s replay capture: %v", fixtureName, err)
	}
}

// playbackBurstExtraFrames makes the burst comfortably exceed the local
// playback queue, so only backpressure (not queue headroom) avoids loss.
const playbackBurstExtraFrames = 20

// TestSessionCommandBackpressuresPlaybackBurstWithoutOverflow delivers more
// assistant audio than the device playback queue holds while the device
// callback is delayed. The live session must park the burst at the queue's
// high watermark and resume as the callback drains it: every sample reaches
// the device in order and the queue never uses its drop-oldest overflow.
func TestSessionCommandBackpressuresPlaybackBurstWithoutOverflow(t *testing.T) {
	format := audio.PCM16DeviceFormat(audio.SampleRate)
	capacity, err := audio.PlaybackQueueCapacity(format, audio.DefaultPlaybackLatencyTarget)
	if err != nil {
		t.Fatalf("compute playback queue capacity: %v", err)
	}
	frames := playbackBurstFrames(capacity/audio.FrameSize + playbackBurstExtraFrames)
	// The realtime provider speaks 24 kHz; the device sink owns one continuous
	// resampler, so the exact device reference is the streamed conversion.
	want := mustResampleProviderToDevice(t, frames)

	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	opened, err := registry.OpenWithFormat("virtual:input", format)
	if err != nil {
		t.Fatalf("open loopback observer: %v", err)
	}
	observer, ok := opened.(*devicegw.VirtualStream)
	if !ok {
		t.Fatalf("loopback observer = %T, want *devices.VirtualStream", opened)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Errorf("close loopback observer: %v", err)
		}
	})

	inferencer := &playbackBurstInferencer{frames: frames, connected: make(chan *playbackBurstSession, 1), closed: make(chan struct{})}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, inferencer, registry).Generate()
	command.SetOut(io.Discard)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--provider", config.ProviderOpenAI, "--model", "gpt-realtime", "--api-key", "test-key",
		"--prompt", "hello", "--audio-out-device", "virtual:output",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	// Hold the device callback until the whole burst was handed to the
	// session and the device queue parked at its high watermark.
	var session *playbackBurstSession
	select {
	case session = <-inferencer.connected:
	case <-ctx.Done():
		t.Fatal("provider session was not connected")
	}
	select {
	case <-session.inbound.drained:
	case <-ctx.Done():
		t.Fatalf("burst was not admitted; stderr=%q", stderr.String())
	}
	waitForPlaybackHighWatermark(t, ctx, observer)

	got := make([]int16, 0, len(want))
	for len(got) < len(want) {
		batch := make([]int16, min(audio.FrameSize, len(want)-len(got)))
		if err := observer.ReadSamples(ctx, batch); err != nil {
			t.Fatalf("read device playback at sample %d of %d (stats %+v); stderr=%q: %v", len(got), len(want), observer.PlaybackStats(), stderr.String(), err)
		}
		got = append(got, batch...)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("session command: %v; stderr=%q", err, stderr.String())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("device playback differs from the provider burst: got %d samples, want %d exact samples", len(got), len(want))
	}
	if stats := observer.PlaybackStats(); stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("paced playback burst lost or retained samples: %+v", stats)
	}
	select {
	case <-inferencer.closed:
	case <-time.After(time.Second):
		t.Fatal("provider session did not close after the command returned")
	}
}

// waitForPlaybackHighWatermark waits until the delayed device queue holds
// buffered audio and stops growing: the producer is parked below capacity
// instead of overflowing the queue.
func waitForPlaybackHighWatermark(t *testing.T, ctx context.Context, observer *devicegw.VirtualStream) {
	t.Helper()
	const settle = 30 * time.Millisecond
	last := -1
	for {
		stats := observer.PlaybackStats()
		if stats.QueuedSamples > 0 && stats.QueuedSamples == last {
			if stats.QueuedSamples >= stats.CapacitySamples || stats.OverflowEvents != 0 || stats.DroppedSamples != 0 {
				t.Fatalf("burst overran the playback queue instead of parking: %+v", stats)
			}
			return
		}
		last = stats.QueuedSamples
		select {
		case <-ctx.Done():
			t.Fatalf("playback queue never settled at its high watermark: %+v", stats)
		case <-time.After(settle):
		}
	}
}

func playbackBurstFrames(count int) [][]int16 {
	frames := make([][]int16, count)
	for frameIndex := range frames {
		frame := make([]int16, audio.FrameSize)
		for sampleIndex := range frame {
			frame[sampleIndex] = int16((frameIndex*audio.FrameSize+sampleIndex)%20000 - 10000)
		}
		frames[frameIndex] = frame
	}
	return frames
}

// playbackBurstInferencer streams one assistant response whose audio is
// exposed as a burst of frames on the provider media endpoint.
type playbackBurstInferencer struct {
	frames    [][]int16
	connected chan *playbackBurstSession
	closed    chan struct{}
}

func (i *playbackBurstInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &playbackBurstSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
		closed:  i.closed,
		inbound: &playbackBurstInbound{frames: i.frames, drained: make(chan struct{})},
	}
	if !session.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("playback-burst", "test")}) {
		return nil, ctx.Err()
	}
	i.connected <- session
	return session, nil
}

type playbackBurstSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closed    chan struct{}
	inbound   *playbackBurstInbound
	audioOnce sync.Once
	closeOnce sync.Once
}

func (s *playbackBurstSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	default:
	}
	if msg.Type == messages.StreamTypeSessionClose {
		// Acknowledge the close only after the media path took every frame,
		// so the assertion never races pump cancellation.
		select {
		case <-s.inbound.drained:
		case <-ctx.Done():
			return false
		}
		s.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("playback-burst", "test complete")})
		return s.Close() == nil
	}
	s.audioOnce.Do(func() { s.startBurst(ctx) })
	return true
}

// startBurst opens the response and ends it once the media path took every
// frame; the terminal outlives the triggering send.
func (s *playbackBurstSession) startBurst(ctx context.Context) {
	detached := context.WithoutCancel(ctx)
	for _, message := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "burst", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "burst", Value: messages.NewAudioDeltaValue(codec.EncodePCM16(s.inbound.frames[0]))},
	} {
		s.receive.Write(ctx, message)
	}
	go func() {
		select {
		case <-s.inbound.drained:
		case <-s.done:
			return
		}
		s.receive.Write(detached, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "burst", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}()
}

func (s *playbackBurstSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *playbackBurstSession) Done() <-chan struct{} { return s.done }

func (s *playbackBurstSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		close(s.closed)
	})
	return nil
}

func (s *playbackBurstSession) RTCMedia() servicetest.RTCMediaEndpoints {
	return servicetest.RTCMediaEndpoints{Inbound: s.inbound}
}

// playbackBurstInbound hands out one frame per read with no pacing, as a
// provider burst does.
type playbackBurstInbound struct {
	frames      [][]int16
	next        atomic.Int32
	drained     chan struct{}
	drainedOnce sync.Once
}

func (m *playbackBurstInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	if err := ctx.Err(); err != nil {
		return audio.PCMFrame{}, err
	}
	index := int(m.next.Add(1)) - 1
	if index >= len(m.frames) {
		m.drainedOnce.Do(func() { close(m.drained) })
		return audio.PCMFrame{}, io.EOF
	}
	return audio.PCMFrame{Samples: append([]int16(nil), m.frames[index]...)}, nil
}

func (*playbackBurstInbound) Close() error { return nil }

var (
	_ messages.SessionInferencer  = (*playbackBurstInferencer)(nil)
	_ servicetest.RTCMediaSession = (*playbackBurstSession)(nil)
	_ audio.InboundMedia          = (*playbackBurstInbound)(nil)
)
