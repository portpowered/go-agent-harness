package cli

import (
	"context"
	"errors"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// This file is the hermetic virtual-device loopback harness: a virtual
// speaker whose playback can be fed back into a virtual microphone with
// explicit, sweepable delay/attenuation, driven through a real `agent
// session` invocation (Generate/Execute, via NewSessionCommandWithDeviceRegistry,
// the same seam TestSessionRecordOnlyLiveOpensDevicesAndDoesNotSelfClose
// uses) against an injected scripted session. No live provider, no real
// hardware.
//
// It is the regression guard for five defects that a file-input/two-process
// probe topology could never exercise because none of them drove one live
// mic+speaker loop end to end:
//   - PR #350/#359: assistant audio played 1.5x fast because the provider's
//     24 kHz PCM was written to a 16 kHz device without resampling.
//   - PR #350: choppy playback from a silently truncating ring buffer.
//   - PR #352/#357: acoustic feedback (the agent hearing itself) was only
//     "detected" (a warning fired) but not actually suppressed from what
//     reached the provider.
//   - PR #356: --record broke the working bare live session outright.
//   - PR #360: drop counters computed but never surfaced.
//
// Three assertions matter, and each is checked on what the capture path
// actually EMITS to the provider (or what the device actually plays), never
// on whether a diagnostic warning fired:
//  1. TestSessionVirtualDeviceLoopbackFidelity: audio piped into the virtual
//     input/output arrives at the correct rate/count, no 1.5x stretch, no
//     silent truncation.
//  2. TestSessionVirtualDeviceLoopbackSuppressesCoupledFeedback: with
//     coupling enabled (a delayed, attenuated copy of real playback injected
//     into capture), none of that echo reaches the provider.
//  3. TestSessionVirtualDeviceLoopbackPreservesIndependentSpeechDuringActivePlayback:
//     genuine, uncorrelated speech injected while the assistant is still
//     speaking DOES reach the provider (the barge-in direction).
const (
	// loopbackProviderChunkSamples is 30ms of mono PCM16 at the realtime
	// provider rate (24 kHz). 720 samples resamples to exactly 480 samples
	// (audio.FrameSize) at the virtual device's native 16 kHz rate, so every
	// pushed provider chunk maps to exactly one clean device frame with no
	// batching remainder to reason about.
	loopbackProviderChunkSamples = 720
	loopbackProviderRate         = 24000
	loopbackDeviceRate           = audio.SampleRate // 16000
)

// newLoopbackDeviceRegistry builds a four-device virtual topology instead of
// reusing audio.DefaultVirtualBackendConfig()'s direct mic<->speaker pair.
// A direct pair would auto-mirror whatever the real sink writes into the
// real source with a fixed, uncontrollable 0-delay/1.0-attenuation coupling.
// Pairing "mic" and "speaker" with dedicated test-owned counterparts instead
// gives the fidelity and independent-speech tests a tap/feed they can read
// or write without ever competing with the CLI's own device reads/writes.
func newLoopbackDeviceRegistry(t *testing.T) *devicegw.VirtualRegistry {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		Devices: []devicegw.VirtualDeviceConfig{
			{ID: "mic", Name: "Virtual Mic", Direction: devicegw.DirectionInput, LoopbackID: "mic-feed"},
			{ID: "mic-feed", Name: "Virtual Mic Feed", Direction: devicegw.DirectionOutput, LoopbackID: "mic"},
			{ID: "speaker", Name: "Virtual Speaker", Direction: devicegw.DirectionOutput, LoopbackID: "speaker-tap"},
			{ID: "speaker-tap", Name: "Virtual Speaker Tap", Direction: devicegw.DirectionInput, LoopbackID: "speaker"},
		},
		Defaults: map[devicegw.Direction]string{
			devicegw.DirectionInput:  "mic",
			devicegw.DirectionOutput: "speaker",
		},
	})
	if err != nil {
		t.Fatalf("new virtual loopback registry: %v", err)
	}
	return registry
}

