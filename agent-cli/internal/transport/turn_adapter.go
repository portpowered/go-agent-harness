package transport

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// TurnAdapter is a stateless transport seam. The session-turn service owns
// all mutable state, policy, composition, and lifecycle decisions.
type TurnAdapter struct{ service sessionturn.Service }

func NewTurnAdapter(service sessionturn.Service) TurnAdapter { return TurnAdapter{service: service} }

func (a TurnAdapter) Prepare(ctx context.Context, request sessionturn.Request) (sessionturn.Runtime, error) {
	if a.service == nil {
		return nil, sessionturn.ErrMissingTurnInferencer
	}
	return a.service.Prepare(ctx, request)
}

func (TurnAdapter) Run(ctx context.Context, runtime sessionturn.Runtime, request sessionturn.TurnRequest) (sessionturn.TurnResult, error) {
	if runtime == nil {
		return sessionturn.TurnResult{}, sessionturn.ErrSessionClosed
	}
	return runtime.RunTurn(ctx, request)
}

func (TurnAdapter) Output(runtime sessionturn.Runtime, writer io.Writer) sessionturn.Output {
	if runtime == nil {
		return nil
	}
	return runtime.NewOutput(writer)
}

func StopPublication(publication sessionturn.Publication) {
	if publication != nil {
		publication.Stop()
	}
}

func MarkPublicationReady(publication sessionturn.Publication) {
	if publication != nil {
		publication.MarkReady()
	}
}

const browserEventBufferSize = 16

// SessionTurnBrowserRequest converts broker events into the neutral service
// contract without retaining broker or session state.
func SessionTurnBrowserRequest(watch func(context.Context) <-chan webmcp.BrokerEvent, refresh func(context.Context) ([]messages.ToolDefinition, error)) sessionturn.BrowserRequest {
	request := sessionturn.BrowserRequest{Refresh: refresh}
	if watch != nil {
		request.Watch = func(ctx context.Context) <-chan sessionturn.BrowserEvent {
			input := watch(ctx)
			if input == nil {
				return nil
			}
			output := make(chan sessionturn.BrowserEvent, browserEventBufferSize)
			go forwardBrowserEvents(ctx, input, output)
			return output
		}
	}
	return request
}

func forwardBrowserEvents(ctx context.Context, input <-chan webmcp.BrokerEvent, output chan<- sessionturn.BrowserEvent) {
	defer close(output)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-input:
			if !ok {
				return
			}
			converted, ok := browserEvent(event)
			if !ok {
				continue
			}
			select {
			case output <- converted:
			case <-ctx.Done():
				return
			}
		}
	}
}

func browserEvent(event webmcp.BrokerEvent) (sessionturn.BrowserEvent, bool) {
	var kind sessionturn.BrowserEventType
	switch event.Type {
	case webmcp.BrokerEventSelected:
		kind = sessionturn.BrowserEventSelectionChanged
	case webmcp.BrokerEventCatalogChanged:
		kind = sessionturn.BrowserEventCatalogChanged
	case webmcp.BrokerEventGenerationChanged:
		kind = sessionturn.BrowserEventGenerationChanged
	case webmcp.BrokerEventInvocationCreated, webmcp.BrokerEventInvocationTerminal, webmcp.BrokerEventSessionClosed:
		return sessionturn.BrowserEvent{}, false
	default:
		return sessionturn.BrowserEvent{}, false
	}
	return sessionturn.BrowserEvent{Type: kind, BrowserID: string(event.BrowserID), TargetID: string(event.TargetID), Generation: event.Generation, Sequence: event.Sequence}, true
}

type TurnPublicationOptions struct {
	Runtime                sessionturn.Runtime
	Inferencer             messages.SessionInferencer
	ToolExecutor           messages.ToolExecutor
	ToolDefinitions        []messages.ToolDefinition
	InteractivePolicy      tools.InteractiveToolPolicy
	ToolExecutionTimeout   time.Duration
	Browser                sessionturn.BrowserRequest
	BrowserWatch           func(context.Context) <-chan webmcp.BrokerEvent
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	BaseDefinitions        []messages.ToolDefinition
}

func (a TurnAdapter) StartPublication(ctx context.Context, loop *agentloop.AgentLoop, options TurnPublicationOptions) (sessionturn.Publication, <-chan error) {
	browser := options.Browser
	if browser.Watch == nil && browser.Refresh == nil {
		browser = SessionTurnBrowserRequest(options.BrowserWatch, options.RefreshToolDefinitions)
	}
	runtime := options.Runtime
	if runtime == nil && browser.Watch != nil && browser.Refresh != nil {
		var err error
		runtime, err = a.Prepare(ctx, sessionturn.Request{
			SessionInferencer:     options.Inferencer,
			ToolExecutor:          options.ToolExecutor,
			ToolDefinitions:       options.ToolDefinitions,
			InteractiveToolPolicy: options.InteractivePolicy,
			ToolExecutionTimeout:  options.ToolExecutionTimeout,
		})
		if err != nil {
			return nil, publicationError(err)
		}
	}
	if runtime == nil || browser.Watch == nil || browser.Refresh == nil || loop == nil {
		return nil, nil
	}
	publisher, err := runtime.StartPublication(ctx, sessionturn.PublicationRequest{
		BaseDefinitions:    options.BaseDefinitions,
		InitialDefinitions: options.ToolDefinitions,
		Browser:            browser,
		Publish: func(ctx context.Context, definitions []messages.ToolDefinition) error {
			return loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionUpdate, Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Tools: definitions})})
		},
	})
	if err != nil {
		return nil, publicationError(err)
	}
	return publisher, publisher.Errors()
}

func publicationError(err error) <-chan error {
	if err == nil {
		return nil
	}
	result := make(chan error, 1)
	result <- err
	close(result)
	return result
}
