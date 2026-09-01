package cli

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
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
func newLoopbackDeviceRegistry(t *testing.T) *audio.VirtualRegistry {
	t.Helper()
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "mic", Name: "Virtual Mic", Direction: audio.DirectionInput, LoopbackID: "mic-feed"},
			{ID: "mic-feed", Name: "Virtual Mic Feed", Direction: audio.DirectionOutput, LoopbackID: "mic"},
			{ID: "speaker", Name: "Virtual Speaker", Direction: audio.DirectionOutput, LoopbackID: "speaker-tap"},
			{ID: "speaker-tap", Name: "Virtual Speaker Tap", Direction: audio.DirectionInput, LoopbackID: "speaker"},
		},
		Defaults: map[audio.Direction]string{
			audio.DirectionInput:  "mic",
			audio.DirectionOutput: "speaker",
		},
	})
	if err != nil {
		t.Fatalf("new virtual loopback registry: %v", err)
	}
	return registry
}

// newLoopbackDirectPairedRegistry pairs "mic" directly to "speaker", the
// same topology the existing services-level regression precedent
// (TestPairedDeviceBindingDropsLoopedSpeakerFramesBeforeProviderMedia) uses.
// Because both ends share one underlying queue, whatever the real sink
// writes to "speaker" is available to the real source's "mic" read with no
// separate re-injection step and therefore no risk of the source pump's own
// scheduling lag letting playback race ahead of the capture cursor -- a real
// hazard measured empirically while building this harness (see
// TestSessionVirtualDeviceLoopbackSuppressesCoupledFeedback). This gives a
// fixed 0-delay/1.0-attenuation coupling; loopbackAttenuate and an explicit
// delay queue (used directly against a tap in earlier iterations of this
// harness) remain the sweepable primitives for a caller that wants a
// different point on the delay/attenuation space against a topology that
// can tolerate the source pump's independent pace.
func newLoopbackDirectPairedRegistry(t *testing.T) *audio.VirtualRegistry {
	t.Helper()
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		RecordPCM: true,
		Devices: []audio.VirtualDeviceConfig{
			{ID: "mic", Name: "Virtual Mic", Direction: audio.DirectionInput, LoopbackID: "speaker"},
			{ID: "speaker", Name: "Virtual Speaker", Direction: audio.DirectionOutput, LoopbackID: "mic"},
		},
		Defaults: map[audio.Direction]string{
			audio.DirectionInput:  "mic",
			audio.DirectionOutput: "speaker",
		},
	})
	if err != nil {
		t.Fatalf("new virtual direct-paired registry: %v", err)
	}
	return registry
}

func newLoopbackFarFieldRecordedRegistry(t *testing.T) *audio.VirtualRegistry {
	t.Helper()
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		RecordPCM: true,
		Devices: []audio.VirtualDeviceConfig{
			{ID: "mic", Name: "Far-field Virtual Mic", Direction: audio.DirectionInput, LoopbackID: "speaker"},
			{
				ID: "speaker", Name: "Far-field Virtual Speaker", Direction: audio.DirectionOutput, LoopbackID: "mic",
				// 240ms at the native 16kHz device rate exceeds the former
				// +100ms detector range and approximates the 188ms callback /
				// acoustic delay measured from the real failing capture.
				LoopbackDelaySamples: 3840,
				LoopbackImpulse:      []float64{0.52, 0.21, 0, -0.09},
			},
		},
		Defaults: map[audio.Direction]string{
			audio.DirectionInput:  "mic",
			audio.DirectionOutput: "speaker",
		},
	})
	if err != nil {
		t.Fatalf("new far-field recorded registry: %v", err)
	}
	return registry
}