// openLoopbackTap opens a second, test-owned handle on a virtual device by
// its native ID -- independent of whatever handle the CLI's own binding has
// open on that same ID -- and returns it as the concrete stream type so the
// test can call its typed ReadFrame/WriteFrame/WriteSamples methods
// directly.
func openLoopbackTap(t *testing.T, registry *devicegw.VirtualRegistry, nativeID string) *devicegw.VirtualStream {
	t.Helper()
	id, err := devicegw.NewDeviceID(devicegw.VirtualBackendName, nativeID)
	if err != nil {
		t.Fatalf("build device id %q: %v", nativeID, err)
	}
	opened, err := registry.Open(id)
	if err != nil {
		t.Fatalf("open virtual tap %q: %v", nativeID, err)
	}
	stream, ok := opened.(*devicegw.VirtualStream)
	if !ok {
		t.Fatalf("virtual tap %q = %T, want *audio.VirtualStream", nativeID, opened)
	}
	t.Cleanup(func() { closeForTest(t, stream.Close) })
	return stream
}

// loopbackTone deterministically generates n PCM16 samples. Distinct seeds
// produce distinguishable, uncorrelated waveforms so the feedback detector's
// correlation-based classification is exercised meaningfully rather than
// against silence or a single constant value. The generator mirrors the
// existing rtc_device_feedback_test.go feedbackSignal fixture, generalized
// to an arbitrary sample count.
func loopbackTone(n, seed int) []int16 {
	samples := make([]int16, n)
	state := uint32(seed*7919 + 1) //nolint:gosec // deterministic bounded fixture
	for i := range samples {
		state = state*1664525 + 1013904223
		samples[i] = int16(int32(state>>16)%24000 - 12000) //nolint:gosec // bounded deterministic PCM fixture
	}
	return samples
}

// loopbackAttenuate scales samples by gain, saturating to the PCM16 range.
// Explicit, named gain (and the caller-chosen frame delay used alongside it)
// are exactly the sweepable coupling parameters the harness is required to
// expose.
func loopbackAttenuate(samples []int16, gain float64) []int16 {
	out := make([]int16, len(samples))
	for i, s := range samples {
		v := math.Round(float64(s) * gain)
		switch {
		case v > math.MaxInt16:
			v = math.MaxInt16
		case v < math.MinInt16:
			v = math.MinInt16
		}
		out[i] = int16(v)
	}
	return out
}

// mustResampleStream mirrors the stream-owned DSP boundary: phase and filter
// history continue across packet boundaries, and only the final packet flushes
// the exact tail. Per-packet stateless conversion would compare a different
// signal at every boundary.
func mustResampleStream(t *testing.T, chunks [][]int16, from, to int) []int16 {
	t.Helper()
	resampler, err := wavio.NewPCM16Resampler(from, to)
	if err != nil {
		t.Fatalf("create streaming resampler %d -> %d Hz: %v", from, to, err)
	}
	result := make([]int16, 0)
	for i, chunk := range chunks {
		out, err := resampler.Process(chunk, i == len(chunks)-1)
		if err != nil {
			t.Fatalf("stream resample chunk %d (%d -> %d Hz): %v", i, from, to, err)
		}
		result = append(result, out...)
	}
	return result
}

// loopbackInboundMedia is the scripted assistant-audio path (provider ->
// client). Frames are buffered generously above the maximum any single test
// pushes, so a send never silently drops a queued burst the way a
// fixed/undersized mock buffer did in the messages.TypedBuffer regression
// behind PR #360.
type loopbackInboundMedia struct {
	frames chan audio.PCMFrame
	closed chan struct{}
	once   sync.Once
}

func newLoopbackInboundMedia() *loopbackInboundMedia {
	return &loopbackInboundMedia{frames: make(chan audio.PCMFrame, 256), closed: make(chan struct{})}
}

func (m *loopbackInboundMedia) push(t *testing.T, ctx context.Context, frame audio.PCMFrame) {
	t.Helper()
	select {
	case m.frames <- frame:
	case <-m.closed:
		t.Fatal("push assistant audio after loopback media closed")
	case <-ctx.Done():
		t.Fatalf("push assistant audio: %v", ctx.Err())
	}
}

func (m *loopbackInboundMedia) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case frame := <-m.frames:
		return frame, nil
	default:
	}
	select {
	case frame := <-m.frames:
		return frame, nil
	case <-m.closed:
		// Drain first so a queued final frame cannot lose a race against
		// Close(), matching the established feedbackInbound pattern.
		select {
		case frame := <-m.frames:
			return frame, nil
		default:
			return audio.PCMFrame{}, io.EOF
		}
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}

func (m *loopbackInboundMedia) Close() error {
	m.once.Do(func() { close(m.closed) })
	return nil
}

