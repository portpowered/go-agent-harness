package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

var (
	// ErrRoomParticipantToolsUnavailable identifies a room configuration that
	// requests tools without a session tool capability composition seam.
	ErrRoomParticipantToolsUnavailable = errors.New("room participant tools are unavailable")
	// ErrRoomParticipantToolMismatch identifies a capability factory that did
	// not return exactly the tools named by the participant manifest.
	ErrRoomParticipantToolMismatch = errors.New("room participant tool capabilities do not match the manifest")
)

// RoomParticipantToolCapabilities is the isolated tool surface for one room
// participant. Definitions and executor are intentionally paired so a room
// cannot advertise a tool that its participant executor cannot run.
type RoomParticipantToolCapabilities struct {
	Executor    messages.ToolExecutor
	Definitions []messages.ToolDefinition
}

// RoomParticipantToolCapabilitiesFactory creates one participant-local tool
// surface. Implementations must return independent executor state for each
// participant invocation.
type RoomParticipantToolCapabilitiesFactory func(room.Participant) (RoomParticipantToolCapabilities, error)

func roomManifestHasTools(manifest room.Manifest) bool {
	for _, participant := range manifest.Participants {
		if len(participant.Tools) > 0 {
			return true
		}
	}
	return false
}

// newDefaultRoomParticipantToolCapabilitiesFactoryWithPolicy loads the host
// config once and resolves a fresh runtime tools capability for every
// participant. The participant allowlist is converted into explicit runtime
// selections so each capability owns only the tools named by its manifest;
// the old CLI registry is not used as a second implementation.
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
	service := runtimeToolsWire.NewService()
	skillRoots := roomRuntimeSkillRoots(storage, policy)

	return func(participant room.Participant) (RoomParticipantToolCapabilities, error) {
		capability, err := service.Resolve(context.Background(), runtimeTools.Request{
			WorkDir:          policy.PrimaryRoot(),
			AllowPaths:       policy.AdditionalRoots(),
			SkillRoots:       skillRoots,
			Selections:       roomRuntimeToolSelections(cfg, participant),
			Exec:             roomRuntimeExecPolicy(cfg),
			DiagnosticWriter: os.Stderr,
			UseDefaultTool:   true,
		})
		if err != nil {
			return RoomParticipantToolCapabilities{}, fmt.Errorf("resolve tools for participant %q: %w", participant.ID, err)
		}
		return RoomParticipantToolCapabilities{
			Executor:    capability.Executor,
			Definitions: orderedRoomToolDefinitions(capability.Definitions, participant.Tools),
		}, nil
	}, nil
}

func roomRuntimeToolSelections(cfg *config.Config, participant room.Participant) []runtimeTools.ToolSelection {
	requested := make(map[string]struct{}, len(participant.Tools))
	for _, name := range participant.Tools {
		requested[name] = struct{}{}
	}
	selections := make([]runtimeTools.ToolSelection, 0, len(config.DefaultToolIDs))
	for _, id := range config.DefaultToolIDs {
		_, selected := requested[id]
		if cfg != nil && !cfg.Tools.ToolEnabled(id) {
			selected = false
		}
		selections = append(selections, runtimeTools.ToolSelection{ID: id, Enabled: selected})
	}
	return selections
}

func roomRuntimeExecPolicy(cfg *config.Config) runtimeTools.ExecPolicy {
	if cfg == nil {
		return runtimeTools.ExecPolicy{}
	}
	return runtimeTools.ExecPolicy{
		EnableDenyPatterns: cfg.Tools.Exec.EnableDenyPatterns,
		CustomDenyPatterns: append([]string(nil), cfg.Tools.Exec.CustomDenyPatterns...),
		Configured:         true,
	}
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

func orderedRoomToolDefinitions(definitions []messages.ToolDefinition, names []string) []messages.ToolDefinition {
	byName := make(map[string]messages.ToolDefinition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = cloneRoomToolDefinition(definition)
	}
	ordered := make([]messages.ToolDefinition, 0, len(names))
	for _, name := range names {
		if definition, ok := byName[name]; ok {
			ordered = append(ordered, definition)
		}
	}
	return messages.CanonicalToolDefinitions(ordered)
}

func cloneRoomToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func cloneRoomToolDefinition(definition messages.ToolDefinition) messages.ToolDefinition {
	definition.Parameters = append([]messages.ToolParameter(nil), definition.Parameters...)
	return definition
}

func validateRoomParticipantToolCapabilities(participant room.Participant, capabilities RoomParticipantToolCapabilities) error {
	if len(participant.Tools) == 0 {
		// An explicit empty list is the no-tools contract. Ignore any accidental
		// factory value rather than allowing a participant to gain capabilities
		// that were not requested by its manifest.
		return nil
	}
	if capabilities.Executor == nil {
		return fmt.Errorf("%w: participant %q has no executor for requested tools %s", ErrRoomParticipantToolsUnavailable, participant.ID, strings.Join(participant.Tools, ", "))
	}
	seen := make(map[string]struct{}, len(capabilities.Definitions))
	requested := make(map[string]struct{}, len(participant.Tools))
	for _, name := range participant.Tools {
		requested[name] = struct{}{}
	}
	for _, definition := range capabilities.Definitions {
		if _, ok := requested[definition.Name]; !ok {
			return fmt.Errorf("%w: participant %q received unrequested tool %q", ErrRoomParticipantToolMismatch, participant.ID, definition.Name)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return fmt.Errorf("%w: participant %q received duplicate tool %q", ErrRoomParticipantToolMismatch, participant.ID, definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	if len(seen) != len(requested) {
		for _, name := range participant.Tools {
			if _, ok := seen[name]; !ok {
				return fmt.Errorf("%w: participant %q is missing definition for requested tool %q", ErrRoomParticipantToolMismatch, participant.ID, name)
			}
		}
	}
	return nil
}
