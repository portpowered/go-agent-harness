package fault

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var (
	// ErrICEFailure identifies a deliberately injected ICE connection failure.
	// ErrICEConnectionFailed is a descriptive alias for callers that prefer the
	// connection-state wording used by WebRTC implementations.
	ErrICEFailure          = errors.New("injected ICE connection failure")
	ErrICEConnectionFailed = ErrICEFailure
)

// ICEFailureError identifies the deterministic signaling boundary at which an
// ICE failure was injected. It is also marked as an InjectedFault so a
// provider/session that receives the error through its transport seam emits a
// typed terminal ERROR instead of silently treating it as a close.
type ICEFailureError struct {
	Stage string
}

func (e *ICEFailureError) Error() string {
	if e == nil || e.Stage == "" {
		return ErrICEFailure.Error()
	}
	return fmt.Sprintf("%s during %s", ErrICEFailure, e.Stage)
}

func (e *ICEFailureError) Unwrap() error { return ErrICEFailure }

// TransportFault marks this error as an intentional probe fault at the shared
// transport/session seam.
func (*ICEFailureError) TransportFault() {}

type signalingConfig struct {
	iceFailure bool
}

// SignalingOption configures a deterministic RTC signaling fault decorator.
type SignalingOption func(*signalingConfig) error

// WithICEFailure arms a failure after the wrapped signaling exchange has
// completed candidate gathering. The trigger is tied to that ordered
// signaling boundary, not host time, so the same loopback scenario always
// produces the same outcome.
func WithICEFailure() SignalingOption {
	return func(cfg *signalingConfig) error {
		cfg.iceFailure = true
		return nil
	}
}

// WithICEConnectionFailure is an explicit alias for WithICEFailure.
func WithICEConnectionFailure() SignalingOption { return WithICEFailure() }

func resolveSignalingOptions(options []SignalingOption) (signalingConfig, error) {
	var cfg signalingConfig
	for _, option := range options {
		if option == nil {
			return signalingConfig{}, fmt.Errorf("%w: nil signaling option", ErrInvalidConfiguration)
		}
		if err := option(&cfg); err != nil {
			return signalingConfig{}, err
		}
	}
	return cfg, nil
}

// Signaling decorates an rtc.Signaling endpoint with deterministic signaling
// faults. It preserves the underlying role/order, cancellation, Done channel,
// and close ownership contracts.
type Signaling struct {
	inner rtc.Signaling
	cfg   signalingConfig

	iceFailureOnce sync.Once
	iceFailureErr  error
}

var _ rtc.Signaling = (*Signaling)(nil)

// WrapSignaling applies deterministic RTC signaling faults to one caller-owned
// signaling endpoint. The underlying endpoint remains caller-owned and must be
// closed through this wrapper when the exchange is finished.
func WrapSignaling(inner rtc.Signaling, options ...SignalingOption) (*Signaling, error) {
	if inner == nil {
		return nil, fmt.Errorf("%w: signaling endpoint is nil", ErrInvalidConfiguration)
	}
	cfg, err := resolveSignalingOptions(options)
	if err != nil {
		return nil, err
	}
	return &Signaling{inner: inner, cfg: cfg}, nil
}

// NewSignaling is an explicit constructor alias for WrapSignaling.
func NewSignaling(inner rtc.Signaling, options ...SignalingOption) (*Signaling, error) {
	return WrapSignaling(inner, options...)
}

func (s *Signaling) SendOffer(ctx context.Context, description rtc.SessionDescription) error {
	if err := s.validate(); err != nil {
		return err
	}
	return s.inner.SendOffer(ctx, description)
}

func (s *Signaling) ReceiveOffer(ctx context.Context) (rtc.SessionDescription, error) {
	if err := s.validate(); err != nil {
		return rtc.SessionDescription{}, err
	}
	return s.inner.ReceiveOffer(ctx)
}

func (s *Signaling) SendAnswer(ctx context.Context, description rtc.SessionDescription) error {
	if err := s.validate(); err != nil {
		return err
	}
	return s.inner.SendAnswer(ctx, description)
}

func (s *Signaling) ReceiveAnswer(ctx context.Context) (rtc.SessionDescription, error) {
	if err := s.validate(); err != nil {
		return rtc.SessionDescription{}, err
	}
	return s.inner.ReceiveAnswer(ctx)
}

func (s *Signaling) SendCandidate(ctx context.Context, candidate rtc.ICECandidate) error {
	if err := s.validate(); err != nil {
		return err
	}
	return s.inner.SendCandidate(ctx, candidate)
}

func (s *Signaling) ReceiveCandidate(ctx context.Context) (rtc.ICECandidate, error) {
	if err := s.validate(); err != nil {
		return rtc.ICECandidate{}, err
	}
	return s.inner.ReceiveCandidate(ctx)
}

func (s *Signaling) CompleteCandidateGathering(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	return s.inner.CompleteCandidateGathering(ctx)
}

// WaitCandidateGathering first observes the wrapped endpoint's normal
// deterministic completion. An armed ICE fault then terminates the exchange
// and returns a stable typed error at the connection-establishment boundary.
func (s *Signaling) WaitCandidateGathering(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := s.inner.WaitCandidateGathering(ctx); err != nil {
		return err
	}
	if !s.cfg.iceFailure {
		return nil
	}
	return s.triggerICEFailure()
}

func (s *Signaling) Done() <-chan struct{} {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Done()
}

func (s *Signaling) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

func (s *Signaling) validate() error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("%w: signaling endpoint is nil", ErrInvalidConfiguration)
	}
	return nil
}

func (s *Signaling) triggerICEFailure() error {
	s.iceFailureOnce.Do(func() {
		faultErr := &ICEFailureError{Stage: "candidate gathering"}
		if closeErr := s.inner.Close(); closeErr != nil {
			s.iceFailureErr = errors.Join(faultErr, closeErr)
			return
		}
		s.iceFailureErr = faultErr
	})
	return s.iceFailureErr
}
