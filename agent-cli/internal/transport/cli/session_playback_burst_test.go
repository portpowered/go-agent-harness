package cli

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

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
	want := mustResampleStream(t, frames, wavio.Rate24kHz, audio.SampleRate)

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
	defer func() { _ = observer.Close() }()

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
	if !equalPCM16(got, want) {
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
		s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("playback-burst", "test complete")})
		return s.Close() == nil
	}
	s.audioOnce.Do(func() {
		for _, message := range []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "burst", Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "burst", Value: messages.NewAudioDeltaValue(cliPCM16Bytes(s.inbound.frames[0]))},
		} {
			s.receive.Write(context.Background(), message)
		}
		go func() {
			select {
			case <-s.inbound.drained:
			case <-s.done:
				return
			}
			s.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "burst", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
		}()
	})
	return true
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
