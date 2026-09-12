package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
)

type Service struct{}

func New() roomplanning.Service { return &Service{} }

func (s *Service) Await(ctx context.Context, options roomplanning.AwaitOptions) error {
	return awaitAdmission(ctx, options)
}

func (s *Service) Plan(ctx context.Context, options roomplanning.Options) (roomplanning.PlanResult, error) {
	scope, err := resolveScope(options)
	if err != nil {
		return roomplanning.PlanResult{}, err
	}
	if options.ReplayPlan != nil {
		return planReplay(ctx, options)
	}
	return planLive(ctx, options, scope)
}
