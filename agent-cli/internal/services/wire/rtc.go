package wire

import (
	rtcontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime/transports"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
)

func NewSessionRTCRuntimeFactory(components rtcontract.SessionRTCComponents, sampler observability.MetricSampler, logger observability.Logger) rtcontract.SessionRTCRuntimeFactory {
	return agentruntime.NewSessionRTCRuntimeFactoryWithObservability(components, sampler, logger)
}
