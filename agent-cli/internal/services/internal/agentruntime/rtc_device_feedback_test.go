package agentruntime

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	selfhearing "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

func TestLocalFeedbackGateSuppressesLoopAndWarnsOnce(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := audio.NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), audio.SampleRate, audio.SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	playback := make([][]int16, 5)
	for frameIndex := range playback {
		playback[frameIndex] = feedbackSignal(frameIndex, 17)
		if err := gate.WritePlayback(context.Background(), playback[frameIndex], func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
	}
	for frameIndex, want := range playback {
		released, err := gate.FilterCapture(context.Background(), want)
		if err != nil {
			t.Fatalf("filter looped capture frame %d: %v", frameIndex, err)
		}
		if len(released) != 0 {
			t.Fatalf("looped capture frame %d released %d frames", frameIndex, len(released))
		}
	}
	select {
	case got := <-warning:
		if !strings.Contains(got, "Acoustic feedback detected") || !strings.Contains(got, "headphones") || !strings.Contains(got, "file") {
			t.Fatalf("warning = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for feedback warning")
	}
	for frameIndex := range playback {
		if err := gate.WritePlayback(context.Background(), playback[frameIndex], func() error { return nil }); err != nil {
			t.Fatalf("observe repeated playback frame %d: %v", frameIndex, err)
		}
		released, err := gate.FilterCapture(context.Background(), playback[frameIndex])
		if err != nil {
			t.Fatalf("filter repeated looped capture frame %d: %v", frameIndex, err)
		}
		if len(released) != 0 {
			t.Fatalf("repeated looped capture frame %d released %d frames", frameIndex, len(released))
		}
	}
	select {
	case extra := <-warning:
		t.Fatalf("repeated feedback emitted another warning %q", extra)
	default:
	}
	if err := gate.Close(); err != nil {
		t.Fatalf("close feedback gate: %v", err)
	}
	_, err = gate.FilterCapture(context.Background(), playback[0])
	if !errors.Is(err, contract.ErrClosed) {
		t.Fatalf("filter after close = %v", err)
	}
}

func TestLocalFeedbackGateNonIntegralDeviceQuantumStaysMonotonic(t *testing.T) {
	for _, test := range []struct {
		name          string
		rate, samples int
	}{
		{name: "coreaudio_44k1", rate: 44100, samples: 480},
		{name: "coreaudio_48k_variable_quantum", rate: 48000, samples: 683},
	} {
		t.Run(test.name, func(t *testing.T) { assertNonIntegralFeedbackQuantum(t, test.rate, test.samples) })
	}
}

func assertNonIntegralFeedbackQuantum(t *testing.T, rate, samples int) {
	t.Helper()
	gate, err := audio.NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), io.Discard, rate, rate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := gate.Close(); closeErr != nil {
			t.Errorf("close feedback gate: %v", closeErr)
		}
	})
	frame := make([]int16, samples)
	seed := feedbackSignal(0, 113)
	for index := range frame {
		frame[index] = seed[index%len(seed)]
	}
	for index := 0; index < 64; index++ {
		if err := gate.WritePlayback(context.Background(), frame, func() error { return nil }); err != nil {
			t.Fatalf("playback callback %d: %v", index, err)
		}
	}
	want := time.Duration((int64(samples)*int64(time.Second)+int64(rate)/2)/int64(rate)) * 64
	if gate.PlaybackPosition() != want {
		t.Fatalf("playback position=%s, want %s", gate.PlaybackPosition(), want)
	}
}

func TestLocalFeedbackGateFeedbackConfirmedTracksWarningState(t *testing.T) {
	var nilGate *audio.PCM16FeedbackGate
	if nilGate.FeedbackConfirmed() {
		t.Fatal("nil gate reported confirmed feedback")
	}
	warning := make(chan string, 1)
	gate, err := audio.NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), audio.SampleRate, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := gate.Close(); closeErr != nil {
			t.Errorf("close feedback gate: %v", closeErr)
		}
	})
	if gate.FeedbackConfirmed() {
		t.Fatal("feedback confirmed before evidence")
	}
	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		loop := feedbackSignal(frameIndex, 31)
		if err := gate.WritePlayback(context.Background(), loop, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := gate.FilterCapture(context.Background(), loop); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-warning:
	case <-time.After(time.Second):
		t.Fatal("feedback was never confirmed")
	}
	if !gate.FeedbackConfirmed() {
		t.Fatal("feedback confirmation was not retained")
	}
	for frameIndex := 5; frameIndex < 15; frameIndex++ {
		if _, err := gate.FilterCapture(context.Background(), feedbackSignal(frameIndex, 71)); err != nil {
			t.Fatal(err)
		}
	}
	if !gate.FeedbackConfirmed() {
		t.Fatal("feedback confirmation reset after idle")
	}
}

