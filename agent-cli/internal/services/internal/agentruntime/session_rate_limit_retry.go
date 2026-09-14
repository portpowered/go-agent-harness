package agentruntime

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/retrypolicy"
	retrypolicywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/retrypolicy/wire"
)

func toRetryPolicyTerminal(terminal *messages.MessageEndValue) retrypolicy.Terminal {
	if terminal == nil {
		return retrypolicy.Terminal{}
	}
	return retrypolicy.Terminal{Status: terminal.Status, StatusDetails: terminal.StatusDetails, ProviderErrorCode: terminal.ProviderErrorCode, ProviderErrorMessage: terminal.ProviderErrorMessage, TerminalReason: string(terminal.TerminalReason)}
}
func retryPolicyService() retrypolicy.Service { return retrypolicywire.NewService() }
func rateLimitRetryDecision(terminal *messages.MessageEndValue) (time.Duration, bool) {
	decision := retryPolicyService().Decide(toRetryPolicyTerminal(terminal))
	return decision.Delay, decision.Eligible
}

func providerTerminalErrorCode(terminal *messages.MessageEndValue) string {
	return retryPolicyService().ProviderErrorCode(toRetryPolicyTerminal(terminal))
}

func providerTerminalErrorMessage(terminal *messages.MessageEndValue) string {
	return retryPolicyService().ProviderErrorMessage(toRetryPolicyTerminal(terminal))
}

func normalizeTerminalStatus(status string) string {
	return retryPolicyService().NormalizeStatus(status)
}
