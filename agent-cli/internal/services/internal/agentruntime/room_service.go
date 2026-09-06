package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	serviceRooms "github.com/portpowered/go-agent-harness/agent-cli/internal/services/rooms"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// roomService is the composition boundary for room orchestration. The device
// registry is installed once by Wire; callers cannot smuggle a gateway or
// provider runtime through the public request DTO.
type roomService struct {
	registry devicegw.DeviceRegistry
	clock    platformclock.Source
	factory  sessionRuntimeFactory
}

func NewRoomService(registry devicegw.DeviceRegistry, clock platformclock.Source, factory sessionRuntimeFactory) serviceRooms.Service {
	return &roomService{registry: registry, clock: clock, factory: factory}
}

func (s *roomService) Run(ctx context.Context, out io.Writer, request serviceRooms.RoomRunOptions) (serviceRooms.RoomResult, error) {
	options := RoomRunOptions{
		Manifest:       request.Manifest,
		ReplayPath:     request.ReplayPath,
		OutputDir:      request.OutputDir,
		ConfigDir:      request.ConfigDir,
		WorkDir:        request.WorkDir,
		AllowPaths:     append([]string(nil), request.AllowPaths...),
		DeviceRegistry: s.registry,
		Clock:          s.clock,
		runtimeFactory: s.factory,
	}
	if request.WorkDir != "" {
		policy, err := tools.ResolveFilesystemPolicy(request.WorkDir, request.AllowPaths...)
		if err != nil {
			return serviceRooms.RoomResult{}, err
		}
		options.FilesystemPolicy = policy
	}
	if request.ReplayPlan != nil {
		if request.ReplayPlan.BundlePath != "" {
			if plan, err := LoadRoomReplayPlan(request.ReplayPlan.BundlePath); err == nil {
				options.ReplayPlan = &plan
			} else {
				return serviceRooms.RoomResult{}, publicRoomError(err)
			}
		}
	}
	if request.LaunchPlan != nil {
		options.LaunchPlan = launchPlanFromContract(*request.LaunchPlan)
	}
	if request.Stream != nil {
		if broker, ok := request.Stream.(*RoomEventBroker); ok {
			options.Stream = broker
		}
	}
	if request.BrowserCapabilitiesFactory != nil {
		options.BrowserCapabilitiesFactory = func(participant room.Participant) (RoomParticipantBrowserCapabilities, error) {
			capabilities, err := request.BrowserCapabilitiesFactory(participant)
			if err != nil {
				return RoomParticipantBrowserCapabilities{}, err
			}
			return RoomParticipantBrowserCapabilities{
				Executor: capabilities.Executor, Definitions: capabilities.Definitions,
				ToolDefinitionBase:     capabilities.ToolDefinitionBase,
				RefreshToolDefinitions: capabilities.RefreshToolDefinitions,
				BrowserWatch:           capabilities.BrowserWatch, Initialize: capabilities.Initialize,
				Close: capabilities.Close,
			}, nil
		}
	}
	if request.OnDiagnostic != nil {
		options.OnDiagnostic = func(id string, record SessionDiagnosticRecord) { request.OnDiagnostic(id, record) }
	}
	if request.OnParticipantReady != nil {
		options.OnParticipantReady = func(ready RoomParticipantReady) {
			request.OnParticipantReady(serviceRooms.RoomParticipantReady{
				ID: ready.ID, ParticipantID: ready.ParticipantID, Kind: ready.Kind,
				InputDevice: ready.InputDevice, OutputDevice: ready.OutputDevice,
				Provider: ready.Provider, Model: ready.Model,
			})
		}
	}
	if request.OnParticipantTerminated != nil {
		options.OnParticipantTerminated = func(result RoomParticipantResult) {
			request.OnParticipantTerminated(roomParticipantResultToContract(result))
		}
	}
	result, err := RunRoomWithResult(ctx, out, options)
	return roomResultToContract(result), err
}

func (s *roomService) ResolveLaunchPlan(options serviceRooms.RoomLaunchOptions) (serviceRooms.RoomLaunchPlan, error) {
	plan, err := ResolveRoomLaunchPlan(RoomLaunchOptions{
		ConfigPath: options.ConfigPath, ManifestPath: options.ManifestPath,
		ConfigDir: options.ConfigDir, DeviceRegistry: s.registry,
		CredentialLookup: options.CredentialLookup,
	})
	if err != nil {
		if errors.Is(err, ErrRoomLaunchPathConflict) {
			return serviceRooms.RoomLaunchPlan{}, fmt.Errorf("%w: %v", serviceRooms.ErrRoomLaunchPathConflict, err)
		}
		return serviceRooms.RoomLaunchPlan{}, err
	}
	return launchPlanToContract(plan), nil
}

