package agentruntime

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
)

func sessionTurnPublicationInputs(sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) (sessionturn.Runtime, sessionturn.BrowserRequest, error) {
	if opts.turnRuntime != nil {
		return opts.turnRuntime, opts.turnBrowser, nil
	}
	if opts.BrowserWatch == nil || opts.RefreshToolDefinitions == nil {
		return nil, sessionturn.BrowserRequest{}, nil
	}
	runtime, err := sessionturnwire.NewDefaultService().Prepare(context.Background(), sessionturn.Request{
		SessionInferencer:     sessionInferencer,
		ToolExecutor:          opts.ToolExecutor,
		ToolDefinitions:       opts.ToolDefinitions,
		InteractiveToolPolicy: opts.InteractiveToolPolicy,
		ToolExecutionTimeout:  opts.ToolExecutionTimeout,
	})
	if err != nil {
		return nil, sessionturn.BrowserRequest{}, fmt.Errorf("prepare session-turn publication runtime: %w", err)
	}
	return runtime, sessionTurnBrowserRequest(opts.BrowserWatch, opts.RefreshToolDefinitions), nil
}

func startSessionTurnPublication(ctx context.Context, loop *agentloop.AgentLoop, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) (sessionturn.Publication, <-chan error) {
	runtime, browser, err := sessionTurnPublicationInputs(sessionInferencer, opts)
	if err != nil {
		return nil, sessionTurnPublicationError(err)
	}
	if runtime == nil || browser.Watch == nil {
		return nil, nil
	}
	publisher, err := runtime.StartPublication(ctx, sessionturn.PublicationRequest{
		BaseDefinitions: opts.ToolDefinitionBase, InitialDefinitions: opts.ToolDefinitions, Browser: browser,
		Publish: func(ctx context.Context, definitions []messages.ToolDefinition) error {
			return loop.SendSessionEvent(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeSessionUpdate,
				Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Tools: definitions}),
			})
		},
	})
	if err != nil {
		return nil, sessionTurnPublicationError(err)
	}
	return publisher, publisher.Errors()
}

func sessionTurnPublicationError(err error) <-chan error {
	if err == nil {
		return nil
	}
	errors := make(chan error, 1)
	errors <- err
	close(errors)
	return errors
}

func stopSessionTurnPublication(publisher sessionturn.Publication) {
	if publisher != nil {
		publisher.Stop()
	}
}

func markSessionTurnPublicationReady(publisher sessionturn.Publication) {
	if publisher != nil {
		publisher.MarkReady()
	}
}
