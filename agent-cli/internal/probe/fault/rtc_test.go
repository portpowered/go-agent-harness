package fault

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestICEFailureChangesLoopbackPeerOutcomeAndPreservesTerminalContract(t *testing.T) {
	clean := runLoopbackPeer(t, false)
	faulted := runLoopbackPeer(t, true)

	if clean.err != nil {
		t.Fatalf("clean loopback peer error = %v", clean.err)
	}
	if clean.state != rtc.StateConnected {
		t.Fatalf("clean loopback peer state = %s, want %s", clean.state, rtc.StateConnected)
	}

	if faulted.err == nil {
		t.Fatal("faulted loopback peer unexpectedly connected")
	}
	if faulted.state != rtc.StateTerminalFailure {
		t.Fatalf("faulted loopback peer state = %s, want %s", faulted.state, rtc.StateTerminalFailure)
	}
	var terminalErr *rtc.TerminalError
	if !errors.As(faulted.err, &terminalErr) {
		t.Fatalf("faulted peer error = %v, want *rtc.TerminalError", faulted.err)
	}
	if !errors.Is(faulted.err, ErrICEFailure) || !errors.Is(faulted.err, rtc.ErrPeerTerminalFailure) {
		t.Fatalf("faulted peer error = %v, want ICE and terminal identities", faulted.err)
	}
	var iceErr *ICEFailureError
	if !errors.As(faulted.err, &iceErr) || iceErr.Stage != "candidate gathering" {
		t.Fatalf("faulted peer error = %v, want typed candidate-gathering ICE failure", faulted.err)
	}

	// Reuse the existing stream terminal projection to prove the transport fault
	// is observable as structured failure metadata, not just a log or a closed
	// Done channel.
	value := providers.NewStreamTransportErrorValue(faulted.err)
	if value.Classification != providers.ErrorClassTransport ||
		value.TerminalReason != messages.TerminalReasonTerminalFailure ||
		value.TerminalProvenance != messages.TerminalProvenanceProvider ||
		value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("faulted terminal value = %#v, want transport terminal failure with no output", value)
	}

	if !clean.signalingDone || !faulted.signalingDone {
		t.Fatalf("clean/faulted signaling Done state = %t/%t, want both exchanges terminated", clean.signalingDone, faulted.signalingDone)
	}
}

func TestWrapSignalingRejectsNilOrNilOption(t *testing.T) {
	if _, err := WrapSignaling(nil, WithICEFailure()); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil signaling error = %v, want ErrInvalidConfiguration", err)
	}
	if _, err := WrapSignaling(loopbackStubSignaling{}, nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil signaling option error = %v, want ErrInvalidConfiguration", err)
	}
}

type loopbackPeerResult struct {
	err           error
	state         rtc.State
	signalingDone bool
}

func runLoopbackPeer(t *testing.T, injectICEFailure bool) loopbackPeerResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	dialer := &loopbackPeerDialer{injectICEFailure: injectICEFailure}
	peer := rtc.NewPeer(rtc.PeerConfig{
		Dialer:   dialer,
		Endpoint: "loopback://fault-injection",
		Retry:    rtc.RetryPolicy{MaxAttempts: 1},
	})
	err := peer.Connect(ctx)
	result := loopbackPeerResult{err: err, state: peer.State()}
	if err := peer.Close(); err != nil {
		t.Fatalf("close loopback peer: %v", err)
	}
	if dialer.done != nil {
		select {
		case <-dialer.done:
			result.signalingDone = true
		default:
		}
	}
	return result
}

type loopbackPeerDialer struct {
	injectICEFailure bool
	done             <-chan struct{}
}

func (d *loopbackPeerDialer) DialContext(ctx context.Context, _ string, _ map[string]string) (rtc.Conn, error) {
	offerer, answerer, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{ICEGatheringTimeout: time.Second})
	if err != nil {
		return nil, err
	}
	defer offerer.Close()
	defer answerer.Close()
	d.done = offerer.Done()

	var offererSignaling rtc.Signaling = offerer
	if d.injectICEFailure {
		offererSignaling, err = WrapSignaling(offerer, WithICEFailure())
		if err != nil {
			return nil, err
		}
	}
	if err := completeLoopbackExchange(ctx, offererSignaling, answerer); err != nil {
		return nil, err
	}
	return loopbackConn{}, nil
}

