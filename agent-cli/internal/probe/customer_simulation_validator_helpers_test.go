package probe

import (
	"context"
	"time"
)

// RunCustomerSimulationValidator evaluates already-collected typed evidence.
// For a persisted run, prefer RunFinalizedCustomerSimulationValidator so the
// runner verifies the on-disk manifest and canonical artifacts before the
// agent is invoked.
func RunCustomerSimulationValidator(ctx context.Context, input ValidatorInput, agent CustomerSimulationValidatorAgent, timeout time.Duration) (CustomerSimulationValidatorResult, error) {
	return (CustomerSimulationValidatorRunner{Agent: agent, Timeout: timeout}).Run(ctx, input)
}

func (r CustomerSimulationValidatorRunner) Run(ctx context.Context, input ValidatorInput) (CustomerSimulationValidatorResult, error) {
	return r.run(ctx, input, nil, nil)
}
