package agentruntime

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
)

func newRoomParticipantSessionOptions(opts RoomRunOptions, participant room.Participant, apiKey string, filesystemPolicy *tools.FilesystemPolicy) SessionRunOptions {
	return SessionRunOptions{
		AudioService:     opts.AudioService,
		runtimeFactory:   opts.RuntimeFactory,
		Provider:         participant.Provider,
		Model:            participant.Model,
		ModelProvided:    true,
		APIKey:           apiKey,
		BaseURL:          opts.BaseURL,
		ConfigDir:        opts.ConfigDir,
		ModelCatalog:     opts.ModelCatalog,
		Clock:            opts.Clock,
		LivenessClock:    opts.LivenessClock,
		WorkDir:          opts.WorkDir,
		AllowPaths:       append([]string(nil), opts.AllowPaths...),
		FilesystemPolicy: filesystemPolicy,
		Prompt:           participant.OpeningPrompt,
		Voice:            participant.Voice,
		WebSocketDialer:  opts.WebSocketDialer,
		WaitForClose:     true,
	}
}
