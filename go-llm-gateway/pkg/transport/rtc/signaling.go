package rtc

import (
	"context"
	"errors"
	"time"
)

type SessionDescription struct{ Type, SDP string }
type ICECandidate struct{ Candidate string }
type SignalingConfig struct {
	ICEGatheringTimeout time.Duration
	Unreachable         bool
}

const DefaultICEGatheringTimeout = time.Second

type SignalingError struct{ Kind string }

func (e *SignalingError) Error() string { return e.Kind }

var ErrMalformedOffer, ErrMalformedAnswer = &SignalingError{"malformed offer"}, &SignalingError{"malformed answer"}
var ErrNoCandidates, ErrICEGatheringTimeout = &SignalingError{"no ICE candidates"}, &SignalingError{"ICE gathering timeout"}
var ErrSignalingUnreachable, ErrAnswerBeforeOffer = &SignalingError{"signaling unreachable"}, &SignalingError{"answer before offer"}
var ErrInvalidSignalingConfiguration, ErrGatheringComplete = errors.New("invalid signaling configuration"), errors.New("ICE gathering complete")
var ErrSignalingClosed, ErrInvalidSignalingOrder = errors.New("signaling exchange closed"), errors.New("invalid signaling order")

// Signaling is caller-owned: the pair returns offerer first and answerer second; contexts cancel calls, positive config bounds ICE waits, and Done/Close terminate it.
type Signaling interface {
	SendOffer(context.Context, SessionDescription) error
	ReceiveOffer(context.Context) (SessionDescription, error)
	SendAnswer(context.Context, SessionDescription) error
	ReceiveAnswer(context.Context) (SessionDescription, error)
	SendCandidate(context.Context, ICECandidate) error
	ReceiveCandidate(context.Context) (ICECandidate, error)
	CompleteCandidateGathering(context.Context) error
	WaitCandidateGathering(context.Context) error
	Done() <-chan struct{}
	Close() error
}

func (c SignalingConfig) timeout() (time.Duration, error) {
	if c.ICEGatheringTimeout <= 0 {
		return 0, ErrInvalidSignalingConfiguration
	}
	return c.ICEGatheringTimeout, nil
}
