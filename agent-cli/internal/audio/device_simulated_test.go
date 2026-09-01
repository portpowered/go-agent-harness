package audio

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/observability"
)

func TestSimulatedDuplexObservabilityReportsFaultsOutsideDeviceLock(t *testing.T) {
	scenario := simulatedScenario(48000, []int{480})
	scenario.Faults = []FaultEvent{{Callback: 0, Direction: DirectionOutput, Type: FaultClockReset, ID: "reset-1"}}
	scenario.PlaybackQueue.LatencyNanos = 1
	scenario.CaptureQueue.LatencyNanos = 1
	var (
		registry *SimulatedDuplexRegistry
		samples  []observability.MetricSample
		records  []observability.LogRecord
	)
	sampler := observability.MetricSamplerFunc(func(_ context.Context, sample observability.MetricSample) error {
		// Snapshot reacquires the registry lock. Advance would deadlock here if
		// it invoked application observers from the callback critical section.
		_ = registry.Trace()
		samples = append(samples, sample)
		return nil
	})
	logger := observability.LoggerFunc(func(_ context.Context, record observability.LogRecord) error {
		records = append(records, record)
		return nil
	})
	var err error
	registry, err = NewSimulatedDuplexRegistryWithObservability(scenario, sampler, logger)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := registry.Open(registry.output.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	if err := opened.(*SimulatedDuplexStream).WriteSamples(context.Background(), make([]int16, 960)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Advance(1); err != nil {
		t.Fatal(err)
	}
	var callbacks, faults int
	for _, sample := range samples {
		switch sample.Name {
		case "audio.device.callbacks":
			callbacks++
		case "audio.device.faults":
			faults++
		}
	}
	if callbacks != 2 || faults < 1 {
		t.Fatalf("observed callbacks=%d faults=%d samples=%+v", callbacks, faults, samples)
	}
	wantLoss := map[string]bool{
		"audio.playback.underflow/samples": false,
		"audio.playback.dropped/samples":   false,
		"audio.capture.dropped/samples":    false,
		"audio.capture.sequence_gaps/gaps": false,
	}
	for _, sample := range samples {
		key := sample.Name + "/" + sample.Unit
		if _, ok := wantLoss[key]; ok && sample.Value > 0 {
			wantLoss[key] = true
		}
	}
	for key, observed := range wantLoss {
		if !observed {
			t.Fatalf("missing positive loss metric %s in %+v", key, samples)
		}
	}
	if len(records) == 0 || records[0].Fields["fault_id"] != "reset-1" {
		t.Fatalf("fault logs = %+v", records)
	}
}

func TestSimulatedDuplexCleanBaselineAndVariableCallbackQuantum(t *testing.T) {
	for _, quanta := range [][]int{{64}, {128}, {240}, {256}, {480}, {512}, {960}, {128, 512, 256, 480}} {
		t.Run(fmtIntSlice(quanta), func(t *testing.T) {
			s := simulatedScenario(48000, quanta)
			r, output := openSimulatedOutput(t, s)
			count := 0
			for _, q := range quanta {
				count += q
			}
			want := int16Samples(-count/2, count)
			if err := output.WriteSamples(context.Background(), want); err != nil {
				t.Fatal(err)
			}
			if err := r.Advance(len(quanta)); err != nil {
				t.Fatal(err)
			}
			if got := r.RenderedSamples(); !reflect.DeepEqual(got, want) {
				t.Fatalf("segmented render changed samples: got %d want %d", len(got), len(want))
			}
			stats := r.PlaybackStats()
			if stats.UnderflowEvents != 0 || stats.DroppedSamples != 0 || stats.RenderedSamples != uint64(count) {
				t.Fatalf("nominal stats = %+v", stats)
			}
			trace := r.Trace()
			for i, event := range trace {
				if i%2 == 0 && event.Tap != "render" {
					t.Fatalf("event ordering = %+v", trace)
				}
			}
		})
	}
}

func TestSimulatedDuplex48kMacCadenceAndExactUnderrun(t *testing.T) {
	r, output := openSimulatedOutput(t, simulatedScenario(48000, []int{480}))
	if err := output.WriteSamples(context.Background(), make([]int16, 960)); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(3); err != nil {
		t.Fatal(err)
	}
	stats := r.PlaybackStats()
	if stats.CallbackCount != 3 || stats.RenderedSamples != 1440 || stats.UnderflowEvents != 1 || stats.UnderflowSamples != 480 {
		t.Fatalf("Mac cadence stats = %+v", stats)
	}
	rendered := r.RenderedSamples()
	if len(rendered) != 1440 || !allZero(rendered[960:]) {
		t.Fatal("underrun region is not exact zero fill")
	}
}

func TestSimulatedDuplexClockJitterFaultsAndEpochsAreDeterministic(t *testing.T) {
	s := simulatedScenario(16000, []int{480})
	s.Render.JitterSamples = []int{-4, 0, 7}
	s.Faults = []FaultEvent{
		{Callback: 1, Direction: DirectionOutput, Type: FaultMissingCallback, ID: "gap"},
		{Callback: 2, Direction: DirectionOutput, Type: FaultClockReset, ID: "reset"},
		{Callback: 3, Direction: DirectionOutput, Type: FaultDuplicateCallback, ID: "duplicate"},
	}
	r, output := openSimulatedOutput(t, s)
	if err := output.WriteSamples(context.Background(), make([]int16, 480*5)); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(4); err != nil {
		t.Fatal(err)
	}
	var renders []DeviceTraceEvent
	for _, event := range r.Trace() {
		if event.Tap == "render" {
			renders = append(renders, event)
		}
	}
	if len(renders) != 5 {
		t.Fatalf("render trace count = %d, want 5 including explicit gap and rejected duplicate", len(renders))
	}
	if !containsFlag(renders[1].Flags, "gap") || !containsFlag(renders[1].Flags, string(FaultMissingCallback)) {
		t.Fatalf("missing callback trace = %+v", renders[1])
	}
	if renders[2].ClockEpoch != 1 || renders[2].StartSample != 0 {
		t.Fatalf("reset event = %+v", renders[2])
	}
	if !containsFlag(renders[4].Flags, "duplicate_rejected") {
		t.Fatalf("duplicate trace = %+v", renders[4])
	}
	if renders[0].HostMonoSamples != -4 {
		t.Fatalf("jitter trace = %+v", renders[0])
	}

	r2, output2 := openSimulatedOutput(t, s)
	_ = output2.WriteSamples(context.Background(), make([]int16, 480*5))
	_ = r2.Advance(4)
	if !reflect.DeepEqual(r.Trace(), r2.Trace()) || !reflect.DeepEqual(r.RenderedSamples(), r2.RenderedSamples()) {
		t.Fatal("same scenario did not replay deterministically")
	}
}

func TestSimulatedDuplexAcousticDelayGainNearEndAndFIR(t *testing.T) {
	s := simulatedScenario(16000, []int{8})
	s.Acoustic = AcousticSpec{DelaySamples: 2, GainQ15: 16384, ImpulseResponseQ15: []int16{32767}, NearEnd: []int16{3, 3, 3}, Background: []int16{2, 2, 2}}
	r, output, input := openSimulatedPair(t, s)
	if err := output.WriteSamples(context.Background(), []int16{100, 200, 300, 400, 500, 600, 700, 800}); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(1); err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, FrameSize)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := input.ReadFrame(ctx, frame); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("partial callback read = %v, want deadline", err)
	}
	if got := r.CaptureStats(); got.CapturedSamples != 8 || got.QueuedSamples != 8 {
		t.Fatalf("capture stats = %+v", got)
	}
	// The first two samples are acoustic delay plus the two independent stems.
	r.mu.Lock()
	got := append([]int16(nil), r.capture...)
	r.mu.Unlock()
	if !reflect.DeepEqual(got[:5], []int16{5, 5, 55, 100, 150}) {
		t.Fatalf("acoustic mix prefix = %v", got[:5])
	}
}

