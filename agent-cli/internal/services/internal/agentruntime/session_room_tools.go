package agentruntime

import (
	"context"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	roomcapabilities "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
	roomcapabilitiesWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
	"os"
	"path/filepath"
)

var (
	ErrRoomParticipantToolsUnavailable = roomcapabilities.ErrRoomParticipantToolsUnavailable
	ErrRoomParticipantToolMismatch     = roomcapabilities.ErrRoomParticipantToolMismatch
)

type RoomParticipantToolCapabilities = roomcapabilities.ToolCapabilities
type RoomParticipantToolCapabilitiesFactory func(room.Participant) (RoomParticipantToolCapabilities, error)

func roomCapabilityParticipant(participant room.Participant) roomcapabilities.Participant {
	return roomcapabilities.Participant{ID: participant.ID, Tools: append([]string(nil), participant.Tools...)}
}
func roomManifestHasTools(manifest room.Manifest) bool {
	for _, participant := range manifest.Participants {
		if len(participant.Tools) > 0 {
			return true
		}
	}
	return false
}
func newDefaultRoomParticipantToolCapabilitiesFactoryWithPolicy(configDir string, policy *tools.FilesystemPolicy) (RoomParticipantToolCapabilitiesFactory, error) {
	if policy == nil {
		return nil, fmt.Errorf("resolve filesystem scope: policy is nil")
	}
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("initialize room tool config: %w", err)
	}
	cfg, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load room tool config: %w", err)
	}
	toolService, capabilityService := runtimeToolsWire.NewService(), roomcapabilitiesWire.NewService()
	skillRoots := roomRuntimeSkillRoots(storage, policy)
	return func(participant room.Participant) (RoomParticipantToolCapabilities, error) {
		resolved, resolveErr := toolService.Resolve(context.Background(), runtimeTools.Request{
			WorkDir: policy.PrimaryRoot(), AllowPaths: policy.AdditionalRoots(), SkillRoots: skillRoots,
			Selections: roomRuntimeToolSelections(cfg, participant), Exec: roomRuntimeExecPolicy(cfg), DiagnosticWriter: os.Stderr, UseDefaultTool: true,
		})
		if resolveErr != nil {
			return RoomParticipantToolCapabilities{}, fmt.Errorf("resolve tools for participant %q: %w", participant.ID, resolveErr)
		}
		return RoomParticipantToolCapabilities{Executor: resolved.Executor, Definitions: capabilityService.OrderDefinitions(resolved.Definitions, participant.Tools)}, nil
	}, nil
}
func roomRuntimeToolSelections(cfg *config.Config, participant room.Participant) []runtimeTools.ToolSelection {
	requested := make(map[string]struct{}, len(participant.Tools))
	for _, name := range participant.Tools {
		requested[name] = struct{}{}
	}
	selections := make([]runtimeTools.ToolSelection, 0, len(config.DefaultToolIDs))
	for _, id := range config.DefaultToolIDs {
		_, enabled := requested[id]
		if cfg != nil && !cfg.Tools.ToolEnabled(id) {
			enabled = false
		}
		selections = append(selections, runtimeTools.ToolSelection{ID: id, Enabled: enabled})
	}
	return selections
}
func roomRuntimeExecPolicy(cfg *config.Config) runtimeTools.ExecPolicy {
	if cfg == nil {
		return runtimeTools.ExecPolicy{}
	}
	return runtimeTools.ExecPolicy{EnableDenyPatterns: cfg.Tools.Exec.EnableDenyPatterns, CustomDenyPatterns: append([]string(nil), cfg.Tools.Exec.CustomDenyPatterns...), Configured: true}
}
func roomRuntimeSkillRoots(storage *config.ConfigStorage, policy *tools.FilesystemPolicy) []runtimeTools.SkillRoot {
	roots := make([]runtimeTools.SkillRoot, 0, 2)
	if policy != nil && policy.PrimaryRoot() != "" {
		roots = append(roots, runtimeTools.SkillRoot{Directory: filepath.Join(policy.PrimaryRoot(), "skills")})
	}
	if storage != nil && storage.Path() != "" {
		roots = append(roots, runtimeTools.SkillRoot{Directory: filepath.Join(filepath.Dir(storage.Path()), "skills")})
	}
	return roots
}
func cloneRoomToolDefinitions(definitions []roomcapabilities.ToolDefinition) []roomcapabilities.ToolDefinition {
	return roomcapabilitiesWire.NewService().CloneDefinitions(definitions)
}
func validateRoomParticipantToolCapabilities(participant room.Participant, capabilities RoomParticipantToolCapabilities) error {
	return roomcapabilitiesWire.NewService().ValidateTools(roomCapabilityParticipant(participant), capabilities)
}
