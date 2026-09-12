package transports

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
)

type adapterFrame struct {
	PCM     []byte
	Sources []string
}

type adapterMixer struct {
	format roommedia.PCM16Format
	frame  adapterFrame
	err    error
}

func (m *adapterMixer) Format() roommedia.PCM16Format { return m.format }

func (m *adapterMixer) ReadFrameWithSources(context.Context) (adapterFrame, error) {
	return m.frame, m.err
}

type hostPolicy string

func closedSignal() <-chan struct{} {
	signal := make(chan struct{})
	close(signal)
	return signal
}

func TestWaitAdaptersHandleReadyAndCancellation(t *testing.T) {
	gate := make(chan struct{})
	close(gate)
	if !AwaitGate(gate, nil, nil) {
		t.Fatal("AwaitGate rejected a ready gate")
	}
	if AwaitGate(nil, closedSignal(), nil) {
		t.Fatal("AwaitGate accepted a cancelled owner")
	}
	if AwaitGate(nil, nil, closedSignal()) {
		t.Fatal("AwaitGate accepted the second cancelled owner")
	}

	values := make(chan string, 1)
	values <- "ready"
	value, ok := AwaitValue(values, nil, nil)
	if !ok || value != "ready" {
		t.Fatalf("AwaitValue=(%q,%t), want ready,true", value, ok)
	}
	if _, ok := AwaitValue(make(chan string), closedSignal(), nil); ok {
		t.Fatal("AwaitValue accepted a cancelled owner")
	}
	if _, ok := AwaitValue(make(chan string), nil, closedSignal()); ok {
		t.Fatal("AwaitValue accepted the second cancelled owner")
	}

	readyValues := make(chan int, 1)
	readyValues <- 7
	if got, ok := AwaitReadyNonNil(gate, readyValues, nil, nil, nil); !ok || got != 7 {
		t.Fatalf("AwaitReadyNonNil=(%d,%t), want 7,true", got, ok)
	}

	zeroValues := make(chan int, 1)
	zeroValues <- 0
	unavailable := 0
	if got, ok := AwaitReadyNonNil(gate, zeroValues, nil, nil, func() { unavailable++ }); ok || got != 0 {
		t.Fatalf("zero readiness=(%d,%t), want 0,false", got, ok)
	}
	if unavailable != 1 {
		t.Fatalf("unavailable calls=%d, want 1", unavailable)
	}
	if _, ok := AwaitReadyNonNil(nil, make(chan int), closedSignal(), nil, nil); ok {
		t.Fatal("AwaitReadyNonNil accepted a cancelled gate")
	}
}

func TestErrorAndOptionalAdaptersPreservePolicy(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", want: true},
		{name: "eof", err: io.EOF, want: true},
		{name: "cancel", err: context.Canceled, want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "failure", err: errors.New("transport failure"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsNormalTermination(test.err); got != test.want {
				t.Fatalf("IsNormalTermination(%v)=%t, want %t", test.err, got, test.want)
			}
		})
	}
	if got := Optional(true, "enabled"); got != "enabled" {
		t.Fatalf("enabled Optional=%q", got)
	}
	if got := Optional(false, "disabled"); got != "" {
		t.Fatalf("disabled Optional=%q, want empty", got)
	}

	if IgnoreError(nil) != nil {
		t.Fatal("IgnoreError(nil) returned a callback")
	}
	var observed []byte
	callback := IgnoreError(func(pcm []byte) error {
		observed = append([]byte(nil), pcm...)
		return errors.New("best effort failure")
	})
	callback([]byte{1, 2, 3})
	if !reflect.DeepEqual(observed, []byte{1, 2, 3}) {
		t.Fatalf("observed=%v", observed)
	}

	actionCalls := 0
	action := func(error) { actionCalls++ }
	OnError(nil, false, action)
	OnError(errors.New("handled"), true, action)
	OnError(errors.New("unhandled"), false, action)
	OnError(errors.New("no action"), false, nil)
	if actionCalls != 1 {
		t.Fatalf("OnError calls=%d, want 1", actionCalls)
	}

	marker := errors.New("action result")
	if got := HandleError(nil, false, func(error) error { t.Fatal("nil error action called"); return marker }); got != nil {
		t.Fatalf("nil HandleError=%v", got)
	}
	if got := HandleError(marker, true, func(error) error { t.Fatal("handled error action called"); return nil }); got != nil {
		t.Fatalf("handled HandleError=%v", got)
	}
	if got := HandleError(marker, false, nil); !errors.Is(got, marker) {
		t.Fatalf("nil action identity lost: %v", got)
	}
	if got := HandleError(marker, false, func(error) error { return marker }); !errors.Is(got, marker) {
		t.Fatalf("action result=%v, want marker", got)
	}

	primary, cancel := context.WithCancel(context.Background())
	fallback := context.Background()
	defer cancel()
	if got := ContextOr(primary, fallback); got != primary {
		t.Fatal("ContextOr did not prefer primary")
	}
	var noPrimary context.Context
	if got := ContextOr(noPrimary, fallback); got != fallback {
		t.Fatal("ContextOr did not use fallback")
	}
	var reported error
	if got := ReportError(marker, false, func(err error) { reported = err }); !errors.Is(got, marker) || !errors.Is(reported, marker) {
		t.Fatalf("ReportError=(%v,%v), want marker identity", got, reported)
	}
}