func TestSimulatedDuplexCaptureOverflowPolicies(t *testing.T) {
	for _, policy := range []string{"drop_oldest", "drop_newest"} {
		t.Run(policy, func(t *testing.T) {
			s := simulatedScenario(16000, []int{8})
			s.CaptureQueue = QueueSpec{LatencyNanos: int64(time.Millisecond), DropPolicy: policy} // 16 samples
			r, output := openSimulatedOutput(t, s)
			_ = output.WriteSamples(context.Background(), int16Samples(1, 24))
			if err := r.Advance(3); err != nil {
				t.Fatal(err)
			}
			stats := r.CaptureStats()
			if stats.QueuedSamples != 16 || stats.DroppedSamples != 8 || stats.DroppedFrames != 1 || stats.SequenceGaps != 1 || stats.DropPolicy != policy {
				t.Fatalf("%s stats = %+v", policy, stats)
			}
			r.mu.Lock()
			samples := append([]int16(nil), r.capture...)
			r.mu.Unlock()
			if policy == "drop_oldest" && samples[0] != 9 {
				t.Fatalf("drop-oldest retained stale prefix %d", samples[0])
			}
			if policy == "drop_newest" && samples[0] != 1 {
				t.Fatalf("drop-newest lost old prefix %d", samples[0])
			}
		})
	}
}

