package rooms

import (
	"fmt"
	"strings"
)

// ContractError is an immutable room sentinel. Constants keep newer room
// contract failures comparable with errors.Is without mutable package state.
type ContractError string

func (e ContractError) Error() string { return string(e) }

const (
	// ErrLaunchDeviceInventoryUnavailable identifies a launch that needs host
	// devices but was resolved without a device inventory port.
	ErrLaunchDeviceInventoryUnavailable ContractError = "room launch device inventory is unavailable"
	// ErrLaunchDeviceUnavailable identifies a configured device selector that
	// is absent from the host's current device inventory.
	ErrLaunchDeviceUnavailable ContractError = "room launch device is unavailable"
	// ErrLaunchDeviceDirectionMismatch identifies a selector whose host device
	// direction does not match the participant field that names it.
	ErrLaunchDeviceDirectionMismatch ContractError = "room launch device direction mismatch"
	// ErrAllParticipantsFailed identifies a room whose admitted participants
	// all terminated with participant-local failures.
	ErrAllParticipantsFailed ContractError = "all room participants failed"
)

// DefaultOpenAIAPIKeyEnv names the credential reference used by the bare
// customer-plus-agent room.
const DefaultOpenAIAPIKeyEnv = "AGENT_MODEL__OPENAI__API_KEY"

// DefaultRealtimeModel is the provider model used by the bare room agent.
const DefaultRealtimeModel = "gpt-realtime-2.1-mini"

// LaunchDeviceDirection names the host media direction of a launch device.
type LaunchDeviceDirection string

const (
	LaunchDeviceInput  LaunchDeviceDirection = "input"
	LaunchDeviceOutput LaunchDeviceDirection = "output"
)

// LaunchDevice is the discovery-free device metadata used to validate device
// selectors before a room runs. The planner never opens a device.
type LaunchDevice struct {
	ID        string
	Direction LaunchDeviceDirection
}

// LaunchDevices is the host device port consulted during launch planning.
// List returns the currently known devices; Default returns the host default
// for one direction and is consulted only for the bare room.
type LaunchDevices interface {
	List() ([]LaunchDevice, error)
	Default(LaunchDeviceDirection) (LaunchDevice, error)
}

// LaunchDeviceDirectionError reports a selector that names a device with the
// wrong direction. It matches ErrLaunchDeviceDirectionMismatch.
type LaunchDeviceDirectionError struct {
	ID   string
	Want LaunchDeviceDirection
	Got  LaunchDeviceDirection
}

func (e *LaunchDeviceDirectionError) Error() string {
	return fmt.Sprintf("device %q has direction %q, want %q", e.ID, e.Got, e.Want)
}

func (e *LaunchDeviceDirectionError) Is(target error) bool {
	return target == ErrLaunchDeviceDirectionMismatch
}

// ConfigCredentialLookup returns a host-configured credential value for a
// credential reference. The planner inspects the value only to establish
// availability and never retains it in a plan.
type ConfigCredentialLookup func(string) (string, error)

// RoomRunPlanOptions contains the caller inputs used to admit one configured
// run or one offline replay. Replay admission is mutually exclusive with live
// config and manifest inputs.
type RoomRunPlanOptions struct {
	Launch     RoomLaunchOptions
	ReplayPath string
}

// RoomRunPlan is the admission result for one room invocation. A live run
// has LaunchPlan set; an offline replay has ReplayPlan and ReplayPath set.
type RoomRunPlan struct {
	LaunchPlan *RoomLaunchPlan
	ReplayPlan *RoomReplayPlan
	ReplayPath string
	Manifest   Manifest
}

// Replay reports whether the plan admits an offline replay.
func (p RoomRunPlan) Replay() bool { return p.ReplayPlan != nil }

// ParticipantFailureDetail is one participant-local failure summarized by
// AllParticipantsFailedError.
type ParticipantFailureDetail struct {
	ParticipantID string
	Error         string
}

// AllParticipantsFailedError reports a room run in which every participant
// terminated with an error. It matches ErrAllParticipantsFailed.
type AllParticipantsFailedError struct {
	Participants []ParticipantFailureDetail
}

func (e *AllParticipantsFailedError) Error() string {
	details := make([]string, 0, len(e.Participants))
	for _, participant := range e.Participants {
		details = append(details, participant.ParticipantID+": "+participant.Error)
	}
	return fmt.Sprintf("room run: all %d participant(s) failed (%s)", len(e.Participants), strings.Join(details, "; "))
}

func (e *AllParticipantsFailedError) Is(target error) bool { return target == ErrAllParticipantsFailed }