func TestMixerAndFrameAdaptersCopyHostValues(t *testing.T) {
	t.Parallel()

	readErr := errors.New("mixer stopped")
	newMixer := NewMixer(
		func() roommedia.PCM16Format { return roommedia.PCM16Format{SampleRate: 16000, Channels: 1} },
		func(context.Context) (roommedia.MixedFrame, error) {
			return roommedia.MixedFrame{PCM: []byte{1, 2}}, readErr
		},
	)
	if got := newMixer.Format(); got.SampleRate != 16000 || got.Channels != 1 {
		t.Fatalf("NewMixer format=%+v", got)
	}
	if _, err := newMixer.ReadFrameWithSources(context.Background()); !errors.Is(err, readErr) {
		t.Fatalf("NewMixer error=%v", err)
	}
	static := StaticMixer(roommedia.PCM16Format{SampleRate: 24000, Channels: 1})
	if got := static.Format(); got.SampleRate != 24000 {
		t.Fatalf("StaticMixer format=%+v", got)
	}
	if _, err := static.ReadFrameWithSources(context.Background()); !errors.Is(err, roommedia.ErrUnavailable) {
		t.Fatalf("StaticMixer read error=%v", err)
	}

	host := &adapterMixer{
		format: roommedia.PCM16Format{SampleRate: 48000, Channels: 1},
		frame:  adapterFrame{PCM: []byte{3, 4}, Sources: []string{"a"}},
		err:    readErr,
	}
	adapted := AdaptMixer(host, func(value roommedia.PCM16Format) roommedia.PCM16Format { return value }, func(value adapterFrame) roommedia.MixedFrame {
		return roommedia.MixedFrame{PCM: append([]byte(nil), value.PCM...), Sources: append([]string(nil), value.Sources...)}
	})
	if got := adapted.Format(); got.SampleRate != 48000 {
		t.Fatalf("AdaptMixer format=%+v", got)
	}
	frame, err := adapted.ReadFrameWithSources(context.Background())
	if !errors.Is(err, readErr) || !reflect.DeepEqual(frame.PCM, []byte{3, 4}) || !reflect.DeepEqual(frame.Sources, []string{"a"}) {
		t.Fatalf("AdaptMixer frame=%+v err=%v", frame, err)
	}
	adaptedFixed := AdaptMixerWithFormat(host, roommedia.PCM16Format{SampleRate: 16000, Channels: 1}, func(value any) roommedia.MixedFrame {
		frame, ok := value.(adapterFrame)
		if !ok {
			return roommedia.MixedFrame{}
		}
		return roommedia.MixedFrame{PCM: append([]byte(nil), frame.PCM...)}
	})
	if got := adaptedFixed.Format(); got.SampleRate != 16000 {
		t.Fatalf("AdaptMixerWithFormat format=%+v", got)
	}

	original := adapterFrame{PCM: []byte{9, 8}, Sources: []string{"peer"}}
	copyOfValue := CopyPCM16Frame(original)
	original.PCM[0] = 0
	original.Sources[0] = "changed"
	if !reflect.DeepEqual(copyOfValue, roommedia.MixedFrame{PCM: []byte{9, 8}, Sources: []string{"peer"}}) {
		t.Fatalf("CopyPCM16Frame=%+v", copyOfValue)
	}
	pointer := &adapterFrame{PCM: []byte{5}, Sources: []string{"pointer"}}
	if got := CopyPCM16Frame(pointer); !reflect.DeepEqual(got.PCM, []byte{5}) || !reflect.DeepEqual(got.Sources, []string{"pointer"}) {
		t.Fatalf("pointer copy=%+v", got)
	}
	if got := CopyPCM16Frame((*adapterFrame)(nil)); !reflect.DeepEqual(got, roommedia.MixedFrame{}) {
		t.Fatalf("nil pointer copy=%+v", got)
	}
	for _, invalid := range []any{nil, 42, struct {
		PCM     string
		Sources []string
	}{}, struct{ PCM []byte }{}} {
		if got := CopyPCM16Frame(invalid); !reflect.DeepEqual(got, roommedia.MixedFrame{}) {
			t.Fatalf("invalid copy of %#v=%+v", invalid, got)
		}
	}
}