func TestSimulatedDuplexDeviceLossWakesBlockedRead(t *testing.T) {
	s := simulatedScenario(16000, []int{480})
	s.Faults = []FaultEvent{{Callback: 0, Direction: DirectionInput, Type: FaultDeviceLoss, ID: "unplug"}}
	r, _, input := openSimulatedPair(t, s)
	done := make(chan error, 1)
	go func() { done <- input.ReadFrame(context.Background(), make([]int16, FrameSize)) }()
	if err := r.Advance(1); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrDeviceLost) {
			t.Fatalf("blocked read = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("device loss did not wake read")
	}
}

func TestDeviceFrameDuration16_24_48k(t *testing.T) {
	for _, tc := range []struct {
		rate int
		want time.Duration
	}{{16000, 30 * time.Millisecond}, {24000, 20 * time.Millisecond}, {48000, 10 * time.Millisecond}} {
		if got := time.Duration(FrameSize) * time.Second / time.Duration(tc.rate); got != tc.want {
			t.Fatalf("480 samples at %d Hz = %s, want %s", tc.rate, got, tc.want)
		}
	}
}

func TestSimulatedDuplexRegistryAndStreamContracts(t *testing.T) {
	s := simulatedScenario(16000, []int{480})
	r, err := NewSimulatedDuplexRegistry(s)
	if err != nil {
		t.Fatal(err)
	}
	devices, err := r.List()
	if err != nil || len(devices) != 2 {
		t.Fatalf("List = %v/%v", devices, err)
	}
	for _, direction := range []Direction{DirectionInput, DirectionOutput} {
		if device, err := r.Default(direction); err != nil || device.Direction != direction {
			t.Fatalf("Default(%s)=%+v/%v", direction, device, err)
		}
	}
	if _, err := r.Default(Direction("sideways")); err == nil {
		t.Fatal("invalid direction accepted")
	}
	if _, err := r.Open("simulated-duplex:missing"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("missing open = %v", err)
	}
	if _, err := r.OpenWithFormat(r.output.ID, PCM16DeviceFormat(24000)); !errors.Is(err, ErrUnsupportedDeviceFormat) {
		t.Fatalf("wrong format = %v", err)
	}
	outRaw, _ := r.Open(r.output.ID)
	inRaw, _ := r.Open(r.input.ID)
	out := outRaw.(*SimulatedDuplexStream)
	in := inRaw.(*SimulatedDuplexStream)
	if out.DeviceFormat() != PCM16DeviceFormat(16000) {
		t.Fatalf("format = %v", out.DeviceFormat())
	}
	if err := in.WriteFrame(context.Background(), make([]int16, FrameSize)); err == nil {
		t.Fatal("input accepted write")
	}
	if err := out.ReadFrame(context.Background(), make([]int16, FrameSize)); err == nil {
		t.Fatal("output accepted read")
	}
	if err := out.Write(context.Background(), []byte{1}); err == nil {
		t.Fatal("odd PCM accepted")
	}
	frame := int16Samples(-240, FrameSize)
	if err := out.WriteFrame(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(1); err != nil {
		t.Fatal(err)
	}
	raw, err := in.Read(context.Background())
	if err != nil || len(raw) != FrameSize*2 {
		t.Fatalf("Read = %d/%v", len(raw), err)
	}
	decoded := make([]int16, FrameSize)
	decodePCM16(decoded, raw)
	if !reflect.DeepEqual(decoded, frame) {
		t.Fatal("raw round trip changed PCM")
	}
	rawFrame := make([]byte, FrameSize*2)
	encodePCM16(rawFrame, frame)
	if err := out.Write(context.Background(), rawFrame); err != nil {
		t.Fatal(err)
	}
	if out.PlaybackStats().QueuedSamples != FrameSize {
		t.Fatalf("queued = %+v", out.PlaybackStats())
	}
	if err := out.WaitForPlaybackCapacity(context.Background(), FrameSize); err != nil {
		t.Fatal(err)
	}
	if err := out.WaitForPlaybackCapacity(context.Background(), 100000); !errors.Is(err, ErrInvalidPlaybackQueue) {
		t.Fatalf("oversized capacity = %v", err)
	}
	if got := out.DiscardPlayback(); got != FrameSize {
		t.Fatalf("discard = %d", got)
	}
	if err := out.WaitForPlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := out.WriteFrame(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := out.WriteSamples(cancelled, frame); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write = %v", err)
	}
	if err := out.WaitForPlayback(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled drain = %v", err)
	}
	_, high, _ := PlaybackQueueWatermarks(r.format)
	_ = out.WriteSamples(context.Background(), make([]int16, high-FrameSize))
	if err := out.WaitForPlaybackCapacity(cancelled, FrameSize); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled capacity wait = %v", err)
	}
	out.DiscardPlayback()
	_ = out.WriteFrame(context.Background(), frame)
	wait := make(chan error, 1)
	go func() { wait <- out.WaitForPlayback(context.Background()) }()
	if err := r.Advance(1); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wait:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("playback wait did not wake")
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSimulatedDuplexScenarioValidationAndTimelineFaults(t *testing.T) {
	invalid := []DuplexScenario{
		{},
		{Render: ClockSpec{NominalRate: 16000, Quanta: []int{480}}, Capture: ClockSpec{NominalRate: 24000, Quanta: []int{480}}},
		{Render: ClockSpec{NominalRate: 16000, Quanta: []int{0}}, Capture: ClockSpec{NominalRate: 16000, Quanta: []int{480}}},
		{Render: ClockSpec{NominalRate: 16000, Quanta: []int{480}, DriftPPM: -1_000_000}, Capture: ClockSpec{NominalRate: 16000, Quanta: []int{480}}},
		{Render: ClockSpec{NominalRate: 16000, Quanta: []int{480}}, Capture: ClockSpec{NominalRate: 16000, Quanta: []int{480}}, CaptureQueue: QueueSpec{DropPolicy: "mystery"}},
	}
	for i, scenario := range invalid {
		if _, err := NewSimulatedDuplexRegistry(scenario); err == nil {
			t.Fatalf("invalid scenario %d accepted", i)
		}
	}
	if r, _ := NewSimulatedDuplexRegistry(simulatedScenario(16000, []int{480})); r.Advance(-1) == nil {
		t.Fatal("negative advance accepted")
	}
	s := simulatedScenario(16000, []int{480})
	s.Faults = []FaultEvent{{Callback: 0, Direction: DirectionOutput, Type: FaultForwardJump, Samples: 10}, {Callback: 1, Direction: DirectionOutput, Type: FaultBackwardJump}, {Callback: 0, Direction: DirectionInput, Type: FaultClockReset}}
	r, out := openSimulatedOutput(t, s)
	_ = out.WriteSamples(context.Background(), make([]int16, 960))
	if err := r.Advance(2); err != nil {
		t.Fatal(err)
	}
	trace := r.Trace()
	if trace[0].StartSample != 10 || trace[1].ClockEpoch != 1 {
		t.Fatalf("timeline trace = %+v", trace)
	}
}

func simulatedScenario(rate int, quanta []int) DuplexScenario {
	return DuplexScenario{Seed: 7, Render: ClockSpec{NominalRate: rate, Quanta: quanta}, Capture: ClockSpec{NominalRate: rate, Quanta: quanta}}
}
func openSimulatedOutput(t *testing.T, scenario DuplexScenario) (*SimulatedDuplexRegistry, *SimulatedDuplexStream) {
	t.Helper()
	r, out, _ := openSimulatedPair(t, scenario)
	return r, out
}
func openSimulatedPair(t *testing.T, scenario DuplexScenario) (*SimulatedDuplexRegistry, *SimulatedDuplexStream, *SimulatedDuplexStream) {
	t.Helper()
	r, err := NewSimulatedDuplexRegistry(scenario)
	if err != nil {
		t.Fatal(err)
	}
	outRaw, err := r.Open(r.output.ID)
	if err != nil {
		t.Fatal(err)
	}
	inRaw, err := r.Open(r.input.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := outRaw.(*SimulatedDuplexStream)
	in := inRaw.(*SimulatedDuplexStream)
	t.Cleanup(func() { _ = out.Close(); _ = in.Close() })
	return r, out, in
}
func allZero(samples []int16) bool {
	for _, v := range samples {
		if v != 0 {
			return false
		}
	}
	return true
}
func containsFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}
func fmtIntSlice(values []int) string {
	result := ""
	for i, v := range values {
		if i > 0 {
			result += "_"
		}
		result += fmt.Sprint(v)
	}
	return result
}