func (s *roomService) LoadReplayPlan(bundle string) (serviceRooms.RoomReplayPlan, error) {
	plan, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		if errors.Is(err, ErrRoomReplayBundleIncomplete) {
			return serviceRooms.RoomReplayPlan{}, fmt.Errorf("%w: %v", serviceRooms.ErrRoomReplayBundleIncomplete, err)
		}
		if errors.Is(err, ErrInvalidRoomReplayBundle) {
			return serviceRooms.RoomReplayPlan{}, fmt.Errorf("%w: %v", serviceRooms.ErrInvalidRoomReplayBundle, err)
		}
		return serviceRooms.RoomReplayPlan{}, err
	}
	return serviceRooms.RoomReplayPlan{BundlePath: plan.BundlePath, ManifestData: plan.Manifest()}, nil
}

func (s *roomService) ValidateReplayOutput(plan serviceRooms.RoomReplayPlan, destination string) error {
	if plan.BundlePath == "" {
		return serviceRooms.ErrRoomReplayBundleIncomplete
	}
	loaded, err := LoadRoomReplayPlan(plan.BundlePath)
	if err != nil {
		return publicRoomError(err)
	}
	return ValidateRoomReplayOutput(loaded, destination)
}

func publicRoomError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRoomReplayBundleIncomplete) {
		return fmt.Errorf("%w: %v", serviceRooms.ErrRoomReplayBundleIncomplete, err)
	}
	if errors.Is(err, ErrInvalidRoomReplayBundle) {
		return fmt.Errorf("%w: %v", serviceRooms.ErrInvalidRoomReplayBundle, err)
	}
	return err
}

func (s *roomService) ValidateEvidenceOutput(path string) error {
	return ValidateRoomEvidenceOutput(path)
}
func (s *roomService) CreateFreshRunDirectory(configDir string) (string, error) {
	return CreateFreshRoomRunDirectory(configDir)
}
func (s *roomService) NewEventBroker(ids []string) (serviceRooms.EventBroker, error) {
	return NewRoomEventBroker(ids)
}

func launchPlanToContract(plan RoomLaunchPlan) serviceRooms.RoomLaunchPlan {
	result := serviceRooms.RoomLaunchPlan{Mode: serviceRooms.RoomLaunchMode(plan.Mode), ConfigPath: plan.ConfigPath, ConfigDir: plan.ConfigDir, Manifest: plan.Manifest}
	result.Participants = make([]serviceRooms.RoomLaunchParticipantPlan, 0, len(plan.Participants))
	for _, participant := range plan.Participants {
		result.Participants = append(result.Participants, serviceRooms.RoomLaunchParticipantPlan{
			ID: participant.ID, Kind: participant.Kind, InputDevice: string(participant.InputDevice.ID), OutputDevice: string(participant.OutputDevice.ID),
			Provider: participant.Provider, Model: participant.Model, CredentialReference: participant.CredentialReference,
			CredentialProvenance: string(participant.CredentialProvenance),
		})
	}
	return result
}

func launchPlanFromContract(plan serviceRooms.RoomLaunchPlan) *RoomLaunchPlan {
	result := &RoomLaunchPlan{Mode: RoomLaunchMode(plan.Mode), ConfigPath: plan.ConfigPath, ConfigDir: plan.ConfigDir, Manifest: plan.Manifest}
	for _, participant := range plan.Participants {
		result.Participants = append(result.Participants, RoomLaunchParticipantPlan{ID: participant.ID, Kind: participant.Kind, Provider: participant.Provider, Model: participant.Model, CredentialReference: participant.CredentialReference, CredentialProvenance: RoomCredentialProvenance(participant.CredentialProvenance)})
	}
	return result
}

func roomParticipantResultToContract(result RoomParticipantResult) serviceRooms.RoomParticipantResult {
	return serviceRooms.RoomParticipantResult{ID: result.ID, ParticipantID: result.ParticipantID, TerminationReason: serviceRooms.ParticipantTerminationReason(result.TerminationReason), Reason: serviceRooms.ParticipantTerminationReason(result.Reason), TerminationTrigger: result.TerminationTrigger, TerminationDisposition: result.TerminationDisposition, Classification: result.Classification, TerminalReason: result.TerminalReason, TerminalProvenance: result.TerminalProvenance, OutputState: result.OutputState, TurnsCompleted: result.TurnsCompleted, Connected: result.Connected, Error: result.Error, RecordingStatus: result.RecordingStatus}
}

func roomResultToContract(result RoomResult) serviceRooms.RoomResult {
	converted := serviceRooms.RoomResult{TerminationReason: serviceRooms.RoomTerminationReason(result.TerminationReason), Reason: serviceRooms.RoomTerminationReason(result.Reason), ActiveParticipants: append([]string(nil), result.ActiveParticipants...), Error: result.Error, RecordingStatus: result.RecordingStatus}
	if result.DegradedArtifacts != nil {
		converted.DegradedArtifacts = map[string]string{}
		for key, value := range result.DegradedArtifacts {
			converted.DegradedArtifacts[key] = value
		}
	}
	converted.Participants = make(map[string]serviceRooms.RoomParticipantResult, len(result.Participants))
	for id, participant := range result.Participants {
		converted.Participants[id] = roomParticipantResultToContract(participant)
	}
	return converted
}
