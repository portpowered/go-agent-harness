package cli

import (
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
)

func validateSessionModelOptions(voice, reasoningEffort string) error {
	if err := serviceSession.ValidateOpenAIRealtimeVoice(voice); err != nil {
		return err
	}
	return serviceSession.ValidateOpenAIRealtimeReasoningEffort(reasoningEffort)
}
