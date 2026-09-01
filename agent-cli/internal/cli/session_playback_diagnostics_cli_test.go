package cli

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// playbackOverflowSessionReceiveHeadroom covers the small number of non-audio
// control messages (SESSION.OPEN, MESSAGE.END, the SESSION.CLOSE ack) queued
// alongside the audio deltas, on top of the exact frame count.
const playbackOverflowSessionReceiveHeadroom = 8

// TestSessionCommandSurfacesPlaybackOverflowDiagnostic is a regression test
// for the CLI wiring gap found while investigating intermittent assistant
// audio cutoff: agent-cli/internal/audio/device_playback.go accumulates
// DroppedSamples/OverflowEvents on every local playback queue correctly, and
// agent-cli/internal/services/session_playback_diagnostics.go can format them
// into a SessionDiagnosticEventPlaybackOverflow record, but the `session`
// command never populated services.SessionRunOptions.Diagnostics, so a real
// operator running `agent session` had no way to observe these counters --
// not on stdout, not on stderr, not in any recording artifact.
//
// This test delivers many device-frame-sized assistant audio chunks to a
// virtual output device that nothing ever drains, which deterministically
// overflows the bounded playback queue (see PlaybackQueueCapacity), and
// asserts the overflow is reported on the command's stderr. Before the
// Diagnostics wiring fix in session.go, this assertion fails even though the
// queue really did drop samples, because sessionPlaybackDiagnosticObserver
// was never handed a sink.
func TestSessionCommandSurfacesPlaybackOverflowDiagnostic(t *testing.T) {
	capacity, err := audio.PlaybackQueueCapacity(audio.DefaultDeviceFormat(), audio.DefaultPlaybackLatencyTarget)
	if err != nil {
		t.Fatalf("compute playback queue capacity: %v", err)
	}

	// Comfortably more frames than fit in the queue so the overflow is
	// deterministic, independent of any device-callback timing.
	frameCount := capacity/audio.FrameSize + 20
	totalSamples := frameCount * audio.FrameSize
	frames := make([][]int16, frameCount)
	for frameIndex := range frames {
		frame := make([]int16, audio.FrameSize)
		for sampleIndex := range frame {
			frame[sampleIndex] = int16((frameIndex*audio.FrameSize+sampleIndex)%20000 - 10000)
		}
		frames[frameIndex] = frame
	}

	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}

	// Deliberately no reader ever drains "virtual:output": the point is to
	// prove the overflow diagnostic is observable purely from the sink's own
	// accounting, not from a device-side assertion.
	inferencer := newPlaybackOverflowInferencer(frames)
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := NewSessionCommandWithDeviceRegistry(
		flags.NewAskFlags(),
		globalFlags,
		nil,
		inferencer,
		registry,
	).Generate()
	command.SetOut(io.Discard)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--replay", "synthetic.json",
		"--prompt", "hello",
		"--audio-out-device", "virtual:output",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("session command: %v", err)
	}

	select {
	case <-inferencer.sessionClosed:
	case <-time.After(time.Second):
		t.Fatal("provider session did not finish closing after the command returned")
	}

	got := stderr.String()
	if !strings.Contains(got, "playback diagnostic:") {
		t.Fatalf("stderr missing playback overflow diagnostic; DroppedSamples/OverflowEvents from device_playback.go must be surfaced by the session command. stderr=%q", got)
	}
	if !strings.Contains(got, `event="session_playback_overflow"`) {
		t.Fatalf("stderr playback diagnostic has wrong event name: %q", got)
	}
	wantDropped := totalSamples - capacity
	if !strings.Contains(got, "dropped_samples="+strconv.Itoa(wantDropped)) {
		t.Fatalf("stderr playback diagnostic dropped_samples != %d (capacity=%d total=%d): %q", wantDropped, capacity, totalSamples, got)
	}
	if strings.Contains(got, "overflow_events=0") {
		t.Fatalf("stderr playback diagnostic overflow_events == 0, want at least one overflow: %q", got)
	}
}

// playbackOverflowInferencer is a minimal SessionInferencer that streams a
// fixed sequence of device-frame-sized assistant audio deltas to whatever
// RTC device sink the session command binds, then closes cleanly. It exists
// only to drive TestSessionCommandSurfacesPlaybackOverflowDiagnostic; unlike
// cliAudioOutputInferencer it delivers many discrete audio.FrameSize frames
// (one rtc.InboundMedia.ReadFrame call each) instead of one oversized frame,
// since RTCDeviceSink rejects a write that is not exactly audio.FrameSize.
type playbackOverflowInferencer struct {
	frames [][]int16

	sessionClosed chan struct{}
}

func newPlaybackOverflowInferencer(frames [][]int16) *playbackOverflowInferencer {
	return &playbackOverflowInferencer{
		frames:        frames,
		sessionClosed: make(chan struct{}),
	}
}

