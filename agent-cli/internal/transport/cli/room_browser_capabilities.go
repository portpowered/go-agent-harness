package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const runtimeRoomBrowserEventCapacity = 16

// roomRunPlans keeps command admission results together until the service
// receives one immutable launch or replay decision. It lives with the room
// adapters because both browser and device admission consume that decision.
type roomRunPlans struct {
	replayMode bool
	replayPath string
	launchPlan runtimeRooms.RoomLaunchPlan
	replayPlan runtimeRooms.RoomReplayPlan
	manifest   runtimeRooms.Manifest
}

func (c *RoomRunCommand) resolveRoomRunPlans(configPath, manifestPath, replayPath string) (roomRunPlans, error) {
	replayPath = strings.TrimSpace(replayPath)
	plans := roomRunPlans{replayPath: replayPath, replayMode: replayPath != ""}
	if plans.replayMode {
		if strings.TrimSpace(configPath) != "" || strings.TrimSpace(manifestPath) != "" {
			return roomRunPlans{}, fmt.Errorf("%w: --replay cannot be combined with --config or --manifest", runtimeRooms.ErrReplaySourceConflict)
		}
		var err error
		plans.replayPlan, err = c.service.LoadReplayPlan(replayPath)
		if err != nil {
			return roomRunPlans{}, err
		}
		plans.manifest = plans.replayPlan.Manifest()
		return plans, nil
	}
	var err error
	plans.launchPlan, err = c.service.ResolveLaunchPlan(runtimeRooms.RoomLaunchOptions{
		ConfigPath: configPath, ManifestPath: manifestPath, ConfigDir: roomConfigDir(roomRunGlobalFlags(c)),
	})
	if err != nil {
		return roomRunPlans{}, err
	}
	plans.manifest = plans.launchPlan.Manifest
	return plans, nil
}

func (c *RoomRunCommand) resolveRoomOutput(plans roomRunPlans, requested string, explicit bool) (string, error) {
	if plans.replayMode {
		requested = resolveRoomReplayCommandOutputDir(requested)
	} else {
		var err error
		requested, err = resolveRoomCommandOutputDir(c.service, plans.launchPlan, requested, explicit)
		if err != nil {
			return "", err
		}
	}
	if err := validateRoomOutput(c.service, plans, requested); err != nil {
		return "", err
	}
	return requested, nil
}

func validateRoomOutput(service runtimeRooms.Service, plans roomRunPlans, outputDir string) error {
	if outputDir == "" {
		return nil
	}
	if plans.replayMode {
		if err := service.ValidateReplayOutput(plans.replayPlan, outputDir); err != nil {
			return fmt.Errorf("validate --out %q: %w", outputDir, err)
		}
	}
	if err := service.ValidateEvidenceOutput(outputDir); err != nil {
		return fmt.Errorf("validate --out %q: %w", outputDir, err)
	}
	return nil
}

// NewRoomParticipantBrowserCapabilitiesFactory creates the production room
// adapter. The session browser composition remains the single source for
// broker tools, initialization, and cleanup; this adapter only changes the
// selection store to a fresh in-memory store for each room participant.
func NewRoomParticipantBrowserCapabilitiesFactory(configDir string) runtimeRooms.BrowserCapabilitiesFactory {
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

	return func(participant runtimeRooms.Participant) (runtimeRooms.BrowserCapabilities, error) {
		if participant.BrowserTools == nil {
			return runtimeRooms.BrowserCapabilities{}, errors.New("room browser capability requested for a participant without browserTools")
		}
		capabilities, err := browserFactory(&config.Config{
			Browser:   runtimeBrowserConfig(*participant.BrowserTools),
			ConfigDir: configDir,
		})
		if err != nil {
			return runtimeRooms.BrowserCapabilities{}, err
		}
		return runtimeRooms.BrowserCapabilities{
			Executor: capabilities.Executor, Definitions: capabilities.Definitions,
			ToolDefinitionBase:     append([]messages.ToolDefinition(nil), capabilities.Definitions...),
			RefreshToolDefinitions: capabilities.RefreshDefinitionsWithError,
			BrowserWatch:           runtimeBrowserWatch(capabilities.BrowserEventWatch, capabilities.BrowserWatch),
			Initialize:             capabilities.Initialize, Close: capabilities.Close,
		}, nil
	}
}

func runtimeBrowserConfig(value runtimeRooms.BrowserToolsConfig) config.BrowserConfig {
	result := config.DefaultBrowserConfig()
	result.Tools.Enabled = true
	result.Tools.Backend = value.Backend
	result.Connection.CDPURL = value.Connection.CDPURL
	result.Connection.WSEndpoint = value.Connection.WSEndpoint
	result.Connection.UserDataDir = value.Connection.UserDataDir
	result.Connection.AllowProcessScan = value.Connection.AllowProcessScan
	result.Connection.AllowRemoteCDP = value.Connection.AllowRemoteCDP
	result.Selection.Browser = value.Selection.Browser
	result.Selection.Tab = value.Selection.Tab
	result.Selection.Origin = value.Selection.Origin
	result.Selection.AutoSelect = value.Selection.AutoSelect
	result.Selection.ActivateTab = value.Selection.ActivateTab
	result.Selection.Persist = value.Selection.Persist
	result.Policy.AllowedOrigins = append([]string(nil), value.Policy.AllowedOrigins...)
	result.Policy.DeniedOrigins = append([]string(nil), value.Policy.DeniedOrigins...)
	result.Policy.Approval = value.Policy.Approval
	result.Policy.CancelOnInterrupt = value.Policy.CancelOnInterrupt
	result.Limits.InvocationTimeout = value.Limits.InvocationTimeout
	result.Limits.MaxInputBytes = value.Limits.MaxInputBytes
	result.Limits.MaxResultBytes = value.Limits.MaxResultBytes
	result.Limits.SerializePerTarget = value.Limits.SerializePerTarget
	result.Recording.Enabled = value.Recording.Enabled
	result.Recording.IncludeArguments = value.Recording.IncludeArguments
	result.Recording.IncludeResults = value.Recording.IncludeResults
	result.Recording.RedactURLQuery = value.Recording.RedactURLQuery
	result.Recording.RedactURLFragment = value.Recording.RedactURLFragment
	result.Replay.Path = value.Replay.Path
	result.Replay.Strict = value.Replay.Strict
	return result
}

