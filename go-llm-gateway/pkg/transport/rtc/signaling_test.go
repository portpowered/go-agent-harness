package rtc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestLoopbackSignalingExchangePreservesOrder(t *testing.T) {
	offerer, answerer := pair(t, 250*time.Millisecond)
	ctx := context.Background()
	offer := rtc.SessionDescription{Type: "offer", SDP: "offer-sdp-exact"}
	answer := rtc.SessionDescription{Type: "answer", SDP: "answer-sdp-exact"}
	offers := []rtc.ICECandidate{{Candidate: "offer-1"}, {Candidate: "offer-2"}}
	answers := []rtc.ICECandidate{{Candidate: "answer-1"}, {Candidate: "answer-2"}}
	must(t, offerer.SendOffer(ctx, offer))
	sendCandidates(t, ctx, offerer, offers)
	must(t, offerer.CompleteCandidateGathering(ctx))
	if got, err := answerer.ReceiveOffer(ctx); err != nil || got != offer {
		t.Fatalf("offer = %#v, %v", got, err)
	}
	receiveCandidates(t, ctx, answerer, offers)
	if _, err := answerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		t.Fatalf("offer completion = %v", err)
	}
	must(t, answerer.SendAnswer(ctx, answer))
	sendCandidates(t, ctx, answerer, answers)
	must(t, answerer.CompleteCandidateGathering(ctx))
	if got, err := offerer.ReceiveAnswer(ctx); err != nil || got != answer {
		t.Fatalf("answer = %#v, %v", got, err)
	}
	receiveCandidates(t, ctx, offerer, answers)
	if _, err := offerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		t.Fatalf("answer completion = %v", err)
	}
	must(t, offerer.WaitCandidateGathering(ctx))
	must(t, answerer.WaitCandidateGathering(ctx))
	waitDone(t, offerer)
	waitDone(t, answerer)
}

func TestLoopbackSignalingErrorsAreDistinctAndTerminal(t *testing.T) {
	cases := []struct {
		name string
		want error
		run  func(context.Context, *rtc.LoopbackEndpoint, *rtc.LoopbackEndpoint) error
	}{
		{"malformed offer", rtc.ErrMalformedOffer, func(ctx context.Context, o, a *rtc.LoopbackEndpoint) error {
			if err := o.SendOffer(ctx, rtc.SessionDescription{Type: "offer"}); err != nil {
				return err
			}
			_, err := a.ReceiveOffer(ctx)
			return err
		}},
		{"malformed answer", rtc.ErrMalformedAnswer, func(ctx context.Context, o, a *rtc.LoopbackEndpoint) error {
			if err := offerPhase(ctx, o, a); err != nil {
				return err
			}
			if err := a.SendAnswer(ctx, rtc.SessionDescription{Type: "answer"}); err != nil {
				return err
			}
			_, err := o.ReceiveAnswer(ctx)
			return err
		}},
		{"no candidates", rtc.ErrNoCandidates, func(ctx context.Context, o, a *rtc.LoopbackEndpoint) error {
			if err := offerPhase(ctx, o, a); err != nil {
				return err
			}
			if err := o.CompleteCandidateGathering(ctx); err != nil {
				return err
			}
			return o.WaitCandidateGathering(ctx)
		}},
		{"ICE gathering timeout", rtc.ErrICEGatheringTimeout, func(ctx context.Context, o, a *rtc.LoopbackEndpoint) error {
			if err := offerPhase(ctx, o, a); err != nil {
				return err
			}
			return a.WaitCandidateGathering(ctx)
		}},
		{"signaling unreachable", rtc.ErrSignalingUnreachable, func(ctx context.Context, o, _ *rtc.LoopbackEndpoint) error { return o.SendOffer(ctx, validOffer()) }},
		{"answer before offer", rtc.ErrAnswerBeforeOffer, func(ctx context.Context, _, a *rtc.LoopbackEndpoint) error { return a.SendAnswer(ctx, validAnswer()) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := rtc.SignalingConfig{ICEGatheringTimeout: 25 * time.Millisecond}
			var o, a *rtc.LoopbackEndpoint
			var err error
			if tc.name == "signaling unreachable" {
				cfg.Unreachable = true
			}
			o, a, err = rtc.NewLoopbackSignalingPair(cfg)
			must(t, err)
			got := tc.run(context.Background(), o, a)
			if !errors.Is(got, tc.want) {
				t.Fatalf("error = %v, want %v", got, tc.want)
			}
			var typed *rtc.SignalingError
			if !errors.As(got, &typed) {
				t.Fatalf("error = %v, want typed signaling error", got)
			}
			for _, other := range failureKinds() {
				if other != tc.want && errors.Is(got, other) {
					t.Fatalf("error %v also matches %v", got, other)
				}
			}
			waitDone(t, o)
			waitDone(t, a)
		})
	}
}

func TestLoopbackSignalingCancellationAndConfiguration(t *testing.T) {
	if _, _, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{}); !errors.Is(err, rtc.ErrInvalidSignalingConfiguration) {
		t.Fatalf("zero timeout = %v", err)
	}
	if _, _, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: -time.Second}); !errors.Is(err, rtc.ErrInvalidSignalingConfiguration) {
		t.Fatalf("negative timeout = %v", err)
	}
	o, a := pair(t, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.ReceiveOffer(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled receive = %v", err)
	}
	waitDone(t, o)
	waitDone(t, a)
}

func offerPhase(ctx context.Context, o, a *rtc.LoopbackEndpoint) error {
	if err := o.SendOffer(ctx, validOffer()); err != nil {
		return err
	}
	_, err := a.ReceiveOffer(ctx)
	return err
}
func validOffer() rtc.SessionDescription {
	return rtc.SessionDescription{Type: "offer", SDP: "valid-offer"}
}
func validAnswer() rtc.SessionDescription {
	return rtc.SessionDescription{Type: "answer", SDP: "valid-answer"}
}
func failureKinds() []error {
	return []error{rtc.ErrMalformedOffer, rtc.ErrMalformedAnswer, rtc.ErrNoCandidates, rtc.ErrICEGatheringTimeout, rtc.ErrSignalingUnreachable, rtc.ErrAnswerBeforeOffer}
}

func sendCandidates(t *testing.T, ctx context.Context, e *rtc.LoopbackEndpoint, candidates []rtc.ICECandidate) {
	t.Helper()
	for _, c := range candidates {
		must(t, e.SendCandidate(ctx, c))
	}
}
func receiveCandidates(t *testing.T, ctx context.Context, e *rtc.LoopbackEndpoint, want []rtc.ICECandidate) {
	t.Helper()
	for i, expected := range want {
		got, err := e.ReceiveCandidate(ctx)
		if err != nil || got != expected {
			t.Fatalf("candidate %d = %#v, %v", i, got, err)
		}
	}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func pair(t *testing.T, timeout time.Duration) (*rtc.LoopbackEndpoint, *rtc.LoopbackEndpoint) {
	t.Helper()
	o, a, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: timeout})
	must(t, err)
	return o, a
}
func waitDone(t *testing.T, e *rtc.LoopbackEndpoint) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-e.Done():
	case <-ctx.Done():
		t.Fatalf("endpoint did not terminate: %v", ctx.Err())
	}
}
