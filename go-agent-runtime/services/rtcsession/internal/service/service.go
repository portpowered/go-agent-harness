// Package service contains the private RTC lifecycle implementation.
package service

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// Service is the private implementation behind rtcsession.Service.
type Service struct {
	components    rtcsession.SessionRTCComponents
	metricSampler observability.MetricSampler
	logger        observability.Logger
}

var _ rtcsession.Service = (*Service)(nil)

// New constructs an inert service. Component validation is deliberately
// deferred to NewRuntime so application graph construction has no side effects.
func New(components rtcsession.SessionRTCComponents, sampler observability.MetricSampler, logger observability.Logger) *Service {
	return &Service{
		components:    components,
		metricSampler: observability.EnsureMetricSampler(sampler),
		logger:        observability.EnsureLogger(logger),
	}
}

func (s *Service) NewRuntime(selection rtcsession.SessionRuntimeSelection) (rtcsession.SessionRTCRuntime, error) {
	if s == nil {
		return nil, rtcsession.ErrSessionRTCRuntimeUnavailable
	}
	if err := validateComponents(s.components); err != nil {
		return nil, err
	}
	return &runtime{
		selection:     selection,
		components:    s.components,
		metricSampler: s.metricSampler,
		logger:        s.logger,
	}, nil
}

func (s *Service) WrapInferencer(runtimeOwner rtcsession.SessionRTCRuntime, inner messages.SessionInferencer) rtcsession.Inferencer {
	return &inferencer{inner: inner, runtime: runtimeOwner}
}

func (s *Service) NewLazyDialer(runtimeOwner rtcsession.SessionRTCRuntime) transport.Dialer {
	return &lazyDialer{runtime: runtimeOwner}
}

func validateComponents(components rtcsession.SessionRTCComponents) error {
	if components.ResolveSignaling == nil {
		return fmt.Errorf("%w: signaling resolver is not configured", rtcsession.ErrSessionRTCRuntimeUnavailable)
	}
	if components.NewDataPlane == nil {
		return fmt.Errorf("%w: RTC data-plane factory is not configured", rtcsession.ErrSessionRTCRuntimeUnavailable)
	}
	if components.OpenMediaSource == nil {
		return fmt.Errorf("%w: media-source opener is not configured", rtcsession.ErrSessionRTCRuntimeUnavailable)
	}
	return nil
}
