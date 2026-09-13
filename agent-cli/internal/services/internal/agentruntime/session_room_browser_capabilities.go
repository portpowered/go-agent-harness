package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	roomcapabilities "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
	roomcapabilitiesWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities/wire"
)

var (
	ErrRoomParticipantBrowserToolsUnavailable = roomcapabilities.ErrRoomParticipantBrowserToolsUnavailable
	ErrRoomParticipantBrowserToolMismatch     = roomcapabilities.ErrRoomParticipantBrowserToolMismatch
)

type RoomParticipantBrowserCapabilities struct {
	Executor                        messages.ToolExecutor
	Definitions, ToolDefinitionBase []messages.ToolDefinition
	RefreshToolDefinitions          func(context.Context) ([]messages.ToolDefinition, error)
	BrowserWatch                    func(context.Context) <-chan webmcp.BrokerEvent
	BrowserEventWatch               func(context.Context) <-chan webmcp.BrowserEvent
	Initialize                      func(context.Context) error
	Close                           func() error
}
type RoomParticipantBrowserCapabilitiesFactory func(room.Participant) (RoomParticipantBrowserCapabilities, error)

func roomCapabilityBrowser(browser RoomParticipantBrowserCapabilities) roomcapabilities.BrowserCapabilities {
	return roomcapabilities.BrowserCapabilities{Executor: browser.Executor, Definitions: browser.Definitions, ToolDefinitionBase: browser.ToolDefinitionBase, RefreshToolDefinitions: browser.RefreshToolDefinitions, Initialize: browser.Initialize, Close: browser.Close}
}
func validateRoomParticipantBrowserCapabilities(participant room.Participant, capabilities RoomParticipantBrowserCapabilities) error {
	return roomcapabilitiesWire.NewService().ValidateBrowser(roomCapabilityBrowser(capabilities))
}
func composeRoomParticipantBrowserCapabilities(participant room.Participant, static RoomParticipantToolCapabilities, browser RoomParticipantBrowserCapabilities) (RoomParticipantBrowserCapabilities, error) {
	browserSurface := roomCapabilityBrowser(browser)
	composed, err := roomcapabilitiesWire.NewService().Compose(context.Background(), roomCapabilityParticipant(participant), static, &browserSurface)
	if err != nil {
		return RoomParticipantBrowserCapabilities{}, fmt.Errorf("compose participant browser tools: %w", err)
	}
	return RoomParticipantBrowserCapabilities{Executor: composed.Executor, Definitions: composed.Definitions, ToolDefinitionBase: composed.ToolDefinitionBase, RefreshToolDefinitions: composed.RefreshToolDefinitions, BrowserWatch: browser.BrowserWatch, BrowserEventWatch: browser.BrowserEventWatch, Initialize: composed.Initialize, Close: composed.Close}, nil
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
