package rtc_test

import (
	"context"
	"errors"
	rtc "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"runtime"
	"testing"
	"time"
)

func TestLoopbackSignalingExchangePreservesOrder(t *testing.T) {
	o, a := pair(t, 250*time.Millisecond)
	ctx := context.Background()
	offer, answer := sdp("offer", "offer-exact"), sdp("answer", "answer-exact")
	ocs, acs := []rtc.ICECandidate{{"offer-1"}, {"offer-2"}}, []rtc.ICECandidate{{"answer-1"}, {"answer-2"}}
	exchange(t, ctx, o.SendOffer, a.ReceiveOffer, o.SendCandidate, a.ReceiveCandidate, o.CompleteCandidateGathering, offer, ocs)
	exchange(t, ctx, a.SendAnswer, o.ReceiveAnswer, a.SendCandidate, o.ReceiveCandidate, a.CompleteCandidateGathering, answer, acs)
	expect(t, o.WaitCandidateGathering(ctx), nil)
	expect(t, a.WaitCandidateGathering(ctx), nil)
	done(t, o)
	done(t, a)
}

var failures = []error{rtc.ErrMalformedOffer, rtc.ErrMalformedAnswer, rtc.ErrNoCandidates, rtc.ErrICEGatheringTimeout, rtc.ErrSignalingUnreachable, rtc.ErrAnswerBeforeOffer}

func TestLoopbackSignalingFailureMatrixAndCleanup(t *testing.T) {
	base := runtime.NumGoroutine()
	for _, timeout := range []time.Duration{0, -time.Second} {
		_, _, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: timeout})
		expect(t, err, rtc.ErrInvalidSignalingConfiguration)
	}
	o, a := pair(t, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.ReceiveOffer(ctx)
	expect(t, err, context.Canceled)
	done(t, o)
	done(t, a)
	for kind, want := range failures {
		for n := 0; n < 8; n++ {
			o, a, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: 5 * time.Millisecond, Unreachable: kind == 4})
			expect(t, err, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			got := runFailure(kind, ctx, o, a)
			cancel()
			assertFailure(t, got, want)
			done(t, o)
			done(t, a)
			postTerminal(t, ctx, o, a, want)
		}
	}
	runtime.GC()
	if got := runtime.NumGoroutine(); got > base {
		t.Fatalf("goroutine baseline grew from %d to %d", base, got)
	}
}
func assertFailure(t *testing.T, got, want error) {
	expect(t, got, want)
	for _, other := range failures {
		if other != want && errors.Is(got, other) {
			t.Fatalf("%v also matches %v", got, other)
		}
	}
}
func runFailure(kind int, ctx context.Context, o, a *rtc.LoopbackEndpoint) error {
	switch kind {
	case 0:
		return phase(ctx, o, a, rtc.SessionDescription{Type: "offer", SDP: "not-sdp"}, false)
	case 1, 2, 3:
		if err := phase(ctx, o, a, sdp("offer", "valid"), false); err != nil {
			return err
		}
		if kind == 1 {
			return phase(ctx, o, a, rtc.SessionDescription{Type: "answer", SDP: "not-sdp"}, true)
		}
		if kind == 2 {
			o.CompleteCandidateGathering(ctx)
			return o.WaitCandidateGathering(ctx)
		}
		return a.WaitCandidateGathering(ctx)
	case 4:
		return o.SendOffer(ctx, sdp("offer", "unreachable"))
	default:
		return a.SendAnswer(ctx, sdp("answer", "early"))
	}
}
func phase(ctx context.Context, o, a *rtc.LoopbackEndpoint, d rtc.SessionDescription, answer bool) error {
	send, receive := o.SendOffer, a.ReceiveOffer
	if answer {
		send, receive = a.SendAnswer, o.ReceiveAnswer
	}
	if err := send(ctx, d); err != nil {
		return err
	}
	_, err := receive(ctx)
	return err
}
func sdp(kind, name string) rtc.SessionDescription {
	return rtc.SessionDescription{Type: kind, SDP: "v=0\r\no=- " + name + " 1 IN IP4 127.0.0.1\r\ns=" + name + "\r\nt=0 0"}
}
func pair(t *testing.T, timeout time.Duration) (*rtc.LoopbackEndpoint, *rtc.LoopbackEndpoint) {
	o, a, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: timeout})
	expect(t, err, nil)
	return o, a
}
func exchange(t *testing.T, ctx context.Context, sendDescription func(context.Context, rtc.SessionDescription) error, receiveDescription func(context.Context) (rtc.SessionDescription, error), sendCandidate func(context.Context, rtc.ICECandidate) error, receiveCandidate func(context.Context) (rtc.ICECandidate, error), complete func(context.Context) error, want rtc.SessionDescription, candidates []rtc.ICECandidate) {
	expect(t, sendDescription(ctx, want), nil)
	for _, candidate := range candidates {
		expect(t, sendCandidate(ctx, candidate), nil)
	}
	expect(t, complete(ctx), nil)
	got, err := receiveDescription(ctx)
	if err != nil || got != want {
		t.Fatalf("description = %#v, %v; want %#v", got, err, want)
	}
	for i, expected := range candidates {
		got, err := receiveCandidate(ctx)
		if err != nil || got != expected {
			t.Fatalf("candidate %d = %#v, %v", i, got, err)
		}
	}
	_, err = receiveCandidate(ctx)
	expect(t, err, rtc.ErrGatheringComplete)
}
func expect(t *testing.T, got, want error) {
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}
func done(t *testing.T, e *rtc.LoopbackEndpoint) {
	select {
	case <-e.Done():
	default:
		t.Fatal("endpoint did not terminate")
	}
}
func postTerminal(t *testing.T, ctx context.Context, o, a *rtc.LoopbackEndpoint, want error) {
	expect(t, o.SendOffer(ctx, sdp("offer", "after")), want)
	expect(t, a.SendAnswer(ctx, sdp("answer", "after")), want)
	expect(t, o.SendCandidate(ctx, rtc.ICECandidate{"after"}), want)
	expect(t, a.CompleteCandidateGathering(ctx), want)
}
