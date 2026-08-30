package cli

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// NewRoomParticipantBrowserCapabilitiesFactory creates the production room
// adapter. The session browser composition remains the single source for
// broker tools, initialization, and cleanup; this adapter only changes the
// selection store to a fresh in-memory store for each room participant.
func NewRoomParticipantBrowserCapabilitiesFactory(configDir string) services.RoomParticipantBrowserCapabilitiesFactory {
	browserFactory := NewSessionToolCapabilitiesFactory(
		roomBrowserOnlyStaticExecutor{},
		func(browser config.BrowserConfig) (webmcp.Broker, error) {
			selectionStore := discovery.NewMemorySelectionStore()
			doctorFactory := NewProductionWebMCPDoctorFactory(
				WithWebMCPProductionSelectionStore(selectionStore),
			)
			return newSessionBrowserBrokerWithDoctorFactory(browser, doctorFactory)
		},
	)

	return func(participant room.Participant) (services.RoomParticipantBrowserCapabilities, error) {
		if participant.BrowserTools == nil {
			return services.RoomParticipantBrowserCapabilities{}, errors.New("room browser capability requested for a participant without browserTools")
		}
		capabilities, err := browserFactory(&config.Config{
			Browser:   participant.BrowserTools.AsBrowserConfig(),
			ConfigDir: configDir,
		})
		if err != nil {
			return services.RoomParticipantBrowserCapabilities{}, err
		}
		return services.RoomParticipantBrowserCapabilities{
			Executor:               capabilities.Executor,
			Definitions:            capabilities.Definitions,
			ToolDefinitionBase:     capabilities.Definitions,
			RefreshToolDefinitions: capabilities.RefreshDefinitionsWithError,
			BrowserWatch:           capabilities.BrowserWatch,
			Initialize:             capabilities.Initialize,
			Close:                  capabilities.Close,
		}, nil
	}
}

// The session capability factory treats a non-registry executor as an
// already-resolved static surface. Keeping this adapter empty lets the room
// service compose the participant's allowlisted tools separately while still
// reusing the production browser capability implementation.
type roomBrowserOnlyStaticExecutor struct{}

func (roomBrowserOnlyStaticExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, errors.New("room browser-only static executor has no tools")
}
