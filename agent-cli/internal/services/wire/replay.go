package wire

import (
	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
)

// NewReplayService keeps the CLI graph's strict replay dependency thin. The
// runtime replay Wire owns admission, private strict business behavior, and
// the credential-free OpenAI/AgentLoop composition; the CLI only presents it.
func NewReplayService() publicreplay.StrictService {
	return runtimeReplayWire.NewStrictService()
}
