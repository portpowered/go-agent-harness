package rtc

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SignalingRole is the role owned by an endpoint.
type SignalingRole string

const (
	OffererRole  SignalingRole = "offerer"
	AnswererRole SignalingRole = "answerer"
)

// SessionDescription is an opaque, provider-neutral SDP description.
type SessionDescription struct {
	Type string
	SDP  string
}

// Description is a concise alias for SessionDescription.
type Description = SessionDescription

// ICECandidate is one opaque trickle-ICE candidate.
type ICECandidate struct{ Candidate string }

// Candidate is a concise alias for ICECandidate.
type Candidate = ICECandidate

// SignalingConfig bounds ICE gathering and optionally injects reachability.
type SignalingConfig struct {
	ICEGatheringTimeout time.Duration
	Unreachable         bool
}

// LoopbackConfig names the same configuration at the implementation seam.
type LoopbackConfig = SignalingConfig

// DefaultICEGatheringTimeout is used only when the constructor has no config.
const DefaultICEGatheringTimeout = time.Second

// SignalingErrorKind identifies one stable signaling failure class.
type SignalingErrorKind string

const (
	SignalingErrorMalformedOffer      SignalingErrorKind = "malformed offer"
	SignalingErrorMalformedAnswer     SignalingErrorKind = "malformed answer"
	SignalingErrorNoCandidates        SignalingErrorKind = "no ICE candidates"
	SignalingErrorICEGatheringTimeout SignalingErrorKind = "ICE gathering timeout"
	SignalingErrorUnreachable         SignalingErrorKind = "signaling unreachable"
	SignalingErrorAnswerBeforeOffer   SignalingErrorKind = "answer before offer"
)

// SignalingError is identifiable with errors.Is or errors.As.
type SignalingError struct{ Kind SignalingErrorKind }

func (e *SignalingError) Error() string { return string(e.Kind) }
func (e *SignalingError) Is(target error) bool {
	other, ok := target.(*SignalingError)
	return ok && e != nil && other != nil && e.Kind == other.Kind
}

var (
	ErrMalformedOffer       = &SignalingError{SignalingErrorMalformedOffer}
	ErrMalformedAnswer      = &SignalingError{SignalingErrorMalformedAnswer}
	ErrNoCandidates         = &SignalingError{SignalingErrorNoCandidates}
	ErrICEGatheringTimeout  = &SignalingError{SignalingErrorICEGatheringTimeout}
	ErrSignalingUnreachable = &SignalingError{SignalingErrorUnreachable}
	ErrAnswerBeforeOffer    = &SignalingError{SignalingErrorAnswerBeforeOffer}

	ErrInvalidSignalingConfiguration = errors.New("invalid signaling configuration")
	ErrGatheringComplete             = errors.New("ICE gathering complete")
	ErrSignalingClosed               = errors.New("signaling exchange closed")
	ErrInvalidSignalingOrder         = errors.New("invalid signaling order")
	ErrWrongSignalingRole            = errors.New("operation is invalid for signaling role")
	ErrMalformedCandidate            = errors.New("malformed ICE candidate")
)

// Signaling is a sequenced, caller-owned offer/answer/candidate exchange.
// The offerer sends an offer, candidates, and completion; the answerer must
// receive that offer before sending its answer, candidates, and completion.
// ReceiveCandidate returns ErrGatheringComplete after delivery. WaitCandidateGathering
// applies the positive configured timeout and caller context. Done closes on
// success or failure; Close ends both paired endpoints.
type Signaling interface {
	Role() SignalingRole
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
		return 0, fmt.Errorf("%w: ICE gathering timeout must be positive", ErrInvalidSignalingConfiguration)
	}
	return c.ICEGatheringTimeout, nil
}
