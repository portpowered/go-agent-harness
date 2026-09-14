package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/internal/roomhost"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// roomRunPlans keeps command admission results together until the service
// receives one immutable launch or replay decision. It is presentation state
// for one command invocation, not room lifecycle state.
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

// NewRoomParticipantBrowserCapabilitiesFactory composes the existing CLI
// browser capability owner and gives the room service only the public runtime
// values. The roomhost package performs the stateless value conversion.
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
	return roomhost.NewBrowserCapabilitiesFactory(configDir, func(cfg *config.Config) (roomhost.Capabilities, error) {
		capabilities, err := browserFactory(cfg)
		if err != nil {
			return roomhost.Capabilities{}, err
		}
		return roomhost.Capabilities{
			Executor: capabilities.Executor, Definitions: capabilities.Definitions,
			RefreshToolDefinitions: capabilities.RefreshDefinitionsWithError,
			BrowserWatch:           capabilities.BrowserWatch, BrowserEventWatch: capabilities.BrowserEventWatch,
			Initialize: capabilities.Initialize, Close: capabilities.Close,
		}, nil
	})
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

// The session capability factory treats a non-registry executor as an
// already-resolved static surface. Room participants compose their own
// browser owner while the runtime service supplies the participant tool list.
type roomBrowserOnlyStaticExecutor struct{}

func (roomBrowserOnlyStaticExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, errors.New("room browser-only static executor has no tools")
}
