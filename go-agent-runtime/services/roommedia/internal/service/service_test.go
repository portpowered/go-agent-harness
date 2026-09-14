package service

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type testMixer struct {
	format roommedia.PCM16Format
	frames []roommedia.MixedFrame
	errs   []error
	index  int
}

func (m *testMixer) Format() roommedia.PCM16Format { return m.format }

func (m *testMixer) ReadFrameWithSources(context.Context) (roommedia.MixedFrame, error) {
	if m.index < len(m.frames) {
		frame := m.frames[m.index]
		m.index++
		frame.PCM = append([]byte(nil), frame.PCM...)
		frame.Sources = append([]string(nil), frame.Sources...)
		return frame, nil
	}
	if len(m.errs) > 0 {
		err := m.errs[0]
		m.errs = m.errs[1:]
		return roommedia.MixedFrame{}, err
	}
	return roommedia.MixedFrame{}, io.EOF
}

type testInput struct {
	frames [][]int16
	index  int
}

func (i *testInput) ReadFrame(_ context.Context, destination []int16) error {
	if i.index >= len(i.frames) {
		return io.EOF
	}
	frame := i.frames[i.index]
	i.index++
	if len(destination) != len(frame) {
		return errors.New("capture frame size mismatch")
	}
	copy(destination, frame)
	return nil
}

type testOutput struct {
	mu       sync.Mutex
	frames   [][]int16
	err      error
	writeCnt int
}

func (o *testOutput) WriteFrame(_ context.Context, frame []int16) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.writeCnt++
	if o.err != nil {
		return o.err
	}
	o.frames = append(o.frames, append([]int16(nil), frame...))
	return nil
}

func (o *testOutput) snapshot() [][]int16 {
	o.mu.Lock()
	defer o.mu.Unlock()
	frames := make([][]int16, len(o.frames))
	for index := range o.frames {
		frames[index] = append([]int16(nil), o.frames[index]...)
	}
	return frames
}

type testClock struct{ now time.Time }

func (c testClock) Now() time.Time { return c.now }