func defaultRuntimeBrowserToolsConfig() runtimeRooms.BrowserToolsConfig {
	defaults := config.DefaultBrowserConfig()
	return runtimeRooms.BrowserToolsConfig{
		Backend: defaults.Tools.Backend,
		Connection: runtimeRooms.BrowserConnectionConfig{
			CDPURL: defaults.Connection.CDPURL, WSEndpoint: defaults.Connection.WSEndpoint,
			UserDataDir: defaults.Connection.UserDataDir, AllowProcessScan: defaults.Connection.AllowProcessScan,
			AllowRemoteCDP: defaults.Connection.AllowRemoteCDP,
		},
		Selection: runtimeRooms.BrowserSelectionConfig{
			Browser: defaults.Selection.Browser, Tab: defaults.Selection.Tab, Origin: defaults.Selection.Origin,
			AutoSelect: defaults.Selection.AutoSelect, ActivateTab: defaults.Selection.ActivateTab,
			Persist: defaults.Selection.Persist,
		},
		Policy: runtimeRooms.BrowserPolicyConfig{
			AllowedOrigins: append([]string(nil), defaults.Policy.AllowedOrigins...),
			DeniedOrigins:  append([]string(nil), defaults.Policy.DeniedOrigins...),
			Approval:       defaults.Policy.Approval, CancelOnInterrupt: defaults.Policy.CancelOnInterrupt,
		},
		Limits: runtimeRooms.BrowserLimitsConfig{
			InvocationTimeout: defaults.Limits.InvocationTimeout, MaxInputBytes: defaults.Limits.MaxInputBytes,
			MaxResultBytes: defaults.Limits.MaxResultBytes, SerializePerTarget: defaults.Limits.SerializePerTarget,
		},
		Recording: runtimeRooms.BrowserRecordingConfig{
			Enabled: defaults.Recording.Enabled, IncludeArguments: defaults.Recording.IncludeArguments,
			IncludeResults: defaults.Recording.IncludeResults, RedactURLQuery: defaults.Recording.RedactURLQuery,
			RedactURLFragment: defaults.Recording.RedactURLFragment,
		},
		Replay: runtimeRooms.BrowserReplayConfig{Path: defaults.Replay.Path, Strict: defaults.Replay.Strict},
	}
}

func runtimeBrowserWatch(semantic func(context.Context) <-chan webmcp.BrowserEvent, legacy func(context.Context) <-chan webmcp.BrokerEvent) func(context.Context) <-chan runtimeRooms.BrowserEvent {
	if semantic != nil {
		return func(ctx context.Context) <-chan runtimeRooms.BrowserEvent {
			input := semantic(ctx)
			return mapRoomRuntimeBrowserEvents(ctx, input)
		}
	}
	if legacy == nil {
		return nil
	}
	return func(ctx context.Context) <-chan runtimeRooms.BrowserEvent {
		return mapRoomLegacyBrowserEvents(ctx, legacy(ctx))
	}
}

func mapRoomLegacyBrowserEvents(ctx context.Context, input <-chan webmcp.BrokerEvent) <-chan runtimeRooms.BrowserEvent {
	if input == nil {
		return nil
	}
	output := make(chan runtimeRooms.BrowserEvent, runtimeRoomBrowserEventCapacity)
	go func() {
		defer close(output)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-input:
				if !ok {
					return
				}
				value := runtimeRooms.BrowserEvent{
					Type: string(event.Type), Sequence: event.Sequence, At: event.At,
					BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
					Generation: event.Generation, InvocationID: string(event.InvocationID),
					ToolName: event.ToolName, State: string(event.State), Reason: event.Reason,
				}
				select {
				case output <- value:
				case <-ctx.Done():
					return
				default:
				}
			}
		}
	}()
	return output
}

func mapRoomRuntimeBrowserEvents(ctx context.Context, input <-chan webmcp.BrowserEvent) <-chan runtimeRooms.BrowserEvent {
	if input == nil {
		return nil
	}
	output := make(chan runtimeRooms.BrowserEvent, runtimeRoomBrowserEventCapacity)
	go func() {
		defer close(output)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-input:
				if !ok {
					return
				}
				value := runtimeRooms.BrowserEvent{
					Type: string(event.Type), Sequence: event.Sequence, At: event.At,
					BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
					Generation: event.Generation, PreviousGeneration: event.PreviousGeneration,
					InvocationID: string(event.InvocationID), ToolName: event.ToolName,
					State: string(event.Status), Status: event.Status, ErrorCode: event.ErrorCode,
					Reason: event.Reason, CatalogReady: event.CatalogReady, ToolCount: event.ToolCount,
					ToolCountKnown: event.ToolCountKnown,
				}
				select {
				case output <- value:
				case <-ctx.Done():
					return
				default:
				}
			}
		}
	}()
	return output
}

// The session capability factory treats a non-registry executor as an
// already-resolved static surface. Keeping this adapter empty lets the room
// service compose the participant's allowlisted tools separately while still
// reusing the production browser capability implementation.
type roomBrowserOnlyStaticExecutor struct{}

func (roomBrowserOnlyStaticExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, errors.New("room browser-only static executor has no tools")
}
