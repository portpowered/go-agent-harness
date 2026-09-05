package agentruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var (
	// ErrRoomParticipantBrowserToolsUnavailable identifies an enabled browser
	// participant for which the composition root did not supply a capability
	// factory.
	ErrRoomParticipantBrowserToolsUnavailable = errors.New("room participant browser tools are unavailable")
	// ErrRoomParticipantBrowserToolMismatch identifies a browser capability
	// factory that returned an unusable executor or definition surface.
	ErrRoomParticipantBrowserToolMismatch = errors.New("room participant browser tool capabilities do not match the contract")
)

// RoomParticipantBrowserCapabilities is the browser-only capability surface
// for one participant. The room service composes it with that participant's
// ordinary tool allowlist, so every returned executor and lifecycle hook stays
// participant-local.
//
// Definitions is the current browser surface at construction time. When a
// browser is already selected, it may include first-class page tools. The
// optional ToolDefinitionBase is the stable browser surface retained while
// page definitions are refreshed; when omitted, Definitions is used.
type RoomParticipantBrowserCapabilities struct {
	Executor               messages.ToolExecutor
	Definitions            []messages.ToolDefinition
	ToolDefinitionBase     []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	BrowserWatch           func(context.Context) <-chan webmcp.BrokerEvent
	Initialize             func(context.Context) error
	Close                  func() error
}

// RoomParticipantBrowserCapabilitiesFactory creates one independent browser
// capability set for an enabled participant. It must allocate fresh broker,
// discovery, selection, invocation, and cleanup state on every call.
type RoomParticipantBrowserCapabilitiesFactory func(room.Participant) (RoomParticipantBrowserCapabilities, error)

func validateRoomParticipantBrowserCapabilities(participant room.Participant, capabilities RoomParticipantBrowserCapabilities) error {
	if nilInterface(capabilities.Executor) {
		return fmt.Errorf("%w: participant %q has no browser executor", ErrRoomParticipantBrowserToolsUnavailable, participant.ID)
	}
	if err := validateRoomToolDefinitions(capabilities.Definitions); err != nil {
		return fmt.Errorf("%w: participant %q browser definitions: %v", ErrRoomParticipantBrowserToolMismatch, participant.ID, err)
	}
	if err := validateRoomToolDefinitions(capabilities.ToolDefinitionBase); err != nil {
		return fmt.Errorf("%w: participant %q browser base definitions: %v", ErrRoomParticipantBrowserToolMismatch, participant.ID, err)
	}
	return nil
}

func validateRoomToolDefinitions(definitions []messages.ToolDefinition) error {
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" {
			return errors.New("contains a definition with an empty name")
		}
		if _, exists := seen[definition.Name]; exists {
			return fmt.Errorf("contains duplicate definition %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	return nil
}

// composeRoomParticipantBrowserCapabilities combines the browser-only
// capability with the participant's static tool capability. The refresh
// closure repeats the same composition so a page catalog update cannot drop
// static tools or accidentally route through another participant.
func composeRoomParticipantBrowserCapabilities(
	participant room.Participant,
	static RoomParticipantToolCapabilities,
	browser RoomParticipantBrowserCapabilities,
) (RoomParticipantBrowserCapabilities, error) {
	if err := validateRoomParticipantBrowserCapabilities(participant, browser); err != nil {
		return RoomParticipantBrowserCapabilities{}, err
	}
	compose := func(browserDefinitions []messages.ToolDefinition) (tools.ToolSurface, error) {
		return tools.ComposeToolSurface(
			static.Executor,
			static.Definitions,
			browser.Executor,
			browserDefinitions,
		)
	}
	initial, err := compose(browser.Definitions)
	if err != nil {
		return RoomParticipantBrowserCapabilities{}, fmt.Errorf("compose participant browser tools: %w", err)
	}

	browserBase := browser.ToolDefinitionBase
	if len(browserBase) == 0 {
		browserBase = browser.Definitions
	}
	base, err := compose(browserBase)
	if err != nil {
		return RoomParticipantBrowserCapabilities{}, fmt.Errorf("compose participant browser tool base: %w", err)
	}

	result := RoomParticipantBrowserCapabilities{
		Executor:           initial.Executor,
		Definitions:        cloneRoomToolDefinitions(initial.Definitions),
		ToolDefinitionBase: cloneRoomToolDefinitions(base.Definitions),
		BrowserWatch:       browser.BrowserWatch,
		Initialize:         browser.Initialize,
		Close:              browser.Close,
	}
	if browser.RefreshToolDefinitions != nil {
		result.RefreshToolDefinitions = func(ctx context.Context) ([]messages.ToolDefinition, error) {
			browserDefinitions, refreshErr := browser.RefreshToolDefinitions(ctx)
			if refreshErr != nil {
				return nil, refreshErr
			}
			refreshed, composeErr := compose(browserDefinitions)
			if composeErr != nil {
				return nil, fmt.Errorf("compose refreshed participant browser tools: %w", composeErr)
			}
			return cloneRoomToolDefinitions(refreshed.Definitions), nil
		}
	}
	return result, nil
}

func closeRoomParticipantCapability(plan *roomParticipantPlan) error {
	if plan == nil || plan.capabilityCoordinator == nil {
		return nil
	}
	return plan.capabilityCoordinator.Close()
}

func closeRoomParticipantPlanCapabilities(plans []*roomParticipantPlan) error {
	var closeErr error
	for _, plan := range plans {
		closeErr = errors.Join(closeErr, closeRoomParticipantCapability(plan))
	}
	return closeErr
}