func (i *playbackOverflowInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	// receive must hold every queued message (SESSION.OPEN, every AUDIO.DELTA
	// frame, MESSAGE.END, and the SESSION.CLOSE ack) without dropping any of
	// it. messages.TypedBuffer is explicitly non-blocking (see
	// go-agent-loop/pkg/messages/buffers.go): Write drops silently past
	// capacity rather than backpressuring the writer. Send below queues the
	// whole burst of audio deltas synchronously in one call, and the CLI
	// relays this session through more than one same-capacity forwarding
	// buffer (see services.sessionInstructionsSession, whose own relay
	// buffer is sized from this one's Cap()) before anything drains it. A
	// small fixed capacity (this used to be 16, far below a typical frame
	// count derived from the playback queue's capacity) only survived
	// because a concurrent drainer usually kept up; under GOMAXPROCS=1 with
	// coverage instrumentation the writer can outrun every drainer, silently
	// dropping the tail of the burst -- including MESSAGE.END -- which makes
	// the relay give up and close the session before the device sink pump
	// (an entirely separate reader of i.frames, see RTCMedia below) has
	// consumed enough frames to overflow the playback queue at all. Sizing
	// this to comfortably exceed the whole burst removes the dependency on
	// any drainer's scheduling.
	session := &playbackOverflowSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](len(i.frames) + playbackOverflowSessionReceiveHeadroom),
		done:    make(chan struct{}),
		frames:  i.frames,
		closed:  i.sessionClosed,
		inbound: newPlaybackOverflowInboundMedia(i.frames),
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("cli-playback-overflow-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

type playbackOverflowSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	frames  [][]int16
	closed  chan struct{}
	// inbound is the same RTCMedia().Inbound endpoint the device sink pump
	// drains. It is cached here (rather than built fresh in RTCMedia) so Send
	// can wait on its drained signal below.
	inbound *playbackOverflowInboundMedia

	audioOnce sync.Once
	closeOnce sync.Once
}

func (s *playbackOverflowSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	default:
	}
	if msg.Type == messages.StreamTypeSessionClose {
		// The CLI can decide the turn is complete (and ask to close the
		// session) as soon as it observes MESSAGE.END, which races the RTC
		// device sink's pump goroutine that independently drains s.inbound
		// (started only after ConnectSession returns). Closing the session
		// here cancels that pump's context (see RTCDeviceSink.Close), so
		// acknowledging the close before the pump has read every frame would
		// make the overflow this test exists to prove nondeterministic:
		// waiting for the drained signal keeps the assertion hermetic
		// instead of racing device-pump scheduling.
		select {
		case <-s.inbound.drained:
		case <-ctx.Done():
			return false
		}
		if !s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("cli-playback-overflow-session", "test complete"),
		}) {
			return false
		}
		return s.Close() == nil
	}
	s.audioOnce.Do(func() {
		for _, frame := range s.frames {
			s.receive.Write(context.Background(), messages.StreamMessage{
				Type:  messages.StreamTypeAudioDelta,
				Role:  messages.RoleAssistant,
				Value: messages.NewAudioDeltaValue(cliPlaybackOverflowPCM16Bytes(frame)),
			})
		}
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	})
	return true
}

func (s *playbackOverflowSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *playbackOverflowSession) Done() <-chan struct{} { return s.done }

func (s *playbackOverflowSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.closed != nil {
			close(s.closed)
		}
	})
	return nil
}

func (s *playbackOverflowSession) RTCMedia() services.RTCMediaEndpoints {
	if s == nil {
		return services.RTCMediaEndpoints{}
	}
	return services.RTCMediaEndpoints{Inbound: s.inbound}
}

// playbackOverflowInboundMedia returns each configured frame in order, one
// per ReadFrame call and with no artificial delay, so a device sink pump
// with nothing draining its queue overflows deterministically. drained is
// closed the moment every frame has been handed out (immediately before the
// terminal io.EOF), so a caller that must not race the pump's cancellation
// window -- see playbackOverflowSession.Send -- can wait for every frame to
// have actually reached the device sink first.
type playbackOverflowInboundMedia struct {
	frames      []([]int16)
	next        atomic.Int32
	drained     chan struct{}
	drainedOnce sync.Once
}

func newPlaybackOverflowInboundMedia(frames [][]int16) *playbackOverflowInboundMedia {
	return &playbackOverflowInboundMedia{
		frames:  frames,
		drained: make(chan struct{}),
	}
}

func (m *playbackOverflowInboundMedia) ReadFrame(ctx context.Context) (rtc.PCMFrame, error) {
	select {
	case <-ctx.Done():
		return rtc.PCMFrame{}, ctx.Err()
	default:
	}
	index := int(m.next.Add(1)) - 1
	if index >= len(m.frames) {
		m.drainedOnce.Do(func() { close(m.drained) })
		return rtc.PCMFrame{}, io.EOF
	}
	return rtc.PCMFrame{Samples: append([]int16(nil), m.frames[index]...)}, nil
}

func (*playbackOverflowInboundMedia) Close() error { return nil }

func cliPlaybackOverflowPCM16Bytes(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		encoded[index*2] = byte(uint16(sample))
		encoded[index*2+1] = byte(uint16(sample) >> 8)
	}
	return encoded
}

var _ messages.SessionInferencer = (*playbackOverflowInferencer)(nil)
var _ messages.Session = (*playbackOverflowSession)(nil)
var _ services.RTCMediaSession = (*playbackOverflowSession)(nil)
var _ rtc.InboundMedia = (*playbackOverflowInboundMedia)(nil)