func TestPolicyAndInputAdapters(t *testing.T) {
	t.Parallel()

	policyErr := errors.New("policy send failed")
	var sentPolicy hostPolicy
	var sentPCM []byte
	sender := AdaptPolicySender(func(_ context.Context, pcm []byte, policy hostPolicy) error {
		sentPCM = append([]byte(nil), pcm...)
		sentPolicy = policy
		return policyErr
	})
	if err := sender(context.Background(), []byte{6, 7}, roommedia.InputPolicyInterrupt); !errors.Is(err, policyErr) {
		t.Fatalf("AdaptPolicySender error=%v", err)
	}
	if !reflect.DeepEqual(sentPCM, []byte{6, 7}) || sentPolicy != hostPolicy(roommedia.InputPolicyInterrupt) {
		t.Fatalf("sender values=(%v,%q)", sentPCM, sentPolicy)
	}
	policy := AdaptPolicy(func(sources []string) hostPolicy { return hostPolicy(sources[0]) })
	if got := policy([]string{"peer-a"}); hostPolicy(got) != hostPolicy("peer-a") {
		t.Fatalf("AdaptPolicy=%q", got)
	}

	hookErr := errors.New("hook failed")
	var hookParticipant string
	var hookPCM []byte
	hook := AdaptInputHook[hostPolicy](func(participant string, pcm []byte) error {
		hookParticipant = participant
		hookPCM = pcm
		pcm[0] = 0
		return hookErr
	}, "participant-a", nil)
	input := []byte{1, 2}
	if err := hook(context.Background(), input, hostPolicy("ignored")); !errors.Is(err, hookErr) {
		t.Fatalf("AdaptInputHook error=%v", err)
	}
	if hookParticipant != "participant-a" || !reflect.DeepEqual(hookPCM, []byte{0, 2}) || !reflect.DeepEqual(input, []byte{1, 2}) {
		t.Fatalf("hook values=(%q,%v), input=%v", hookParticipant, hookPCM, input)
	}
	fallbackErr := errors.New("fallback failed")
	fallbackHook := AdaptInputHook[hostPolicy](nil, "unused", func(_ context.Context, pcm []byte, policy hostPolicy) error {
		if !reflect.DeepEqual(pcm, []byte{4}) || policy != hostPolicy("fallback") {
			t.Fatalf("fallback inputs=(%v,%q)", pcm, policy)
		}
		return fallbackErr
	})
	if err := fallbackHook(context.Background(), []byte{4}, hostPolicy("fallback")); !errors.Is(err, fallbackErr) {
		t.Fatalf("fallback error=%v", err)
	}
}

func TestCollectionAdapters(t *testing.T) {
	t.Parallel()

	if got := MapIf([]int{1, 2, 3}, nil, func(value int) int { return value * 2 }); !reflect.DeepEqual(got, []int{2, 4, 6}) {
		t.Fatalf("MapIf nil=%v", got)
	}
	if got := MapIf([]int{1, 2, 3}, func(value int) bool { return value != 2 }, func(value int) string { return string(rune('a' + value)) }); !reflect.DeepEqual(got, []string{"b", "d"}) {
		t.Fatalf("MapIf filtered=%v", got)
	}
	if got := MapNonZero([]string{"", "a", "", "b"}, func(value string) string { return value + "!" }); !reflect.DeepEqual(got, []string{"a!", "b!"}) {
		t.Fatalf("MapNonZero=%v", got)
	}
	supplier := MapSupplier(func() []int { return []int{0, 2, 0, 4} }, func(value int) string { return string(rune('0' + value)) })
	if got := supplier(); !reflect.DeepEqual(got, []string{"2", "4"}) {
		t.Fatalf("MapSupplier=%v", got)
	}
}

func TestFanoutAdapters(t *testing.T) {
	t.Parallel()

	writeCalls := 0
	target := NewFanoutTarget("peer-a", roommedia.PCM16Format{SampleRate: 24000, Channels: 1}, func() bool { return true }, func(_ context.Context, source string, pcm []byte) error {
		writeCalls++
		if source != "participant-a" || !reflect.DeepEqual(pcm, []byte{8}) {
			t.Fatalf("target write=(%q,%v)", source, pcm)
		}
		return nil
	})
	if target.ID != "peer-a" || !target.Active() || target.Format.SampleRate != 24000 {
		t.Fatalf("target=%+v", target)
	}
	if err := target.Write(context.Background(), "participant-a", []byte{8}); err != nil || writeCalls != 1 {
		t.Fatalf("target write err=%v calls=%d", err, writeCalls)
	}

	mapper := FanoutMapper(
		func(value string) string { return value },
		func(value string) roommedia.PCM16Format {
			return roommedia.PCM16Format{SampleRate: len(value), Channels: 1}
		},
		func(value string) bool { return value != "inactive" },
		func(value string, _ context.Context, source string, pcm []byte) error {
			if value != "peer-b" || source != "source" || !reflect.DeepEqual(pcm, []byte{9}) {
				t.Fatalf("mapped write=(%q,%q,%v)", value, source, pcm)
			}
			return nil
		},
	)
	mapped := mapper("peer-b")
	if mapped.ID != "peer-b" || !mapped.Active() || mapped.Format.SampleRate != len("peer-b") {
		t.Fatalf("mapped target=%+v", mapped)
	}
	if err := mapped.Write(context.Background(), "source", []byte{9}); err != nil {
		t.Fatalf("mapped write err=%v", err)
	}
}
