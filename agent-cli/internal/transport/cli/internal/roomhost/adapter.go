// Package roomhost translates CLI host inputs into the public room contract.
// It holds no room state and makes no room decision: admission, launch
// planning, output policy, and browser event delivery belong to the rooms
// service. The adapter only projects host types onto that contract.
package roomhost

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// Paths are the invocation's room sources as named on the command line.
type Paths struct {
	Config    string
	Manifest  string
	Replay    string
	ConfigDir string
}

// RunPlanOptions binds the host device registry and config credential store
// to one run-plan request. A nil registry leaves the device port unset.
func RunPlanOptions(paths Paths, registry devicegw.DeviceRegistry) rooms.RoomRunPlanOptions {
	launch := rooms.RoomLaunchOptions{
		ConfigPath: paths.Config, ManifestPath: paths.Manifest, ConfigDir: paths.ConfigDir,
		ConfigCredential: ConfigCredential(paths.ConfigDir),
	}
	if registry != nil {
		launch.Devices = devices{registry: registry}
	}
	return rooms.RoomRunPlanOptions{Launch: launch, ReplayPath: paths.Replay}
}

// RunOptions projects an admitted plan onto the run request.
func RunOptions(plan rooms.RoomRunPlan) rooms.RoomRunOptions {
	return rooms.RoomRunOptions{Manifest: plan.Manifest, ReplayPath: plan.ReplayPath, ReplayPlan: plan.ReplayPlan, LaunchPlan: plan.LaunchPlan}
}

type devices struct{ registry devicegw.DeviceRegistry }

func (d devices) List() ([]rooms.LaunchDevice, error) {
	listed, err := d.registry.List()
	if err != nil {
		return nil, err
	}
	result := make([]rooms.LaunchDevice, 0, len(listed))
	for _, device := range listed {
		if device.Validate() == nil {
			result = append(result, launchDevice(device))
		}
	}
	return result, nil
}

func (d devices) Default(direction rooms.LaunchDeviceDirection) (rooms.LaunchDevice, error) {
	device, err := d.registry.Default(devicegw.Direction(direction))
	if err != nil {
		return rooms.LaunchDevice{}, err
	}
	if err := device.Validate(); err != nil {
		return rooms.LaunchDevice{}, err
	}
	return launchDevice(device), nil
}

func launchDevice(device devicegw.Device) rooms.LaunchDevice {
	return rooms.LaunchDevice{ID: device.ID, Direction: rooms.LaunchDeviceDirection(device.Direction)}
}

// ConfigCredential reads the host-configured fallback credential for a room
// credential reference. Launch planning uses it to admit the bare room, and
// the run uses it so evidence redacts the configured key.
func ConfigCredential(configDir string) rooms.ConfigCredentialLookup {
	return func(name string) (string, error) {
		storage, err := config.NewDefaultConfigStorage(configDir)
		if err != nil {
			return "", fmt.Errorf("initialize room config: %w", err)
		}
		loaded, err := storage.Load()
		if err != nil {
			return "", fmt.Errorf("load room config: %w", err)
		}
		if name != rooms.DefaultOpenAIAPIKeyEnv || loaded == nil || loaded.Model.OpenAI == nil {
			return "", nil
		}
		return loaded.Model.OpenAI.APIKey, nil
	}
}

// BrowserWatch projects the host browser event stream onto the room browser
// watch, preferring semantic browser events over legacy broker events.
func BrowserWatch(semantic func(context.Context) <-chan webmcp.BrowserEvent, legacy func(context.Context) <-chan webmcp.BrokerEvent) func(context.Context) <-chan rooms.BrowserEvent {
	switch {
	case semantic != nil:
		return func(ctx context.Context) <-chan rooms.BrowserEvent {
			return roomswire.BrowserEventStream(ctx, semantic(ctx), semanticEvent)
		}
	case legacy != nil:
		return func(ctx context.Context) <-chan rooms.BrowserEvent {
			return roomswire.BrowserEventStream(ctx, legacy(ctx), legacyEvent)
		}
	default:
		return nil
	}
}

func semanticEvent(event webmcp.BrowserEvent) rooms.BrowserEvent {
	return rooms.BrowserEvent{
		Type: string(event.Type), Sequence: event.Sequence, At: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, PreviousGeneration: event.PreviousGeneration,
		InvocationID: string(event.InvocationID), ToolName: event.ToolName,
		State: event.Status, Status: event.Status, ErrorCode: event.ErrorCode,
		Reason: event.Reason, CatalogReady: event.CatalogReady, ToolCount: event.ToolCount,
		ToolCountKnown: event.ToolCountKnown,
	}
}

func legacyEvent(event webmcp.BrokerEvent) rooms.BrowserEvent {
	return rooms.BrowserEvent{
		Type: string(event.Type), Sequence: event.Sequence, At: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID),
		Generation: event.Generation, InvocationID: string(event.InvocationID),
		ToolName: event.ToolName, State: string(event.State), Reason: event.Reason,
	}
}
