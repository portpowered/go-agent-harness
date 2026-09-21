package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
)

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func timerChannel(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func sortedRoomIDs(ids []string) []string {
	result := append([]string(nil), ids...)
	for index := 1; index < len(result); index++ {
		value := result[index]
		position := index
		for position > 0 && result[position-1] > value {
			result[position] = result[position-1]
			position--
		}
		result[position] = value
	}
	return result
}

func roomFormatForOptions(opts RoomRunOptions) room.PCM16Format {
	format := roomMixerConfigForOptions(opts).Format
	if format == (room.PCM16Format{}) {
		return room.DefaultPCM16Format()
	}
	return format
}

func configureRoomParticipantBrowserOptions(ctx context.Context, opts RoomRunOptions, participant room.Participant, plan *roomParticipantPlan, sessionOptions SessionRunOptions, staticCapabilities RoomParticipantToolCapabilities, secret string) (SessionRunOptions, bool, error) {
	if participant.BrowserTools == nil {
		return sessionOptions, false, nil
	}
	if opts.BrowserCapabilitiesFactory == nil {
		return roomParticipantBrowserStartupFailure(sessionOptions, plan, participant, secret, ErrRoomParticipantBrowserToolsUnavailable)
	}
	browserCapabilities, err := opts.BrowserCapabilitiesFactory(participant)
	if err != nil {
		return roomParticipantBrowserStartupFailure(sessionOptions, plan, participant, secret, fmt.Errorf("configure browser tools: %w", err))
	}
	plan.capabilityCoordinator = NewSessionCapabilityCoordinator(browserCapabilities.Close)
	if err := validateRoomParticipantBrowserCapabilities(participant, browserCapabilities); err != nil {
		if errors.Is(err, ErrRoomParticipantBrowserToolMismatch) {
			return sessionOptions, false, fmt.Errorf("room participant %q browser capability contract: %w", participant.ID, err)
		}
		return roomParticipantBrowserStartupFailure(sessionOptions, plan, participant, secret, err)
	}
	composed, err := composeRoomParticipantBrowserCapabilities(participant, staticCapabilities, browserCapabilities)
	if err != nil {
		return sessionOptions, false, fmt.Errorf("room participant %q browser composition contract: %w", participant.ID, err)
	}
	composed, err = initializeRoomParticipantBrowserCapabilities(ctx, composed)
	if err != nil {
		return roomParticipantBrowserStartupFailure(sessionOptions, plan, participant, secret, err)
	}
	sessionOptions.ToolExecutor = composed.Executor
	sessionOptions.ToolDefinitions = cloneRoomToolDefinitions(composed.Definitions)
	sessionOptions.ToolDefinitionBase = cloneRoomToolDefinitions(composed.ToolDefinitionBase)
	sessionOptions.RefreshToolDefinitions = composed.RefreshToolDefinitions
	sessionOptions.BrowserWatch = composed.BrowserWatch
	sessionOptions.BrowserToolsEnabled = true
	sessionOptions.CapabilityClose = plan.capabilityCoordinator.Close
	return sessionOptions, false, nil
}

func roomParticipantBrowserStartupFailure(sessionOptions SessionRunOptions, plan *roomParticipantPlan, participant room.Participant, secret string, err error) (SessionRunOptions, bool, error) {
	plan.startupErr = roomParticipantFailure(participant.ID, err, []string{secret})
	return sessionOptions, true, nil
}

func initializeRoomParticipantBrowserCapabilities(ctx context.Context, capabilities RoomParticipantBrowserCapabilities) (RoomParticipantBrowserCapabilities, error) {
	if capabilities.Initialize != nil {
		if err := capabilities.Initialize(ctx); err != nil {
			return capabilities, fmt.Errorf("initialize browser tools: %w", err)
		}
	}
	if capabilities.RefreshToolDefinitions == nil {
		return capabilities, nil
	}
	definitions, err := capabilities.RefreshToolDefinitions(ctx)
	if err == nil {
		capabilities.Definitions = cloneRoomToolDefinitions(definitions)
		return capabilities, nil
	}
	if ctx.Err() != nil {
		return capabilities, fmt.Errorf("refresh browser tools: %w", err)
	}
	return capabilities, nil
}