func TestPumpProviderInputPreservesConversionAndDispositionOrder(t *testing.T) {
	t.Parallel()

	inputSamples := []int16{1000, -1000, 2000, -2000, 3000, -3000}
	inputPCM := codec.EncodePCM16(inputSamples)
	mixer := &testMixer{
		format: roommedia.PCM16Format{SampleRate: 24000, Channels: 1},
		frames: []roommedia.MixedFrame{{PCM: inputPCM, Sources: []string{"peer-a", "peer-b"}}},
	}
	service := New(Dependencies{})
	var events []string
	var gotPolicy roommedia.InputPolicy
	var gotPCM []byte
	var gotSources []string
	acks := make(chan struct{}, 1)
	err := service.PumpProviderInput(context.Background(), roommedia.ProviderInputRequest{
		Mixer:              mixer,
		ParticipantID:      "participant-a",
		ProviderSampleRate: 16000,
		Send: func(_ context.Context, pcm []byte, policy roommedia.InputPolicy) error {
			events = append(events, "send")
			gotPCM = append([]byte(nil), pcm...)
			gotPolicy = policy
			pcm[0] = 0
			return nil
		},
		Policy: func(sources []string) roommedia.InputPolicy {
			gotSources = append([]string(nil), sources...)
			return roommedia.InputPolicyDoNotInterrupt
		},
		Resolve: func(sources []string, byteCount int, reason string) {
			events = append(events, "resolve:"+reason)
			if !reflect.DeepEqual(sources, []string{"peer-a", "peer-b"}) || byteCount != len(inputPCM) {
				t.Errorf("resolve=(%v,%d,%q), want original sources and byte count", sources, byteCount, reason)
			}
		},
		Observe: func(participantID string, pcm []byte) error {
			events = append(events, "observe")
			if participantID != "participant-a" {
				t.Errorf("observe participant=%q", participantID)
			}
			if pcm[0] == 0 {
				t.Error("observe received the mutable Send buffer")
			}
			return nil
		},
		ReplayAcks: acks,
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("PumpProviderInput error=%v, want EOF", err)
	}
	expectedSamples, resampleErr := audio.ResamplePCM16(inputSamples, 24000, 16000)
	if resampleErr != nil {
		t.Fatal(resampleErr)
	}
	if !reflect.DeepEqual(gotPCM, codec.EncodePCM16(expectedSamples)) {
		t.Fatalf("provider PCM=%v, want converted=%v", gotPCM, codec.EncodePCM16(expectedSamples))
	}
	if gotPolicy != roommedia.InputPolicyDoNotInterrupt {
		t.Fatalf("policy=%q, want do_not_interrupt", gotPolicy)
	}
	if !reflect.DeepEqual(gotSources, []string{"peer-a", "peer-b"}) {
		t.Fatalf("policy sources=%v", gotSources)
	}
	if got := len(acks); got != 1 {
		t.Fatalf("replay acknowledgements=%d, want 1", got)
	}
	if !reflect.DeepEqual(events, []string{"send", "resolve:", "observe"}) {
		t.Fatalf("events=%v, want send -> resolve -> observe", events)
	}
}

func TestPumpProviderInputRejectsOnceAndPreservesErrorIdentity(t *testing.T) {
	t.Parallel()

	sendErr := errors.New("provider transport rejected frame")
	pcm := codec.EncodePCM16([]int16{123, -456})
	var rejected []byte
	var rejectedErr error
	var resolved []string
	service := New(Dependencies{})
	err := service.PumpProviderInput(context.Background(), roommedia.ProviderInputRequest{
		Mixer: &testMixer{
			format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1},
			frames: []roommedia.MixedFrame{{PCM: pcm, Sources: []string{"peer-a"}}},
		},
		Send: func(context.Context, []byte, roommedia.InputPolicy) error { return sendErr },
		ObserveRejected: func(got []byte, err error) {
			rejected = append([]byte(nil), got...)
			rejectedErr = err
		},
		Resolve: func(_ []string, _ int, reason string) { resolved = append(resolved, reason) },
	})
	if !errors.Is(err, sendErr) {
		t.Fatalf("error=%v, want wrapped send error", err)
	}
	if !errors.Is(rejectedErr, sendErr) || !reflect.DeepEqual(rejected, pcm) {
		t.Fatalf("rejected=(%v,%v), want original PCM and send error", rejected, rejectedErr)
	}
	if !reflect.DeepEqual(resolved, []string{roommedia.ProviderInputRejectedReason}) {
		t.Fatalf("resolve reasons=%v", resolved)
	}
}