func TestLocalFeedbackGateReanchorsCaptureAfterPrePlaybackLead(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := audio.NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), audio.SampleRate, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	for frameIndex := 0; frameIndex < 20; frameIndex++ {
		released, err := gate.FilterCapture(context.Background(), feedbackSignal(frameIndex, 73))
		if err != nil || len(released) != 1 {
			t.Fatalf("pre-playback frame %d released=%d err=%v", frameIndex, len(released), err)
		}
	}
	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		loop := feedbackSignal(frameIndex, 17)
		if err := gate.WritePlayback(context.Background(), loop, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		released, err := gate.FilterCapture(context.Background(), loop)
		if err != nil || len(released) != 0 {
			t.Fatalf("looped frame %d released=%d err=%v", frameIndex, len(released), err)
		}
	}
	select {
	case got := <-warning:
		if !strings.Contains(got, "Acoustic feedback detected") {
			t.Fatalf("warning = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-playback capture lead prevented confirmation")
	}
}

func TestLocalFeedbackGateReleasesIndependentCaptureOnceInOrder(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := audio.NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), feedbackWarningChannel(warning), audio.SampleRate, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		if err := gate.WritePlayback(context.Background(), feedbackSignal(frameIndex, 23), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	want := make([][]int16, 5)
	var got [][]int16
	for frameIndex := range want {
		want[frameIndex] = feedbackSignal(frameIndex, 71)
		released, err := gate.FilterCapture(context.Background(), want[frameIndex])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, released...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("released frames = %#v, want %#v", got, want)
	}
	select {
	case extra := <-warning:
		t.Fatalf("independent capture emitted feedback warning %q", extra)
	default:
	}
}

func TestLocalFeedbackGateBlockedWarningWriterCannotBlockMedia(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	gate, err := audio.NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), blockingFeedbackWarningWriter{started: started, release: release}, audio.SampleRate, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		if closeErr := gate.Close(); closeErr != nil {
			t.Errorf("close feedback gate: %v", closeErr)
		}
	})
	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		if err := gate.WritePlayback(context.Background(), feedbackSignal(frameIndex, 17), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := gate.FilterCapture(context.Background(), feedbackSignal(frameIndex, 17)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for warning writer")
	}
	mediaDone := make(chan error, 1)
	go func() {
		_, filterErr := gate.FilterCapture(context.Background(), feedbackSignal(5, 17))
		mediaDone <- filterErr
	}()
	select {
	case filterErr := <-mediaDone:
		if filterErr != nil {
			t.Fatalf("capture classification failed: %v", filterErr)
		}
	case <-time.After(time.Second):
		t.Fatal("capture classification blocked behind warning writer")
	}
}

func feedbackDeviceDurationAtRate(samples, rate int) time.Duration {
	if samples <= 0 || rate <= 0 {
		return 0
	}
	return time.Duration((int64(samples)*int64(time.Second) + int64(rate)/2) / int64(rate))
}

func feedbackSignal(frameIndex, seed int) []int16 {
	samples := make([]int16, audio.FrameSize)
	state := uint32(seed*7919 + frameIndex*104729 + 1)
	for index := range samples {
		state = state*1664525 + 1013904223
		samples[index] = int16(int32(state>>16)%24000 - 12000)
	} //nolint:gosec // bounded deterministic PCM fixture
	return samples
}

func feedbackWarningChannel(warning chan<- string) *feedbackChannelWriter {
	return &feedbackChannelWriter{warning: warning}
}

type feedbackChannelWriter struct{ warning chan<- string }

func (w *feedbackChannelWriter) Write(data []byte) (int, error) {
	w.warning <- string(data)
	return len(data), nil
}

type blockingFeedbackWarningWriter struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (w blockingFeedbackWarningWriter) Write(data []byte) (int, error) {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.release
	return len(data), nil
}

type feedbackOutbound struct{ frames chan audio.PCMFrame }

func (m *feedbackOutbound) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	select {
	case m.frames <- audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *feedbackOutbound) Close() error { return nil }

var _ audio.OutboundMedia = (*feedbackOutbound)(nil)

type testRTCBinding struct {
	inferencer messages.SessionInferencer
	errors     <-chan error
}

func (b *testRTCBinding) Inferencer() messages.SessionInferencer { return b.inferencer }
func (b *testRTCBinding) Errors() <-chan error                   { return b.errors }
func (*testRTCBinding) Close() error                             { return nil }

var _ runtimedevices.RTCBinding = (*testRTCBinding)(nil)