// loopbackOutboundMedia is the recording capture-to-provider path (client ->
// provider). It is exactly the seam the three assertions check: what the
// capture path actually emits, not what a diagnostic writer says about it.
type loopbackOutboundMedia struct {
	mu     sync.Mutex
	frames []audio.PCMFrame
	notify chan struct{}
	closed chan struct{}
	once   sync.Once
}

func newLoopbackOutboundMedia() *loopbackOutboundMedia {
	return &loopbackOutboundMedia{notify: make(chan struct{}, 256), closed: make(chan struct{})}
}

func (m *loopbackOutboundMedia) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	select {
	case <-m.closed:
		return rtc.ErrPeerClosed
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	m.mu.Lock()
	m.frames = append(m.frames, audio.PCMFrame{
		Samples:       append([]int16(nil), frame.Samples...),
		EndOfResponse: frame.EndOfResponse,
	})
	m.mu.Unlock()
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return nil
}

func (m *loopbackOutboundMedia) Close() error {
	m.once.Do(func() { close(m.closed) })
	return nil
}

var (
	_ audio.InboundMedia  = (*loopbackInboundMedia)(nil)
	_ audio.OutboundMedia = (*loopbackOutboundMedia)(nil)
)

// loopbackSession is a minimal messages.Session + rtc.MediaSession double
// that, unlike browserAdmissionMedia (a pure blocking stub used only to
// satisfy admission checks), actually carries real PCM16 audio both ways.
type loopbackSession struct {
	receive        *messages.TypedBuffer[messages.StreamMessage]
	done           chan struct{}
	closeRequested chan struct{}
	inbound        *loopbackInboundMedia
	outbound       *loopbackOutboundMedia
	requestOnce    sync.Once
	closeOnce      sync.Once
}

func (s *loopbackSession) RTCMedia() audio.MediaEndpoints {
	return audio.MediaEndpoints{Inbound: s.inbound, Outbound: s.outbound}
}

func (s *loopbackSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionClose {
		s.requestOnce.Do(func() { close(s.closeRequested) })
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *loopbackSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *loopbackSession) Done() <-chan struct{} { return s.done }

func (s *loopbackSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = errors.Join(s.inbound.Close(), s.outbound.Close())
	})
	return err
}

type loopbackInferencer struct {
	opened     chan struct{}
	openedOnce sync.Once
	session    *loopbackSession
}

func newLoopbackInferencer(inbound *loopbackInboundMedia, outbound *loopbackOutboundMedia) *loopbackInferencer {
	return &loopbackInferencer{
		opened: make(chan struct{}),
		session: &loopbackSession{
			receive:        messages.NewTypedBuffer[messages.StreamMessage](16),
			done:           make(chan struct{}),
			closeRequested: make(chan struct{}),
			inbound:        inbound,
			outbound:       outbound,
		},
	}
}

func (i *loopbackInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("virtual-loopback-session", "test"),
	})
	i.openedOnce.Do(func() { close(i.opened) })
	return i.session, nil
}

// endFromProvider simulates the far side hanging up, mirroring
// recordOnlyLiveInferencer's shutdown so ExecuteContext returns cleanly.
func (i *loopbackInferencer) endFromProvider(ctx context.Context) {
	i.session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue("virtual-loopback-session", "test complete"),
	})
}

// loopbackWarningSignal captures the CLI's stderr and fires fired exactly
// once the local acoustic-feedback warning appears. It is used only to
// synchronize the test with the moment suppression is confirmed -- never as
// the pass/fail assertion itself. That distinction (assert on emitted audio,
// not on a warning) is what let "detected" masquerade as "fixed" before
// PR #357; this signal exists purely to know *when* to inspect the outbound
// media, not *whether* the test should pass.
type loopbackWarningSignal struct {
	mu    sync.Mutex
	text  strings.Builder
	fired chan struct{}
	once  sync.Once
}

func newLoopbackWarningSignal() *loopbackWarningSignal {
	return &loopbackWarningSignal{fired: make(chan struct{})}
}

func (w *loopbackWarningSignal) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.text.Write(data)
	confirmed := strings.Contains(w.text.String(), "Acoustic feedback detected")
	w.mu.Unlock()
	if confirmed {
		w.once.Do(func() { close(w.fired) })
	}
	return len(data), nil
}