// openLoopbackTap opens a second, test-owned handle on a virtual device by
// its native ID -- independent of whatever handle the CLI's own binding has
// open on that same ID -- and returns it as the concrete stream type so the
// test can call its typed ReadFrame/WriteFrame/WriteSamples methods
// directly.
func openLoopbackTap(t *testing.T, registry *audio.VirtualRegistry, nativeID string) *audio.VirtualStream {
	t.Helper()
	id, err := audio.NewDeviceID(audio.VirtualBackendName, nativeID)
	if err != nil {
		t.Fatalf("build device id %q: %v", nativeID, err)
	}
	opened, err := registry.Open(id)
	if err != nil {
		t.Fatalf("open virtual tap %q: %v", nativeID, err)
	}
	stream, ok := opened.(*audio.VirtualStream)
	if !ok {
		t.Fatalf("virtual tap %q = %T, want *audio.VirtualStream", nativeID, opened)
	}
	t.Cleanup(func() { _ = stream.Close() })
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

// TestLoopbackAttenuateScalesAndSaturates unit-tests the sweepable
// attenuation parameter the coupling harness above documents: an explicit
// gain in (0,1) scales linearly (and round-trips through the exact,
// scale-invariant correlation math the suppression test relies on), while a
// gain that would overflow PCM16 saturates instead of wrapping.
func TestLoopbackAttenuateScalesAndSaturates(t *testing.T) {
	source := []int16{0, 100, -100, 32767, -32768, 12345, -12345}

	half := loopbackAttenuate(source, 0.5)
	want := []int16{0, 50, -50, 16384, -16384, 6173, -6173}
	if !reflect.DeepEqual(half, want) {
		t.Fatalf("attenuate(0.5) = %v, want %v", half, want)
	}

	saturated := loopbackAttenuate(source, 2.0)
	wantSaturated := []int16{0, 200, -200, math.MaxInt16, math.MinInt16, 24690, -24690}
	if !reflect.DeepEqual(saturated, wantSaturated) {
		t.Fatalf("attenuate(2.0) = %v, want %v (must saturate, not wrap)", saturated, wantSaturated)
	}

	if identity := loopbackAttenuate(source, 1.0); !reflect.DeepEqual(identity, source) {
		t.Fatalf("attenuate(1.0) = %v, want the source unchanged (%v)", identity, source)
	}
}

func mustResample(t *testing.T, samples []int16, from, to int) []int16 {
	t.Helper()
	out, err := wavio.Resample(samples, from, to)
	if err != nil {
		t.Fatalf("resample %d -> %d Hz: %v", from, to, err)
	}
	return out
}

// loopbackInboundMedia is the scripted assistant-audio path (provider ->
// client). Frames are buffered generously above the maximum any single test
// pushes, so a send never silently drops a queued burst the way a
// fixed/undersized mock buffer did in the messages.TypedBuffer regression
// behind PR #360.
type loopbackInboundMedia struct {
	frames chan rtc.PCMFrame
	closed chan struct{}
	once   sync.Once
}

func newLoopbackInboundMedia() *loopbackInboundMedia {
	return &loopbackInboundMedia{frames: make(chan rtc.PCMFrame, 256), closed: make(chan struct{})}
}

func (m *loopbackInboundMedia) push(t *testing.T, ctx context.Context, frame rtc.PCMFrame) {
	t.Helper()
	select {
	case m.frames <- frame:
	case <-m.closed:
		t.Fatal("push assistant audio after loopback media closed")
	case <-ctx.Done():
		t.Fatalf("push assistant audio: %v", ctx.Err())
	}
}

func (m *loopbackInboundMedia) ReadFrame(ctx context.Context) (rtc.PCMFrame, error) {
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
			return rtc.PCMFrame{}, rtc.ErrPeerClosed
		}
	case <-ctx.Done():
		return rtc.PCMFrame{}, ctx.Err()
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
	frames []rtc.PCMFrame
	notify chan struct{}
	closed chan struct{}
	once   sync.Once
}

func newLoopbackOutboundMedia() *loopbackOutboundMedia {
	return &loopbackOutboundMedia{notify: make(chan struct{}, 256), closed: make(chan struct{})}
}

