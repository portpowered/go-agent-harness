// Package roomhost contains the stateless CLI-to-runtime room adapter.
//
// It translates command-owned browser configuration and event values at the
// room service boundary. It retains no participant, browser, or run state.
package roomhost

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const browserEventCapacity = 16

// Capabilities is the browser transport shape produced by the existing CLI
// capability composition. The adapter only projects it into the public room
// contract and does not own its lifecycle.
type Capabilities struct {
	Executor               messages.ToolExecutor
	Definitions            []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	BrowserWatch           func(context.Context) <-chan webmcp.BrokerEvent
	BrowserEventWatch      func(context.Context) <-chan webmcp.BrowserEvent
	Initialize             func(context.Context) error
	Close                  func() error
}

// Compose resolves one participant's browser capability using the host's
// existing composition. The callback is invoked once per participant, so a
// returned capability has an independent owner.
type Compose func(*config.Config) (Capabilities, error)

// NewBrowserCapabilitiesFactory converts the command's participant document
// into the runtime room browser contract. The returned closure is stateless;
// ownership of any browser resources remains in the returned Close function.
func NewBrowserCapabilitiesFactory(configDir string, compose Compose) runtimeRooms.BrowserCapabilitiesFactory {
	return func(participant runtimeRooms.Participant) (runtimeRooms.BrowserCapabilities, error) {
		if participant.BrowserTools == nil {
			return runtimeRooms.BrowserCapabilities{}, errors.New("room browser capability requested for a participant without browserTools")
		}
		if compose == nil {
			return runtimeRooms.BrowserCapabilities{}, errors.New("room browser capability composition is unavailable")
		}
		capabilities, err := compose(&config.Config{
			Browser:   browserConfig(*participant.BrowserTools),
			ConfigDir: configDir,
		})
		if err != nil {
			return runtimeRooms.BrowserCapabilities{}, err
		}
		return runtimeRooms.BrowserCapabilities{
			Executor: capabilities.Executor, Definitions: capabilities.Definitions,
			ToolDefinitionBase:     append([]messages.ToolDefinition(nil), capabilities.Definitions...),
			RefreshToolDefinitions: capabilities.RefreshToolDefinitions,
			BrowserWatch:           browserWatch(capabilities.BrowserEventWatch, capabilities.BrowserWatch),
			Initialize:             capabilities.Initialize, Close: capabilities.Close,
		}, nil
	}
}

func browserConfig(value runtimeRooms.BrowserToolsConfig) config.BrowserConfig {
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

func browserWatch(semantic func(context.Context) <-chan webmcp.BrowserEvent, legacy func(context.Context) <-chan webmcp.BrokerEvent) func(context.Context) <-chan runtimeRooms.BrowserEvent {
	if semantic != nil {
		return func(ctx context.Context) <-chan runtimeRooms.BrowserEvent { return mapEvents(ctx, semantic(ctx)) }
	}
	if legacy == nil {
		return nil
	}
	return func(ctx context.Context) <-chan runtimeRooms.BrowserEvent { return mapLegacyEvents(ctx, legacy(ctx)) }
}

func mapLegacyEvents(ctx context.Context, input <-chan webmcp.BrokerEvent) <-chan runtimeRooms.BrowserEvent {
	if input == nil {
		return nil
	}
	output := make(chan runtimeRooms.BrowserEvent, browserEventCapacity)
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
				value := runtimeRooms.BrowserEvent{Type: string(event.Type), Sequence: event.Sequence, At: event.At, BrowserID: string(event.BrowserID), TargetID: string(event.TargetID), Generation: event.Generation, InvocationID: string(event.InvocationID), ToolName: event.ToolName, State: string(event.State), Reason: event.Reason}
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

func mapEvents(ctx context.Context, input <-chan webmcp.BrowserEvent) <-chan runtimeRooms.BrowserEvent {
	if input == nil {
		return nil
	}
	output := make(chan runtimeRooms.BrowserEvent, browserEventCapacity)
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
				value := runtimeRooms.BrowserEvent{Type: string(event.Type), Sequence: event.Sequence, At: event.At, BrowserID: string(event.BrowserID), TargetID: string(event.TargetID), Generation: event.Generation, PreviousGeneration: event.PreviousGeneration, InvocationID: string(event.InvocationID), ToolName: event.ToolName, State: string(event.Status), Status: event.Status, ErrorCode: event.ErrorCode, Reason: event.Reason, CatalogReady: event.CatalogReady, ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown}
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
