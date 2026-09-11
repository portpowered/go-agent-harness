package integration

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// TestRemoteToolAudioSlowDeviceEdgeOracleControl exercises the same remote
// device, callback cadence, and terminal-marker oracle as the failing agent
// scenario, while removing provider/session lifecycle ordering from the path.
// A passing control localizes a missing sample run to the upstream delivery
// boundary instead of allowing a device underflow to be mistaken for fixture
// or oracle behavior.
func TestRemoteToolAudioSlowDeviceEdgeOracleControl(t *testing.T) {
	want := remoteToolAudioSlowDeviceControlPCM(t)
	if len(want) < audio.FrameSize {
		t.Fatalf("expected control audio to contain at least one frame: %d samples", len(want))
	}

	endpoint, stopDevice := startAudioDeviceServerBinary(t, true)
	defer stopDevice()

	sink := newRemoteToolAudioEdgeSink(t, endpoint)
	defer func() {
		if err := sink.Close(); err != nil {
			t.Errorf("close remote device edge sink: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Queue one frame before the first clock advance so the control measures
	// delivery/observation rather than an intentional initial underflow.
	queueRemoteToolAudioControlFrame(t, ctx, sink, want[:audio.FrameSize])
	writeDone := make(chan error, 1)
	go func() { writeDone <- writeRemoteToolAudioControl(ctx, sink, want[audio.FrameSize:]) }()

	callbackAdvances := &atomic.Uint64{}
	snapshot, err := waitForRemoteToolAudio(
		ctx,
		endpoint,
		want,
		45*time.Millisecond,
		callbackAdvances,
	)
	if err != nil {
		cancel()
		<-writeDone
		t.Fatalf("remote device edge control: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("queue remaining control audio: %v", err)
	}

	if err := verifyRemoteToolAudio(nonzeroRemoteToolAudio(snapshot.RenderedSamples), want); err != nil {
		t.Fatalf("remote device edge control PCM: %v", err)
	}
	if snapshot.Playback.DroppedSamples != 0 || snapshot.Playback.OverflowEvents != 0 || snapshot.Playback.DiscardedSamples != 0 {
		t.Fatalf("remote device edge control discarded audio: %+v", snapshot.Playback)
	}
}

func remoteToolAudioSlowDeviceControlPCM(t *testing.T) []int16 {
	t.Helper()
	responses := make([][]int16, 9)
	for index, count := range []int{46800, 0, 48000, 55200, 0, 0, 0, 0, 111600} {
		if count > 0 {
			responses[index] = remoteToolAudioPCM(count, int16(900+index*2200))
		}
	}
	return remoteToolAudioExpected(t, responses)
}

func newRemoteToolAudioEdgeSink(t *testing.T, endpoint string) *devicegw.DeviceSink {
	t.Helper()
	registry, err := devicegw.NewRemoteDeviceRegistry(endpoint)
	if err != nil {
		t.Fatalf("connect to remote device: %v", err)
	}
	output, err := registry.Default(devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("resolve default remote output: %v", err)
	}
	sink, err := devicegw.NewDeviceSinkAtRate(registry, output.ID, audio.SampleRate)
	if err != nil {
		t.Fatalf("create remote output sink: %v", err)
	}
	return sink
}

func queueRemoteToolAudioControlFrame(t *testing.T, ctx context.Context, sink *devicegw.DeviceSink, frame []int16) {
	t.Helper()
	if err := sink.WriteSamples(ctx, frame); err != nil {
		t.Fatalf("queue first control frame: %v", err)
	}
}

func writeRemoteToolAudioControl(ctx context.Context, sink *devicegw.DeviceSink, samples []int16) error {
	for offset := 0; offset < len(samples); offset += audio.FrameSize {
		end := offset + audio.FrameSize
		if end > len(samples) {
			end = len(samples)
		}
		if err := sink.WaitForPlaybackCapacity(ctx, end-offset); err != nil {
			return err
		}
		if err := sink.WriteSamples(ctx, samples[offset:end]); err != nil {
			return err
		}
	}
	return nil
}

func waitForRemoteToolAudio(ctx context.Context, endpoint string, want []int16, callbackInterval time.Duration, callbackAdvances *atomic.Uint64) (devicegw.DeviceServerSnapshot, error) {
	// Keep the external callback clock alive until the expected PCM suffix crosses
	// the device edge; a fixed callback budget races coverage-instrumented agents.
	markerSamples := audio.FrameSize
	if markerSamples > len(want) {
		markerSamples = len(want)
	}
	finalMarker := want[len(want)-markerSamples:]
	const callbacksPerSnapshot = 32
	ticker := time.NewTicker(callbackInterval)
	defer ticker.Stop()
	callbacksSinceSnapshot := callbacksPerSnapshot
	for {
		if callbacksSinceSnapshot >= callbacksPerSnapshot {
			snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(ctx, endpoint)
			if err != nil {
				return devicegw.DeviceServerSnapshot{}, fmt.Errorf("read remote device evidence: %w", err)
			}
			got := nonzeroRemoteToolAudio(snapshot.RenderedSamples)
			if remoteToolAudioHasSuffix(got, finalMarker) && snapshot.Playback.QueuedSamples == 0 {
				return snapshot, nil
			}
			callbacksSinceSnapshot = 0
		}
		select {
		case <-ticker.C:
			if err := devicegw.AdvanceRemoteDeviceServer(ctx, endpoint, 1); err != nil {
				return devicegw.DeviceServerSnapshot{}, fmt.Errorf("advance remote playback while awaiting final marker: %w", err)
			}
			callbackAdvances.Add(1)
			callbacksSinceSnapshot++
		case <-ctx.Done():
			return devicegw.DeviceServerSnapshot{}, fmt.Errorf("remote playback did not reach final PCM marker before the scenario deadline: %w", ctx.Err())
		}
	}
}

func requireRemoteToolAudio(t *testing.T, ctx context.Context, endpoint string, want []int16, callbackInterval time.Duration, callbackAdvances *atomic.Uint64, provider *remoteToolAudioProvider, expectedToolCalls int, expected []int16, done <-chan error, stderr *remoteToolAudioBuffer) devicegw.DeviceServerSnapshot {
	t.Helper()
	snapshot, err := waitForRemoteToolAudio(ctx, endpoint, want, callbackInterval, callbackAdvances)
	if err != nil {
		t.Fatalf("remote playback wait failed: %v; %s", err, remoteToolAudioFailureEvidence(ctx, endpoint, provider, expectedToolCalls, expected, done, stderr, callbackAdvances))
	}
	return snapshot
}

type remoteToolAudioBuffer struct {
	mu               sync.Mutex
	buf              bytes.Buffer
	callbackAdvances atomic.Uint64
}

func (b *remoteToolAudioBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *remoteToolAudioBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func remoteToolAudioEnvironment(base []string, fixturePath string, holdToneControl bool) []string {
	environment := append(base,
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
	)
	if fixturePath != "" {
		environment = append(environment, "YUI_E2E_TOOL_MOCK_FIXTURE="+fixturePath)
	}
	// Exact provider-PCM scenarios exclude the independently synthesized cue.
	// The forced-gap control retains the default and proves the cue separately.
	if holdToneControl {
		return append(environment, "YUI_E2E_DISABLE_HOLD_TONE=0")
	}
	return append(environment, "YUI_E2E_DISABLE_HOLD_TONE=1")
}

func assertRemoteToolAudioScenario(t *testing.T, testCase remoteToolAudioCase, got, want []int16, snapshot devicegw.DeviceServerSnapshot, provider *remoteToolAudioProvider) {
	t.Helper()
	if testCase.holdToneControl {
		assertRemoteToolAudioHoldToneControl(t, got, want)
		return
	}
	if err := verifyRemoteToolAudio(got, want); err != nil {
		t.Fatalf("%s process-boundary audio verification: %v (playback=%+v provider=%+v)", testCase.name, err, snapshot.Playback, provider.Snapshot())
	}
}

// TestAgentBinaryDefaultHoldToneIsSeparateFromProviderPCM forces a gap beyond
// the production cue threshold. The remote device must observe both complete
// provider responses plus independent local cue samples. This is the control
// that permits exact-delivery scenarios to disable the cue explicitly instead
// of weakening their provider-only PCM oracle.
func TestAgentBinaryDefaultHoldToneIsSeparateFromProviderPCM(t *testing.T) {
	testCase := remoteToolAudioCase{
		name:            "default_hold_tone_control",
		responseSamples: []int{remoteToolAudioDeltaSamples, remoteToolAudioDeltaSamples},
		toolResponses:   map[int]bool{0: true},
		providerClose:   true,
		holdToneControl: true,
	}
	t.Run("default_cue", func(t *testing.T) {
		runRemoteToolAudioScenario(t, testCase, 0, 3*time.Second, time.Millisecond, 0, 0, 0)
	})
	t.Run("provider_only_fixture", func(t *testing.T) {
		testCase.name = "provider_only_hold_tone_policy"
		testCase.holdToneControl = false
		runRemoteToolAudioScenario(t, testCase, 0, 3*time.Second, time.Millisecond, 0, 0, 0)
	})
}

func assertRemoteToolAudioHoldToneControl(t *testing.T, got, want []int16) {
	t.Helper()
	if err := verifyRemoteToolAudioHoldToneControl(got, want); err != nil {
		t.Fatal(err)
	}
}

func verifyRemoteToolAudioHoldToneControl(got, want []int16) error {
	if len(got) <= len(want) {
		return fmt.Errorf("hold-tone control rendered %d samples for %d provider samples, want an independent local cue", len(got), len(want))
	}
	wantIndex := 0
	cue := make([]int16, 0, len(got)-len(want))
	// Cue and provider workers share the paced device writer, so a pulse tail
	// may be interleaved until the next real frame resets the gap filler.
	for _, sample := range got {
		if wantIndex < len(want) && sample == want[wantIndex] {
			wantIndex++
			continue
		}
		cue = append(cue, sample)
	}
	if wantIndex != len(want) {
		return fmt.Errorf("hold-tone control changed provider PCM: preserved %d/%d ordered samples", wantIndex, len(want))
	}
	amplitude := int(audio.DefaultHoldToneConfig().Amplitude)
	hasPositive, hasNegative := false, false
	for _, sample := range cue {
		value := int(sample)
		if value > amplitude || value < -amplitude {
			return fmt.Errorf("hold-tone control emitted out-of-profile sample %d", sample)
		}
		hasPositive = hasPositive || sample > 0
		hasNegative = hasNegative || sample < 0
	}
	if !hasPositive || !hasNegative {
		return fmt.Errorf("hold-tone control cue lacks the two-tone waveform polarity")
	}
	return nil
}

// TestToolAudioHoldToneControlRejectsProviderDuplication proves the cue control
// cannot relabel repeated provider PCM as legitimate local filler.
func TestToolAudioHoldToneControlRejectsProviderDuplication(t *testing.T) {
	first := remoteToolAudioPCM(4*audio.FrameSize, 900)
	second := remoteToolAudioPCM(4*audio.FrameSize, 3100)
	want := append(append([]int16(nil), first...), second...)
	got := append(append(append([]int16(nil), first...), first[:audio.FrameSize]...), second...)
	if err := verifyRemoteToolAudioHoldToneControl(got, want); err == nil {
		t.Fatal("hold-tone control accepted duplicated provider PCM as a local cue")
	}
}

// TestToolAudioDeviceVerifierRejectsEdgeDamage red-teams the device-edge
// oracle itself. These controls mutate only the PCM a faulty external device
// would report; every loss, duplication, reorder, and corruption must fail.
func TestToolAudioDeviceVerifierRejectsEdgeDamage(t *testing.T) {
	want := remoteToolAudioPCM(20*audio.FrameSize, 1200)
	controls := []struct {
		name   string
		mutate func([]int16) []int16
	}{
		{name: "retain_10_percent", mutate: retainRemoteToolAudio(10)},
		{name: "retain_25_percent", mutate: retainRemoteToolAudio(25)},
		{name: "retain_49_percent", mutate: retainRemoteToolAudio(49)},
		{name: "retain_50_percent", mutate: retainRemoteToolAudio(50)},
		{name: "retain_75_percent", mutate: retainRemoteToolAudio(75)},
		{name: "retain_90_percent", mutate: retainRemoteToolAudio(90)},
		{name: "retain_99_percent", mutate: retainRemoteToolAudio(99)},
		{name: "drop_first_sample", mutate: dropRemoteToolAudioSpan(0, 1)},
		{name: "drop_last_sample", mutate: dropRemoteToolAudioSpan(len(want)-1, 1)},
		{name: "drop_first_frame", mutate: dropRemoteToolAudioSpan(0, audio.FrameSize)},
		{name: "drop_middle_frame", mutate: dropRemoteToolAudioSpan(10*audio.FrameSize, audio.FrameSize)},
		{name: "drop_last_frame", mutate: dropRemoteToolAudioSpan(19*audio.FrameSize, audio.FrameSize)},
		{name: "duplicate_first_frame", mutate: duplicateRemoteToolAudioSpan(0, audio.FrameSize)},
		{name: "duplicate_middle_frame", mutate: duplicateRemoteToolAudioSpan(10*audio.FrameSize, audio.FrameSize)},
		{name: "duplicate_last_frame", mutate: duplicateRemoteToolAudioSpan(19*audio.FrameSize, audio.FrameSize)},
		{name: "swap_first_frames", mutate: swapRemoteToolAudioFrames(0, 1)},
		{name: "swap_middle_frames", mutate: swapRemoteToolAudioFrames(9, 10)},
		{name: "corrupt_first_sample", mutate: corruptRemoteToolAudioSample(0)},
		{name: "corrupt_middle_sample", mutate: corruptRemoteToolAudioSample(len(want) / 2)},
		{name: "corrupt_last_sample", mutate: corruptRemoteToolAudioSample(len(want) - 1)},
	}
	for _, control := range controls {
		t.Run(control.name, func(t *testing.T) {
			got := control.mutate(append([]int16(nil), want...))
			if err := verifyRemoteToolAudio(got, want); err == nil {
				t.Fatal("device-edge verifier accepted intentionally damaged PCM")
			}
		})
	}
}

func TestToolAudioFinalMarkerCannotBeImpersonatedByKnownTruncations(t *testing.T) {
	responses := make([][]int16, 9)
	for index, count := range []int{38400, 0, 66000, 66000, 0, 0, 0, 0, 96000} {
		if count > 0 {
			responses[index] = remoteToolAudioPCM(count, int16(900+index*2200))
		}
	}
	want := remoteToolAudioExpected(t, responses)
	marker := want[len(want)-audio.FrameSize:]
	for _, lost := range []int{1, audio.FrameSize, 6400, 40160, len(want) / 2} {
		t.Run(fmt.Sprintf("lost_%d", lost), func(t *testing.T) {
			truncated := want[:len(want)-lost]
			if remoteToolAudioHasSuffix(truncated, marker) {
				t.Fatalf("truncated device PCM falsely matched the final marker after losing %d samples", lost)
			}
		})
	}
}

func retainRemoteToolAudio(percent int) func([]int16) []int16 {
	return func(samples []int16) []int16 { return samples[:len(samples)*percent/100] }
}

func dropRemoteToolAudioSpan(start, count int) func([]int16) []int16 {
	return func(samples []int16) []int16 {
		return append(samples[:start:start], samples[start+count:]...)
	}
}

func duplicateRemoteToolAudioSpan(start, count int) func([]int16) []int16 {
	return func(samples []int16) []int16 {
		result := make([]int16, 0, len(samples)+count)
		result = append(result, samples[:start+count]...)
		result = append(result, samples[start:start+count]...)
		return append(result, samples[start+count:]...)
	}
}

func swapRemoteToolAudioFrames(first, second int) func([]int16) []int16 {
	return func(samples []int16) []int16 {
		firstStart := first * audio.FrameSize
		secondStart := second * audio.FrameSize
		firstFrame := append([]int16(nil), samples[firstStart:firstStart+audio.FrameSize]...)
		copy(samples[firstStart:firstStart+audio.FrameSize], samples[secondStart:secondStart+audio.FrameSize])
		copy(samples[secondStart:secondStart+audio.FrameSize], firstFrame)
		return samples
	}
}

func corruptRemoteToolAudioSample(index int) func([]int16) []int16 {
	return func(samples []int16) []int16 {
		samples[index] = -samples[index]
		return samples
	}
}