func (m *loopbackOutboundMedia) WriteFrame(ctx context.Context, frame rtc.PCMFrame) error {
	select {
	case <-m.closed:
		return rtc.ErrPeerClosed
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	m.mu.Lock()
	m.frames = append(m.frames, rtc.PCMFrame{
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

// snapshot returns every frame received so far.
func (m *loopbackOutboundMedia) snapshot() []rtc.PCMFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]rtc.PCMFrame(nil), m.frames...)
}

// waitForCount deterministically blocks (no sleeps) until at least n frames
// have been recorded, driven by the notify signal rather than polling on a
// timer, and returns exactly the first n.
func (m *loopbackOutboundMedia) waitForCount(ctx context.Context, n int) ([]rtc.PCMFrame, error) {
	for {
		m.mu.Lock()
		if len(m.frames) >= n {
			out := append([]rtc.PCMFrame(nil), m.frames[:n]...)
			m.mu.Unlock()
			return out, nil
		}
		m.mu.Unlock()
		select {
		case <-m.notify:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

var (
	_ rtc.InboundMedia  = (*loopbackInboundMedia)(nil)
	_ rtc.OutboundMedia = (*loopbackOutboundMedia)(nil)
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

func (s *loopbackSession) RTCMedia() rtc.MediaEndpoints {
	return rtc.MediaEndpoints{Inbound: s.inbound, Outbound: s.outbound}
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
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.inbound.Close()
		_ = s.outbound.Close()
	})
	return nil
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
	registry   *audio.VirtualRegistry
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

func startLoopbackHarnessWithRegistry(t *testing.T, registry *audio.VirtualRegistry) *loopbackHarness {
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

	owner := NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry)
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
		h.inbound.push(t, h.ctx, rtc.PCMFrame{Samples: pushed[i], EndOfResponse: i == chunks-1})
	}

	for i, chunk := range pushed {
		got := make([]int16, audio.FrameSize)
		if err := tap.ReadFrame(h.ctx, got); err != nil {
			t.Fatalf("read played frame %d: %v", i, err)
		}
		want := mustResample(t, chunk, loopbackProviderRate, loopbackDeviceRate)
		if len(got) != len(want) {
			t.Fatalf("played frame %d = %d samples, want %d (regression guard: provider audio must be resampled, not played at the wrong rate)", i, len(got), len(want))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("played frame %d content mismatch:\n got=%v\nwant=%v", i, got, want)
		}
	}
}

// TestSessionVirtualDeviceLoopbackSuppressesCoupledFeedback guards PR #357:
// with coupling enabled (the virtual speaker's playback fed directly back
// into the virtual microphone, a real tapped copy of assistant playback
// reaching capture with zero added delay and full amplitude -- the worst,
// most detectable case on the delay/attenuation space, and the same
// topology the existing services-level regression precedent uses), the
// coupled echo must never reach the provider. loopbackAttenuate and an
// explicit per-frame delay queue (both used against a live tap in earlier
// iterations of this harness) are this file's sweepable coupling
// parameters for a caller that wants a different point on that space; this
// specific regression check uses the topology proven robust under
// GOMAXPROCS=1 and -shuffle=on, where a source pump's own scheduling lag
// relative to a re-injecting test goroutine measurably let early playback
// age out of the detector's rolling comparison window before an
// interleaved re-injection ever reached it (see git history on this file).
// The assertion is entirely on what the capture path emits to
// loopbackOutboundMedia; the acoustic-feedback warning is used only to know
// when suppression has been confirmed, never as the pass/fail signal.
func TestSessionVirtualDeviceLoopbackSuppressesCoupledFeedback(t *testing.T) {
	h := startLoopbackHarnessWithRegistry(t, newLoopbackDirectPairedRegistry(t))

	// userFeed is a second, test-owned handle on the same "speaker" device
	// binding.Sink opened. Because "mic" and "speaker" share one underlying
	// queue, a frame written here reaches the real source's "mic" read
	// exactly like a frame the sink itself wrote -- the mechanism the
	// existing precedent uses to inject independent user speech after a
	// confirmed loop.
	userFeed := openLoopbackTap(t, h.registry, "speaker")

	const echoChunks = 6
	echoSignal := make([]int16, 0, echoChunks*audio.FrameSize)
	for i := 0; i < echoChunks; i++ {
		chunk := loopbackTone(loopbackProviderChunkSamples, 5101+i)
		h.inbound.push(t, h.ctx, rtc.PCMFrame{Samples: chunk, EndOfResponse: i == echoChunks-1})
		echoSignal = append(echoSignal, mustResample(t, chunk, loopbackProviderRate, loopbackDeviceRate)...)
	}

	select {
	case <-h.warning.fired:
	case <-h.ctx.Done():
		t.Fatal("coupled echo was never confirmed as feedback (harness setup problem, not a suppression check)")
	}
	// The gate's documented contract is detect-then-suppress, not zero
	// latency: audio.PCM16SelfHearingConfig.MinimumEvidence deliberately
	// requires ~80ms of paired evidence before classifying a loop as
	// feedback at all, precisely so a brief coincidental correlation cannot
	// false-positive. Some of that bounded analysis-window audio can
	// legitimately still be in flight to the provider at the instant the
	// warning fires (a real timing variance in which of that window's
	// frames the primary detector versus the sink's own device write
	// happens to have processed first, confirmed empirically while
	// building this harness -- not the PR #357 regression, which was about
	// content that stayed correlated with the assistant well after
	// confirmation). What #357 requires, and what this asserts, is that
	// nothing correlated with the echo reaches the provider from this
	// point forward.
	suppressedFrom := len(h.outbound.snapshot())

	// Independent probe content, pushed after confirmation, to prove the
	// microphone path is not simply jammed shut (a broken "suppress
	// everything forever" regression would otherwise masquerade as
	// passing). Exactly how the gate's post-confirmation reclassifier
	// batches or paces these releases is an internal timing detail this
	// harness does not assert on.
	const probeFrames = 20
	for i := 0; i < probeFrames; i++ {
		probe := loopbackTone(audio.FrameSize, 9001+i)
		before := h.registry.PCMObservations()
		lastSequence := 0
		if len(before) > 0 {
			lastSequence = before[len(before)-1].Sequence
		}
		if err := userFeed.WriteFrame(h.ctx, probe); err != nil {
			t.Fatalf("write independent probe frame %d: %v", i, err)
		}
		// Advance at the virtual microphone callback boundary. This preserves
		// main's bounded hardware-queue model and guarantees the test never wins
		// enough producer time slices to overwrite its own probe before capture.
		wantCount := len(before) + 1
		for {
			observations, err := h.registry.WaitForPCMObservations(h.ctx, wantCount)
			if err != nil {
				t.Fatalf("wait for independent probe callback %d: %v (playback=%+v outbound=%d)", i, err, userFeed.PlaybackStats(), len(h.outbound.snapshot()))
			}
			consumed := false
			for _, observation := range observations {
				if observation.Sequence > lastSequence && observation.Operation == "read" {
					consumed = true
					break
				}
			}
			if consumed {
				break
			}
			wantCount = len(observations) + 1
		}
	}

	const minFramesExpected = 3
	got, err := h.outbound.waitForCount(h.ctx, suppressedFrom+minFramesExpected)
	if err != nil {
		t.Fatalf("independent probe frames never reached the provider media after suppression (over-suppression regression): %v", err)
	}

	// The suppression assertion is entirely on what the capture path
	// emitted from the confirmed suppression point forward: does the
	// coupled echo's own fingerprint appear anywhere in that received
	// stream? This uses the same correlation primitive that backs
	// audio.PCM16SelfHearingMeasurement (self-hearing is judged by
	// BestAbsoluteCorrelation against a source/received pair) rather than
	// an exact, frame-by-frame content match, since the gate's internal
	// batching of independent releases is not part of this harness's
	// contract. A real leak would show a strong correlation somewhere
	// across the whole received window; silence or independent probe
	// content will not.
	receivedSignal := make([]int16, 0, (len(got)-suppressedFrom)*audio.FrameSize)
	for _, frame := range got[suppressedFrom:] {
		receivedSignal = append(receivedSignal, mustResample(t, frame.Samples, loopbackProviderRate, loopbackDeviceRate)...)
	}

	measurement := measureLoopbackSelfHearing(t, "coupled-echo", echoSignal, "provider-received", receivedSignal)
	if !measurement.Passed {
		t.Fatalf("coupled echo correlates with what the capture path emitted to the provider after suppression was confirmed (BestAbsoluteCorrelation=%.3f at lag=%s over %d compared samples): suppression failed", measurement.BestAbsoluteCorrelation, measurement.BestAbsoluteLag, measurement.ComparedSamples)
	}
	t.Logf("suppression held: coupled echo vs. post-confirmation provider-received BestAbsoluteCorrelation=%.3f (want < %.2f)", measurement.BestAbsoluteCorrelation, audio.PCM16AnalysisDefaultSelfCorrelation)
}

func TestSessionVirtualDeviceLoopbackSuppressesFarFieldFeedbackAndRecordsDevices(t *testing.T) {
	h := startLoopbackHarnessWithRegistry(t, newLoopbackFarFieldRecordedRegistry(t))

	const chunks = 24
	played := make([]int16, 0, chunks*audio.FrameSize)
	for index := 0; index < chunks; index++ {
		chunk := loopbackTone(loopbackProviderChunkSamples, 12001+index)
		played = append(played, mustResample(t, chunk, loopbackProviderRate, loopbackDeviceRate)...)
		h.inbound.push(t, h.ctx, rtc.PCMFrame{Samples: chunk, EndOfResponse: index == chunks-1})
		// A real device callback consumes at the device clock. Pace this mock at
		// the same boundary so a CPU-loaded test process cannot enqueue the
		// entire far-field response into a 250ms hardware queue before capture
		// gets its first turn; that would test producer flooding, not EAC.
		if _, err := h.registry.WaitForPCMObservations(h.ctx, (index+1)*2); err != nil {
			t.Fatalf("pace far-field device callback %d: %v", index, err)
		}
	}

	select {
	case <-h.warning.fired:
	case <-h.ctx.Done():
		var writes, reads []int16
		for _, observation := range h.registry.PCMObservations() {
			if observation.Operation == "write" {
				writes = append(writes, observation.Samples...)
			} else if observation.Operation == "read" {
				reads = append(reads, observation.Samples...)
			}
		}
		compareSamples := min(len(writes), len(reads)-3840)
		var correlation audio.PCM16CorrelationMeasurement
		if compareSamples > 0 {
			duration := time.Duration(compareSamples) * time.Second / loopbackDeviceRate
			correlation, _ = audio.NormalizedPCM16CrossCorrelation(
				audio.PCM16TimedStream{PCM16Input: audio.PCM16Input{StreamID: "writes", ParticipantID: "speaker", SampleRate: loopbackDeviceRate, Samples: writes}, TimelineEnd: time.Duration(len(writes)) * time.Second / loopbackDeviceRate},
				audio.PCM16TimedStream{PCM16Input: audio.PCM16Input{StreamID: "reads", ParticipantID: "mic", SampleRate: loopbackDeviceRate, Samples: reads}, TimelineEnd: time.Duration(len(reads)) * time.Second / loopbackDeviceRate},
				audio.PCM16TimeInterval{ID: "debug", End: duration}, audio.PCM16LagWindow{Min: 240 * time.Millisecond, Max: 240 * time.Millisecond}, audio.PCM16AnalysisSilenceFloorDBFS,
			)
		}
		t.Fatalf("far-field device loop was never confirmed as feedback; writes=%d reads=%d fixed240ms-correlation=%.3f evidence=%d", len(writes), len(reads), correlation.BestAbsoluteCorrelation, correlation.ComparedSamples)
	}

	// At least one output write and one input read per chunk must cross the
	// device backend. The initial 240ms delay adds capture reads, so this lower
	// bound deliberately avoids asserting internal callback batching.
	// Every speaker write must be recorded; microphone reads are asynchronous
	// and the finite virtual playback queue may coalesce/drop leading delay
	// silence before the source pump consumes it. One additional observation
	// is sufficient to prove that the capture side was active, and the exact
	// per-direction counts below remain the authoritative assertion.
	observations, err := h.registry.WaitForPCMObservations(h.ctx, chunks+1)
	if err != nil {
		t.Fatalf("wait for recorded mock-device PCM: %v", err)
	}
	var outputWrites, inputReads int
	for _, observation := range observations {
		if observation.Format.SampleRate != loopbackDeviceRate {
			t.Fatalf("recorded device operation %d rate = %d, want native %d", observation.Sequence, observation.Format.SampleRate, loopbackDeviceRate)
		}
		switch {
		case observation.DeviceID == "virtual:speaker" && observation.Operation == "write":
			outputWrites++
		case observation.DeviceID == "virtual:mic" && observation.Operation == "read":
			inputReads++
		}
	}
	if outputWrites < chunks || inputReads == 0 {
		t.Fatalf("recorded device evidence = output writes %d, input reads %d; want at least %d/%d", outputWrites, inputReads, chunks, 1)
	}

	providerSignal := make([]int16, 0)
	for _, frame := range h.outbound.snapshot() {
		providerSignal = append(providerSignal, mustResample(t, frame.Samples, loopbackProviderRate, loopbackDeviceRate)...)
	}
	if len(providerSignal) > 0 {
		measurement := measureLoopbackSelfHearing(t, "far-field-playback", played, "provider-received", providerSignal)
		if !measurement.Passed {
			t.Fatalf("far-field echo escaped device suppression: correlation=%.3f lag=%s", measurement.BestAbsoluteCorrelation, measurement.BestAbsoluteLag)
		}
	}
}

// measureLoopbackSelfHearing wraps audio.NormalizedPCM16CrossCorrelation --
// the primitive behind audio.PCM16SelfHearingMeasurement -- to search the
// entire received window (not just a narrow lag around zero) for the
// source signal's fingerprint, and reports the result as exactly that
// documented type: "records one participant's sent-to-received correlation.
// Self-hearing uses BestAbsoluteCorrelation by design."
func measureLoopbackSelfHearing(t *testing.T, sourceID string, source []int16, receivedID string, received []int16) audio.PCM16SelfHearingMeasurement {
	t.Helper()
	sourceStream := audio.PCM16TimedStream{
		PCM16Input:  audio.PCM16Input{StreamID: sourceID, ParticipantID: sourceID, SampleRate: loopbackDeviceRate, Samples: source},
		TimelineEnd: time.Duration(len(source)) * time.Second / time.Duration(loopbackDeviceRate),
	}
	receivedStream := audio.PCM16TimedStream{
		PCM16Input:  audio.PCM16Input{StreamID: receivedID, ParticipantID: receivedID, SampleRate: loopbackDeviceRate, Samples: received},
		TimelineEnd: time.Duration(len(received)) * time.Second / time.Duration(loopbackDeviceRate),
	}
	maxLag := receivedStream.TimelineEnd - sourceStream.TimelineEnd
	if maxLag < 0 {
		maxLag = 0
	}
	interval := audio.PCM16TimeInterval{ID: "echo-window", Start: 0, End: sourceStream.TimelineEnd}
	lagWindow := audio.PCM16LagWindow{Min: 0, Max: maxLag}
	correlation, err := audio.NormalizedPCM16CrossCorrelation(sourceStream, receivedStream, interval, lagWindow, audio.PCM16AnalysisSilenceFloorDBFS)
	if err != nil {
		t.Fatalf("measure self-hearing correlation (%s vs %s): %v", sourceID, receivedID, err)
	}
	return audio.PCM16SelfHearingMeasurement{
		PCM16CorrelationMeasurement: correlation,
		Direction:                   "capture-to-provider",
		Passed:                      !correlation.HasEvidence() || correlation.BestAbsoluteCorrelation < audio.PCM16AnalysisDefaultSelfCorrelation,
	}
}

// TestSessionVirtualDeviceLoopbackPreservesIndependentSpeechDuringActivePlayback
// guards against silently over-suppressing genuine barge-in speech: audio
// that is uncorrelated with assistant playback must still reach the
// provider even while the assistant is actively speaking.
func TestSessionVirtualDeviceLoopbackPreservesIndependentSpeechDuringActivePlayback(t *testing.T) {
	h := startLoopbackHarness(t)
	feed := openLoopbackTap(t, h.registry, "mic-feed")
	tap := openLoopbackTap(t, h.registry, "speaker-tap")

	// The assistant's response is still open (no EndOfResponse yet) when
	// independent speech arrives at the microphone. Draining each pushed
	// chunk from speaker-tap before continuing is a real synchronization
	// point, not a sleep: RTCDeviceSink.Pump only enqueues a played frame
	// (unblocking this ReadFrame) after localFeedbackGate.WritePlayback has
	// fully observed it under the gate's lock, so by the time this loop
	// returns the assistant's playback timeline is guaranteed to be
	// established before independent speech starts arriving at the
	// microphone.
	for i := 0; i < 3; i++ {
		h.inbound.push(t, h.ctx, rtc.PCMFrame{Samples: loopbackTone(loopbackProviderChunkSamples, 6001+i)})
		got := make([]int16, audio.FrameSize)
		if err := tap.ReadFrame(h.ctx, got); err != nil {
			t.Fatalf("read played frame %d: %v", i, err)
		}
	}

	const speechFrames = 4
	want := make([][]int16, speechFrames)
	for i := range want {
		want[i] = loopbackTone(audio.FrameSize, 7001+i)
		if err := feed.WriteFrame(h.ctx, want[i]); err != nil {
			t.Fatalf("feed independent speech frame %d: %v", i, err)
		}
	}

	got, err := h.outbound.waitForCount(h.ctx, speechFrames)
	if err != nil {
		t.Fatalf("independent speech during active playback did not reach the provider (over-suppression regression): %v", err)
	}
	for i, frame := range got {
		wantResampled := mustResample(t, want[i], loopbackDeviceRate, loopbackProviderRate)
		if !reflect.DeepEqual(frame.Samples, wantResampled) {
			t.Fatalf("independent speech frame %d mismatch during active playback: got=%v want=%v", i, frame.Samples, wantResampled)
		}
	}

	// Finish the assistant response so the session ends cleanly.
	for i := 0; i < 2; i++ {
		h.inbound.push(t, h.ctx, rtc.PCMFrame{Samples: loopbackTone(loopbackProviderChunkSamples, 6101+i), EndOfResponse: i == 1})
	}
}