func TestConvertProviderInputRejectsTruncatedPCMAndCopiesIdentity(t *testing.T) {
	t.Parallel()

	service := New(Dependencies{})
	if _, err := service.ConvertProviderInput([]byte{1}, roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, 0); !errors.Is(err, roommedia.ErrPCM16Truncated) {
		t.Fatalf("truncated error=%v, want ErrPCM16Truncated", err)
	}
	input := []byte{1, 2, 3, 4}
	converted, err := service.ConvertProviderInput(input, roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = 99
	if converted[0] == input[0] {
		t.Error("identity conversion retained caller-owned input")
	}
}

func TestCaptureHumanFansOutConvertedFramesAndSkipsInactiveOrSelf(t *testing.T) {
	t.Parallel()

	inputSamples := make([]int16, 480)
	for index := range inputSamples {
		inputSamples[index] = int16(index - 200)
	}
	input := &testInput{frames: [][]int16{inputSamples}}
	mixer := &testMixer{format: roommedia.PCM16Format{SampleRate: 24000, Channels: 1}}
	service := New(Dependencies{})
	var sent []byte
	type fanout struct {
		id  string
		pcm []byte
	}
	var fanned []fanout
	err := service.CaptureHuman(context.Background(), roommedia.HumanCaptureRequest{
		ParticipantID:   "self",
		Input:           input,
		Mixer:           mixer,
		InputSampleRate: 16000,
		FrameSamples:    480,
		Targets: func() []roommedia.FanoutTarget {
			return []roommedia.FanoutTarget{
				{ID: "self", Format: roommedia.PCM16Format{SampleRate: 24000, Channels: 1}, Active: func() bool { return true }, Write: func(context.Context, string, []byte) error { t.Error("self target received audio"); return nil }},
				{ID: "peer-24", Format: roommedia.PCM16Format{SampleRate: 24000, Channels: 1}, Active: func() bool { return true }, Write: func(_ context.Context, source string, pcm []byte) error {
					fanned = append(fanned, fanout{source + ":peer-24", append([]byte(nil), pcm...)})
					return nil
				}},
				{ID: "peer-16", Format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, Active: func() bool { return true }, Write: func(_ context.Context, source string, pcm []byte) error {
					fanned = append(fanned, fanout{source + ":peer-16", append([]byte(nil), pcm...)})
					return nil
				}},
				{ID: "inactive", Format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, Active: func() bool { return false }, Write: func(context.Context, string, []byte) error { t.Error("inactive target received audio"); return nil }},
			}
		},
		ObserveSent: func(pcm []byte) { sent = append([]byte(nil), pcm...) },
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("CaptureHuman error=%v, want EOF", err)
	}
	expected24, err := audio.ResamplePCM16(inputSamples, 16000, 24000)
	if err != nil {
		t.Fatal(err)
	}
	expected16 := codec.EncodePCM16(inputSamples)
	if !reflect.DeepEqual(sent, codec.EncodePCM16(expected24)) {
		t.Fatal("sent frame was not converted to mixer rate")
	}
	if len(fanned) != 2 || fanned[0].id != "self:peer-24" || fanned[1].id != "self:peer-16" {
		t.Fatalf("fan-out=%v, want peer-24 then peer-16", fanned)
	}
	if !reflect.DeepEqual(fanned[0].pcm, codec.EncodePCM16(expected24)) || !reflect.DeepEqual(fanned[1].pcm, expected16) {
		t.Fatal("fan-out formats were not preserved")
	}
}

func TestCaptureHumanTreatsPeerTerminationAsNormalTeardown(t *testing.T) {
	t.Parallel()

	peerErr := errors.New("peer write failed")
	active := true
	service := New(Dependencies{})
	err := service.CaptureHuman(context.Background(), roommedia.HumanCaptureRequest{
		ParticipantID: "self",
		Input:         &testInput{frames: [][]int16{make([]int16, 480)}},
		Mixer:         &testMixer{format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}},
		Targets: func() []roommedia.FanoutTarget {
			return []roommedia.FanoutTarget{{ID: "peer", Format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, Active: func() bool { return active }, Write: func(context.Context, string, []byte) error { active = false; return peerErr }}}
		},
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("inactive peer error=%v, want EOF from input", err)
	}
}

func TestCaptureHumanReturnsActivePeerWriteError(t *testing.T) {
	t.Parallel()

	peerErr := errors.New("peer write failed")
	service := New(Dependencies{})
	err := service.CaptureHuman(context.Background(), roommedia.HumanCaptureRequest{
		ParticipantID: "self",
		Input:         &testInput{frames: [][]int16{make([]int16, 480)}},
		Mixer:         &testMixer{format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}},
		Targets: func() []roommedia.FanoutTarget {
			return []roommedia.FanoutTarget{{ID: "peer", Format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, Active: func() bool { return true }, Write: func(context.Context, string, []byte) error { return peerErr }}}
		},
	})
	if !errors.Is(err, peerErr) {
		t.Fatalf("active peer error=%v, want peer error", err)
	}
	if !strings.Contains(err.Error(), "receive fan out human PCM from self") {
		t.Fatalf("error=%v, want source context", err)
	}
}

func TestOutputBufferRetainsPartialDeviceQuantum(t *testing.T) {
	t.Parallel()

	output := &testOutput{}
	service := New(Dependencies{})
	buffer, err := service.NewOutputBuffer(roommedia.OutputBufferRequest{
		Output:           output,
		Format:           roommedia.PCM16Format{SampleRate: 24000, Channels: 1},
		TargetSampleRate: 16000,
		FrameSamples:     480,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := make([]int16, 480)
	second := make([]int16, 480)
	for index := range first {
		first[index] = int16(index)
		second[index] = int16(index + 1000)
	}
	convertedFirst, err := audio.ResamplePCM16(first, 24000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	convertedSecond, err := audio.ResamplePCM16(second, 24000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if err := buffer.WriteFrame(context.Background(), codec.EncodePCM16(first)); err != nil {
		t.Fatal(err)
	}
	if got := buffer.PendingSamples(); got != len(convertedFirst) {
		t.Fatalf("pending after first=%d, want %d", got, len(convertedFirst))
	}
	if got := len(output.snapshot()); got != 0 {
		t.Fatalf("writes after first=%d, want 0", got)
	}
	if err := buffer.WriteFrame(context.Background(), codec.EncodePCM16(second)); err != nil {
		t.Fatal(err)
	}
	frames := output.snapshot()
	if len(frames) != 1 || len(frames[0]) != 480 {
		t.Fatalf("output frames=%d/%d, want one 480-sample frame", len(frames), len(frames[0]))
	}
	expected := append(append([]int16(nil), convertedFirst...), convertedSecond...)
	if !reflect.DeepEqual(frames[0], expected[:480]) {
		t.Fatal("output buffer padded or reordered the PCM quantum")
	}
	if got := buffer.PendingSamples(); got != len(expected)-480 {
		t.Fatalf("pending after second=%d, want %d", got, len(expected)-480)
	}
}

func TestPumpHumanOutputPreservesResolveAndObservationOrder(t *testing.T) {
	t.Parallel()

	output := &testOutput{}
	var events []string
	var observed []byte
	service := New(Dependencies{Clock: testClock{now: time.Unix(100, 0)}})
	err := service.PumpHumanOutput(context.Background(), roommedia.HumanOutputRequest{
		Mixer: &testMixer{
			format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1},
			frames: []roommedia.MixedFrame{{PCM: codec.EncodePCM16(make([]int16, 480)), Sources: []string{"peer"}}},
		},
		Output: output,
		Resolve: func(_ []string, _ int, reason string) {
			events = append(events, "resolve:"+reason)
		},
		ObserveReceived: func(pcm []byte) {
			events = append(events, "observe")
			observed = append([]byte(nil), pcm...)
		},
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("PumpHumanOutput error=%v, want EOF", err)
	}
	frames := output.snapshot()
	if len(frames) != 1 || len(frames[0]) != 480 {
		t.Fatalf("output frames=%d, want one 480-sample frame", len(frames))
	}
	if !reflect.DeepEqual(observed, codec.EncodePCM16(make([]int16, 480))) {
		t.Fatal("silent frame changed before hold-tone threshold")
	}
	if !reflect.DeepEqual(events, []string{"resolve:", "observe"}) {
		t.Fatalf("events=%v, want resolve -> observe", events)
	}
}

func TestPumpHumanOutputPreservesOutputErrorIdentity(t *testing.T) {
	t.Parallel()

	outputErr := errors.New("speaker closed")
	var reason string
	service := New(Dependencies{})
	err := service.PumpHumanOutput(context.Background(), roommedia.HumanOutputRequest{
		Mixer: &testMixer{
			format: roommedia.PCM16Format{SampleRate: 16000, Channels: 1},
			frames: []roommedia.MixedFrame{{PCM: codec.EncodePCM16(make([]int16, 480)), Sources: []string{"peer"}}},
		},
		Output:  &testOutput{err: outputErr},
		Resolve: func(_ []string, _ int, gotReason string) { reason = gotReason },
	})
	if !errors.Is(err, outputErr) {
		t.Fatalf("output error=%v, want speaker error", err)
	}
	if reason != roommedia.ParticipantOutputRejectedReason {
		t.Fatalf("resolve reason=%q", reason)
	}
}

func TestRequestValidationAndNilPorts(t *testing.T) {
	t.Parallel()

	service := New(Dependencies{})
	if !errors.Is(service.PumpProviderInput(context.Background(), roommedia.ProviderInputRequest{}), roommedia.ErrUnavailable) {
		t.Error("provider pump accepted nil ports")
	}
	if !errors.Is(service.CaptureHuman(context.Background(), roommedia.HumanCaptureRequest{}), roommedia.ErrUnavailable) {
		t.Error("capture accepted nil ports")
	}
	if _, err := service.NewOutputBuffer(roommedia.OutputBufferRequest{Output: &testOutput{}, Format: roommedia.PCM16Format{SampleRate: 16000, Channels: 2}}); !errors.Is(err, roommedia.ErrInvalidFormat) {
		t.Errorf("invalid output format error=%v", err)
	}
}