func completeLoopbackExchange(ctx context.Context, offerer rtc.Signaling, answerer *rtc.LoopbackEndpoint) error {
	offer := rtc.SessionDescription{Type: "offer", SDP: loopbackSDP("offer")}
	answer := rtc.SessionDescription{Type: "answer", SDP: loopbackSDP("answer")}
	if err := offerer.SendOffer(ctx, offer); err != nil {
		return fmt.Errorf("send offer: %w", err)
	}
	if err := offerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "offer-candidate"}); err != nil {
		return fmt.Errorf("send offer candidate: %w", err)
	}
	if err := offerer.CompleteCandidateGathering(ctx); err != nil {
		return fmt.Errorf("complete offer gathering: %w", err)
	}
	if _, err := answerer.ReceiveOffer(ctx); err != nil {
		return fmt.Errorf("receive offer: %w", err)
	}
	if _, err := answerer.ReceiveCandidate(ctx); err != nil {
		return fmt.Errorf("receive offer candidate: %w", err)
	}
	if _, err := answerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		return fmt.Errorf("finish offer candidates: %w", err)
	}

	if err := answerer.SendAnswer(ctx, answer); err != nil {
		return fmt.Errorf("send answer: %w", err)
	}
	if err := answerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "answer-candidate"}); err != nil {
		return fmt.Errorf("send answer candidate: %w", err)
	}
	if err := answerer.CompleteCandidateGathering(ctx); err != nil {
		return fmt.Errorf("complete answer gathering: %w", err)
	}
	if _, err := offerer.ReceiveAnswer(ctx); err != nil {
		return fmt.Errorf("receive answer: %w", err)
	}
	if _, err := offerer.ReceiveCandidate(ctx); err != nil {
		return fmt.Errorf("receive answer candidate: %w", err)
	}
	if _, err := offerer.ReceiveCandidate(ctx); !errors.Is(err, rtc.ErrGatheringComplete) {
		return fmt.Errorf("finish answer candidates: %w", err)
	}

	if err := offerer.WaitCandidateGathering(ctx); err != nil {
		return fmt.Errorf("wait offer gathering: %w", err)
	}
	if err := answerer.WaitCandidateGathering(ctx); err != nil {
		return fmt.Errorf("wait answer gathering: %w", err)
	}
	return nil
}

func loopbackSDP(name string) string {
	return "v=0\r\no=- " + name + " 1 IN IP4 127.0.0.1\r\ns=" + name + "\r\nt=0 0"
}

type loopbackConn struct{}

func (loopbackConn) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (loopbackConn) WriteMessage(int, []byte) error    { return nil }
func (loopbackConn) Close() error                      { return nil }

type loopbackStubSignaling struct{}

func (loopbackStubSignaling) SendOffer(context.Context, rtc.SessionDescription) error { return nil }
func (loopbackStubSignaling) ReceiveOffer(context.Context) (rtc.SessionDescription, error) {
	return rtc.SessionDescription{}, nil
}
func (loopbackStubSignaling) SendAnswer(context.Context, rtc.SessionDescription) error { return nil }
func (loopbackStubSignaling) ReceiveAnswer(context.Context) (rtc.SessionDescription, error) {
	return rtc.SessionDescription{}, nil
}
func (loopbackStubSignaling) SendCandidate(context.Context, rtc.ICECandidate) error { return nil }
func (loopbackStubSignaling) ReceiveCandidate(context.Context) (rtc.ICECandidate, error) {
	return rtc.ICECandidate{}, nil
}
func (loopbackStubSignaling) CompleteCandidateGathering(context.Context) error { return nil }
func (loopbackStubSignaling) WaitCandidateGathering(context.Context) error     { return nil }
func (loopbackStubSignaling) Done() <-chan struct{}                            { return nil }
func (loopbackStubSignaling) Close() error                                     { return nil }

var _ rtc.ContextDialer = (*loopbackPeerDialer)(nil)
var _ rtc.Conn = loopbackConn{}
var _ rtc.Signaling = loopbackStubSignaling{}
