package services

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

// RunSessionWithInstructions resolves the ask-path system-prompt contract and
// applies the result to the realtime session before the first user turn.
//
// Session instructions intentionally disable dynamic system information. A
// realtime session's instructions are the configured workspace or explicit
// prompt content, while the provider/session runtime continues to own its
// model configuration.
func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) error {
	// A pure replay has no provider session to configure. Preserve its captured
	// outbound sequence; injected replay sessions remain configurable for tests
	// and caller-owned session seams.
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSession(ctx, out, opts)
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}

	instructions, err := resolveSessionInstructions(opts, systemPrompt)
	if err != nil {
		return err
	}

	planFactory := defaultSessionRuntimeFactory
	useInitialProviderInstructions := instructions != "" && opts.SessionInferencer == nil
	if useInitialProviderInstructions {
		planFactory = sessionRuntimeFactoryWithInstructions(instructions)
	}
	plan, err := planSessionRuntimeWithFactory(opts, planFactory)
	if err != nil {
		return err
	}
	if instructions != "" && plan.inferencer != nil && !useInitialProviderInstructions {
		plan.inferencer = newSessionInstructionsInferencer(plan.inferencer, instructions)
	}
	return plan.run(ctx, out)
}

// sessionRuntimeFactoryWithInstructions carries resolved instructions into
// the provider's initial SessionConfig. The generic session adapter remains
// the fallback for injected session seams, while live providers receive the
// same value before ConnectSession can send their initial wire update.
func sessionRuntimeFactoryWithInstructions(instructions string) sessionRuntimeFactory {
	factory := defaultSessionRuntimeFactory
	factory.newGrokSessionInferencer = func(sessionCfg config.GrokConfig, dialer grok.WebSocketDialer) (messages.SessionInferencer, error) {
		return buildGrokSessionInferencerWithInstructions(sessionCfg, dialer, instructions)
	}
	factory.newOpenAISessionInf = func(sessionCfg config.OpenAIConfig, dialer grok.WebSocketDialer) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithInstructions(sessionCfg, dialer, instructions)
	}
	return factory
}

func buildGrokSessionInferencerWithInstructions(sessionCfg config.GrokConfig, dialer grok.WebSocketDialer, instructions string) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderGrok)
	}
	providerOpts := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey), grok.WithWebSocketDialer(dialer)}
	if sessionCfg.BaseURL != "" {
		providerOpts = append(providerOpts, grok.WithBaseURL(sessionCfg.BaseURL))
	}
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create Grok session gateway: %w", err)
	}
	return inference.NewSessionGatewayInferencer(
		sessionGateway,
		inference.WithSessionModel(sessionCfg.Model),
		inference.WithSessionInstructions(instructions),
	), nil
}

func buildOpenAIRealtimeSessionInferencerWithInstructions(sessionCfg config.OpenAIConfig, dialer grok.WebSocketDialer, instructions string) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderOpenAI)
	}
	if !isOpenAIRealtimeModel(sessionCfg.Model) {
		return nil, unsupportedOpenAIRealtimeModelError(sessionCfg.Model)
	}
	providerOpts := []oaiprovider.Option{
		oaiprovider.WithAPIKey(sessionCfg.APIKey),
		oaiprovider.WithModel(sessionCfg.Model),
		oaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(sessionCfg)),
		oaiprovider.WithWebSocketDialer(newOpenAIWebSocketDialerAdapter(dialer)),
	}
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI realtime session gateway: %w", err)
	}
	return inference.NewSessionGatewayInferencer(
		sessionGateway,
		inference.WithSessionModel(sessionCfg.Model),
		inference.WithSessionInstructions(instructions),
	), nil
}

// resolveSessionInstructions delegates prompt selection, AGENTS.md creation,
// and path-or-literal precedence to the existing ask-path Executor contract.
func resolveSessionInstructions(opts SessionRunOptions, systemPrompt string) (string, error) {
	cfg := &agent.Config{
		SystemPrompt:        systemPrompt,
		NoSystemInformation: true,
		ConfigDir:           opts.ConfigDir,
	}
	executor := agent.NewExecutor(nil, nil, nil, true)
	storage, err := executor.GetSessionStorage(cfg)
	if err != nil {
		return "", fmt.Errorf("resolve session instructions: %w", err)
	}
	instructions, err := executor.LoadSystemPrompt(cfg, storage.WorkspaceDir(), nil)
	if err != nil {
		return "", fmt.Errorf("resolve session instructions: %w", err)
	}
	return instructions, nil
}

// sessionInstructionsInferencer decorates caller-owned session seams without
// changing their provider construction. The provider-aware runtime factory
// above handles the live provider path; injected sessions receive a generic
// session update after the provider announces that the session is open.
type sessionInstructionsInferencer struct {
	inner        messages.SessionInferencer
	instructions string
}

var _ messages.SessionInferencer = (*sessionInstructionsInferencer)(nil)

func newSessionInstructionsInferencer(inner messages.SessionInferencer, instructions string) messages.SessionInferencer {
	return &sessionInstructionsInferencer{inner: inner, instructions: instructions}
}

func (i *sessionInstructionsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newSessionInstructionsSession(inner, ctx, i.instructions), nil
}

type sessionInstructionsSession struct {
	inner         messages.Session
	instructions  string
	receive       *messages.TypedBuffer[messages.StreamMessage]
	ctx           context.Context
	cancel        context.CancelFunc
	configureOnce sync.Once
}

var _ messages.Session = (*sessionInstructionsSession)(nil)
var _ messages.SessionSendOutcomeSender = (*sessionInstructionsSession)(nil)

func newSessionInstructionsSession(inner messages.Session, parent context.Context, instructions string) messages.Session {
	ctx, cancel := context.WithCancel(parent)
	session := &sessionInstructionsSession{
		inner:        inner,
		instructions: instructions,
		receive:      messages.NewTypedBuffer[messages.StreamMessage](inner.Receive().Cap()),
		ctx:          ctx,
		cancel:       cancel,
	}
	go session.relay()
	return session
}

func (s *sessionInstructionsSession) relay() {
	innerReceive := s.inner.Receive()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.inner.Done():
			for {
				msg, ok := innerReceive.Read()
				if !ok {
					return
				}
				if !s.forward(msg) {
					return
				}
			}
		case msg := <-innerReceive.Chan():
			if !s.forward(msg) {
				return
			}
		}
	}
}

func (s *sessionInstructionsSession) forward(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionOpen || msg.Type == messages.StreamTypeSessionCreated {
		var configureErr error
		s.configureOnce.Do(func() {
			outcome := messages.SendSessionWithOutcome(s.ctx, s.inner, messages.StreamMessage{
				Type: messages.StreamTypeSessionUpdate,
				Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
					Instructions: s.instructions,
				}),
			})
			if !outcome.OK() {
				configureErr = fmt.Errorf("send session instructions: %s", outcome.Status)
				if outcome.Err != nil {
					configureErr = fmt.Errorf("%w: %v", configureErr, outcome.Err)
				}
			}
		})
		if configureErr != nil {
			s.receive.Write(s.ctx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Value: messages.NewErrorValueWithError(configureErr),
			})
			_ = s.inner.Close()
			return false
		}
	}
	return s.receive.Write(s.ctx, msg)
}

func (s *sessionInstructionsSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

func (s *sessionInstructionsSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *sessionInstructionsSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionInstructionsSession) Done() <-chan struct{} {
	return s.inner.Done()
}

func (s *sessionInstructionsSession) Close() error {
	s.cancel()
	return s.inner.Close()
}