// loopbackHarness owns one live (Generate/Execute) `agent session --record`
// invocation wired to the virtual device registry and a scripted inferencer,
// exactly the seam TestSessionRecordOnlyLiveOpensDevicesAndDoesNotSelfClose
// uses. --record alone (no --prompt/--audio-in/--image/--browser-tools) is
// recognized as record-only-live, so this automatically gets the same
// implicit microphone/speaker devices and stay-open semantics a bare live
// conversation gets, and (because the provider is grok with no replay path)
// automatically negotiates the real 24 kHz realtime provider rate against a
// 16 kHz-only virtual device -- the exact rate mismatch PR #350/#359 guards.
type loopbackHarness struct {
	t          *testing.T
	ctx        context.Context
	cancel     context.CancelFunc
	registry   *devicegw.VirtualRegistry
	inbound    *loopbackInboundMedia
	outbound   *loopbackOutboundMedia
	inferencer *loopbackInferencer
	warning    *loopbackWarningSignal
	runErr     chan error
	recordPath string
	finishOnce sync.Once
}

func startLoopbackHarness(t *testing.T) *loopbackHarness {
	t.Helper()
	return startLoopbackHarnessWithRegistry(t, newLoopbackDeviceRegistry(t))
}

func startLoopbackHarnessWithRegistry(t *testing.T, registry *devicegw.VirtualRegistry) *loopbackHarness {
	t.Helper()
	inbound := newLoopbackInboundMedia()
	outbound := newLoopbackOutboundMedia()
	inferencer := newLoopbackInferencer(inbound, outbound)
	warning := newLoopbackWarningSignal()

	configDir := t.TempDir()
	configYAML := `
model:
  provider: grok
  grok:
    model: grok-realtime-test
    api_key: test-key
`
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir

	owner := newTestSessionCommand(flags.NewAskFlags(), globalFlags, testSessionDeps{Inferencer: inferencer, Registry: registry})
	owner.SetFeedbackWarningWriter(warning)
	command := owner.Generate()
	command.SetOut(io.Discard)
	command.SetErr(warning)
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	command.SetArgs([]string{"--record", recordPath})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	select {
	case <-inferencer.opened:
	case <-ctx.Done():
		cancel()
		t.Fatal("virtual loopback session never connected to the provider")
	}

	h := &loopbackHarness{
		t: t, ctx: ctx, cancel: cancel,
		registry: registry, inbound: inbound, outbound: outbound,
		inferencer: inferencer, warning: warning, runErr: runErr, recordPath: recordPath,
	}
	t.Cleanup(h.finish)
	return h
}

func (h *loopbackHarness) finish() {
	h.finishOnce.Do(func() {
		defer h.cancel()
		h.inferencer.endFromProvider(h.ctx)
		select {
		case err := <-h.runErr:
			if err != nil {
				h.t.Errorf("virtual loopback session command: %v", err)
			}
		case <-h.ctx.Done():
			h.t.Error("virtual loopback session did not return after the provider-driven close")
		}
	})
}

// TestSessionVirtualDeviceLoopbackFidelity guards PR #350/#359: assistant
// audio piped through the virtual speaker must arrive resampled correctly
// (24 kHz provider -> 16 kHz device), with the exact expected sample count
// and content for its duration -- no 1.5x stretch, no silent truncation.
func TestSessionVirtualDeviceLoopbackFidelity(t *testing.T) {
	h := startLoopbackHarness(t)
	tap := openLoopbackTap(t, h.registry, "speaker-tap")

	const chunks = 6
	pushed := make([][]int16, chunks)
	for i := range pushed {
		pushed[i] = loopbackTone(loopbackProviderChunkSamples, 4001+i)
		h.inbound.push(t, h.ctx, audio.PCMFrame{Samples: pushed[i], EndOfResponse: i == chunks-1})
	}

	want := mustResampleStream(t, pushed, loopbackProviderRate, loopbackDeviceRate)
	got := make([]int16, 0, len(want))
	for i := 0; i < len(pushed); i++ {
		frame := make([]int16, audio.FrameSize)
		if err := tap.ReadFrame(h.ctx, frame); err != nil {
			t.Fatalf("read played frame %d: %v", i, err)
		}
		got = append(got, frame...)
	}
	if len(got) != len(want) {
		t.Fatalf("played stream = %d samples, want %d (regression guard: provider audio must be resampled, not played at the wrong rate)", len(got), len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("played stream content mismatch:\n got=%v\nwant=%v", got, want)
	}
}
