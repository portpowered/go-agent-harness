package services

import (
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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

// newDefaultRoomParticipantToolCapabilitiesFactory loads the config once and
// creates a fresh registry for every participant. A fresh full registry is
// used as the source so selected tool objects, including any mutable callback
// state, are never shared between room participants.
func newDefaultRoomParticipantToolCapabilitiesFactory(configDir string) (RoomParticipantToolCapabilitiesFactory, error) {
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("initialize room tool config: %w", err)
	}
	cfg, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load room tool config: %w", err)
	}

	return func(participant room.Participant) (RoomParticipantToolCapabilities, error) {
		available := tools.NewToolRegistryFromConfig(cfg)
		selected := tools.NewToolRegistry()
		for _, name := range participant.Tools {
			tool, ok := available.Get(name)
			if !ok {
				return RoomParticipantToolCapabilities{}, fmt.Errorf(
					"participant %q requested tool %q, but it is not available in the configured tool registry",
					participant.ID,
					name,
				)
			}
			if err := selected.Register(tool); err != nil {
				return RoomParticipantToolCapabilities{}, fmt.Errorf("select tool %q for participant %q: %w", name, participant.ID, err)
			}
		}
		return RoomParticipantToolCapabilities{
			Executor:    tools.NewRegistryExecutor(selected),
			Definitions: orderedRoomToolDefinitions(selected.ToAgentLoopDefs(), participant.Tools),
		}, nil
	}, nil
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
	return ordered
}

func cloneRoomToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	if len(definitions) == 0 {
		return nil
	}
	cloned := make([]messages.ToolDefinition, len(definitions))
	for index, definition := range definitions {
		cloned[index] = cloneRoomToolDefinition(definition)
	}
	return cloned
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
