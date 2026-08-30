package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// RunSessionWithInstructions resolves the ask-path system-prompt contract and
// applies the result to the realtime session before the first user turn.
//
// Session instructions intentionally disable dynamic system information. A
// realtime session's instructions are the configured workspace or explicit
// prompt content, while the provider/session runtime continues to own its
// model configuration.
func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

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

	plan, err := planSessionWithResolvedInstructions(opts, instructions)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration preserves the
// session command's audio, explicit text-seed, and duration behavior while
// carrying the selected or default workspace instructions into provider
// construction.
func RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioPath, maxDuration, seed)
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	instructions, err := resolveSessionInstructions(opts, systemPrompt)
	if err != nil {
		return err
	}
	plan, err := planSessionWithResolvedInstructions(opts, instructions)
	if err != nil {
		return err
	}

	if audioPath == "" {
		if seed.Present {
			wirePrompt := nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
			output := &sessionTextOutput{writer: out}
			if maxDuration == 0 {
				if plan.inferencer != nil {
					plan.inferencer = &sessionTextSeedInferencer{
						inner:      plan.inferencer,
						wirePrompt: wirePrompt,
						value:      seed.Value,
					}
				}
				return errors.Join(plan.run(ctx, output), output.errorValue())
			}
			durationCtx, err := prepareSessionDurationArtifacts(ctx)
			if err != nil {
				return err
			}
			admission := newSessionDurationAdmission()
			// The seed substitution wrapper must sit INSIDE the admission
			// boundary: the duration runner connects through
			// admittedInferencer, so any wrapper composed outside it never
			// observes the session and the sentinel prompt would leak onto
			// the live wire.
			var admittedInner messages.SessionInferencer
			if plan.inferencer != nil {
				admittedInner = &sessionTextSeedInferencer{
					inner:      plan.inferencer,
					wirePrompt: wirePrompt,
					value:      seed.Value,
				}
			}
			if admittedInner != nil {
				plan.inferencer = &sessionDurationAdmissionInferencer{
					inner:     admittedInner,
					admission: admission,
					closeDone: make(chan struct{}),
				}
			}
			var admittedInferencer *sessionDurationAdmissionInferencer
			if admitted, ok := plan.inferencer.(*sessionDurationAdmissionInferencer); ok {
				admittedInferencer = admitted
			}
			runErr = runSessionDurationPlanWithAdmission(durationCtx, output, plan, maxDuration, realSessionDurationClock{}, admittedInferencer)
			return errors.Join(runErr, output.errorValue())
		}
		if maxDuration == 0 {
			return plan.run(ctx, out)
		}
		durationCtx, err := prepareSessionDurationArtifacts(ctx)
		if err != nil {
			return err
		}
		return runSessionDurationPlan(durationCtx, out, plan, maxDuration, realSessionDurationClock{})
	}

	if seed.Present {
		plan.loop.Prompt = nextSessionTextWirePrompt()
	}
	sink, err := newSessionAudioSink(audioPath, out)
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", audioPath, err)
	}
	audioOut := &sessionAudioOutput{sink: sink, runtime: plan.runtime}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioPath, closeErr))
		}
	}()

	sessionOut := out
	if audioPath == "-" {
		sessionOut = io.Discard
	}
	if plan.inferencer != nil {
		wirePrompt := ""
		if seed.Present {
			wirePrompt = plan.loop.Prompt
		}
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
		plan.inferencer = wrapped
		if maxDuration == 0 {
			runErr = plan.run(ctx, sessionOut)
		} else {
			durationCtx, durationErr := prepareSessionDurationArtifacts(ctx)
			if durationErr != nil {
				return durationErr
			}
			runErr = runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
		}
		wrapped.wait()
		if outputErr := wrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioPath, outputErr))
		}
		return runErr
	}
	if maxDuration == 0 {
		return plan.run(ctx, sessionOut)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
}

func planSessionWithResolvedInstructions(opts SessionRunOptions, instructions string) (sessionRuntimePlan, error) {
	// This is the single service-owned boundary between prompt resolution and
	// provider construction. The tool definitions in opts are the same snapshot
	// that the runtime planner passes to the provider, so the grounding contract
	// cannot drift from the advertised tool surface.
	opts.ToolDefinitions = messages.CanonicalToolDefinitions(opts.ToolDefinitions)
	instructions = composeSessionInstructions(opts, instructions)
	planFactory := defaultSessionRuntimeFactory
	useInitialProviderInstructions := instructions != "" && opts.SessionInferencer == nil
	if useInitialProviderInstructions {
		planFactory = sessionRuntimeFactoryWithInstructions(instructions)
	}
	plan, err := planSessionRuntimeWithFactory(opts, planFactory)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if instructions != "" && plan.inferencer != nil && !useInitialProviderInstructions {
		plan.inferencer = newSessionInstructionsInferencer(plan.inferencer, instructions, opts.ToolDefinitions)
	}
	return plan, nil
}

// sessionRuntimeFactoryWithInstructions carries resolved instructions into
// the provider's initial SessionConfig. The generic session adapter remains
// the fallback for injected session seams, while live providers receive the
// same value before ConnectSession can send their initial wire update.
func sessionRuntimeFactoryWithInstructions(instructions string) sessionRuntimeFactory {
	factory := defaultSessionRuntimeFactory
	factory.newBareLiveSessionInferencer = func(opts SessionRunOptions) (messages.SessionInferencer, string, error) {
		return NewLiveSessionInferencer(opts, instructions)
	}
	factory.newGrokSessionInferencer = func(sessionCfg config.GrokConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
		return buildGrokSessionInferencerWithInstructions(sessionCfg, dialer, instructions)
	}
	factory.newOpenAISessionInf = func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithInstructionsAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, inputAudioTranscription)
	}
	factory.newGrokSessionWithTools = func(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
		return buildGrokSessionInferencerWithInstructionsAndTools(sessionCfg, dialer, instructions, toolDefinitions)
	}
	factory.newOpenAISessionWithTools = func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, toolDefinitions, inputAudioTranscription)
	}
	factory.newOpenAIScheduledSessionWithTools = func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndScheduledAudioAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, toolDefinitions, inputAudioTranscription)
	}
	return factory
}

func buildGrokSessionInferencerWithInstructions(sessionCfg config.GrokConfig, dialer transport.Dialer, instructions string) (messages.SessionInferencer, error) {
	return buildGrokSessionInferencerWithInstructionsAndTools(sessionCfg, dialer, instructions, nil)
}

func buildGrokSessionInferencerWithInstructionsAndTools(sessionCfg config.GrokConfig, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderGrok)
	}
	providerOpts := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey), grok.WithWebSocketDialer(dialer)}
	if strings.TrimSpace(sessionCfg.BaseURL) != "" {
		providerOpts = append(providerOpts, grok.WithBaseURL(sessionCfg.BaseURL))
	}
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create Grok session gateway: %w", err)
	}
	inferenceOpts := []inference.SessionOption{
		inference.WithSessionModel(sessionCfg.Model),
		inference.WithSessionInstructions(instructions),
	}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndTools(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, toolDefinitions, models.InputAudioTranscriptionConfig{})
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, nil, inputAudioTranscription)
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg, voice, dialer, instructions, toolDefinitions, inputAudioTranscription)
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndScheduledAudioAndInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg, voice, dialer, instructions, toolDefinitions, inputAudioTranscription, oaiprovider.WithClientOwnedAudioTurnBoundaries())
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig, extra ...oaiprovider.Option) (messages.SessionInferencer, error) {
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
		oaiprovider.WithWebSocketDialer(dialer),
	}
	providerOpts = append(providerOpts, extra...)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI realtime session gateway: %w", err)
	}
	inferenceOpts := []inference.SessionOption{
		inference.WithSessionModel(sessionCfg.Model),
		inference.WithSessionInstructions(instructions),
		inference.WithSessionInputAudioTranscription(inputAudioTranscription),
	}
	if voice != "" {
		inferenceOpts = append(inferenceOpts, inference.WithSessionVoice(voice))
	}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
}

// resolveSessionInstructions delegates prompt selection, AGENTS.md creation,
// and path-or-literal precedence to the existing ask-path Executor contract.
func resolveSessionInstructions(opts SessionRunOptions, systemPrompt string) (string, error) {
	toolDefinitions := messages.CanonicalToolDefinitions(opts.ToolDefinitions)
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
	instructions, err := executor.LoadSystemPrompt(cfg, storage.WorkspaceDir(), toolDefinitions)
	if err != nil {
		return "", fmt.Errorf("resolve session instructions: %w", err)
	}
	return instructions, nil
}

const sessionToolGroundingPolicy = `Tool-grounding requirements:
- For requests about actual files, commands, web resources, images, or other machine state, use the relevant advertised tool before making factual claims about what exists, happened, or was observed. Use only tools advertised in this session; if no relevant advertised tool exists, say that you cannot inspect the real state instead of guessing.
- Do not claim that an action ran or that state was observed without its corresponding tool result. Wait for the result and base the response on its returned facts.
- Report tool errors, missing resources, permission denials, and non-zero command exits as failures. Never invent output, turn a failure into apparent success, or present memory or assumptions as observations.`

const sessionConnectedUnselectedBrowserGrounding = `WebMCP browser selection:
- A browser endpoint is connected, but no page is selected.
- Before any page work, call webmcp_list_tabs.
- If multiple eligible tabs are returned, ask the customer which page to use; do not guess.
- After the customer chooses, call webmcp_select_tab with the exact browser_id and target_id returned by webmcp_list_tabs.
- Until exact selection succeeds, do not invoke page tools, say that browser access is unavailable, or suggest uploads, links, manual page descriptions, shell commands, or other workarounds.`

const sessionWebMCPAmbiguityPolicy = `WebMCP ambiguity recovery:
- A failed WebMCP result with error.code "ambiguous_browser" or error.code "ambiguous_tab" and details.recovery.action "ask_customer" is a pending customer choice, not permission to retry the same call.
- Ask exactly one concise spoken/text question before any additional browser tool call. For ambiguous_tab, name every candidate in details.candidate_choices with its safe title and origin; if a label is unavailable, name its exact candidate ID. For ambiguous_browser, name every exact ID in details.candidate_browser_ids. Do not claim that a page was selected.
- Until the customer answers, do not repeat webmcp_get_context, webmcp_list_tabs, or webmcp_select_tab, and do not invoke a page tool. Never retry with an omitted, unchanged, title-based, URL-based, or inferred selector, and never request multiple continuations for the same ambiguity result.
- After the customer answers, map the answer to one advertised exact candidate ID. For a page selection, pass the exact browser_id and target_id from that candidate once; for a browser selection, pass its exact browser_id once. Do not substitute by list order or act on an unchosen page.`

// composeSessionInstructions preserves the selected customer instructions and
// adds the provider-neutral grounding contract exactly once for tool-enabled
// sessions. Browser-enabled sessions additionally receive the ambiguity
// recovery contract, which makes the retryable WebMCP result a customer-input
// boundary rather than an invitation to repeat a selector-free call. The
// no-tools path remains byte-for-byte unchanged, and callers that already
// supplied either policy do not receive a duplicate copy.
func composeSessionInstructions(opts SessionRunOptions, instructions string) string {
	if len(opts.ToolDefinitions) == 0 {
		return instructions
	}
	blocks := []string{instructions}
	if opts.BrowserCapabilityState == webmcp.BrowserCapabilityConnectedUnselected && !strings.Contains(instructions, sessionConnectedUnselectedBrowserGrounding) {
		blocks = append(blocks, sessionConnectedUnselectedBrowserGrounding)
	}
	if !strings.Contains(instructions, sessionToolGroundingPolicy) {
		blocks = append(blocks, sessionToolGroundingPolicy)
	}
	if opts.BrowserToolsEnabled && !strings.Contains(instructions, sessionWebMCPAmbiguityPolicy) {
		blocks = append(blocks, sessionWebMCPAmbiguityPolicy)
	}
	filtered := blocks[:0]
	for _, block := range blocks {
		if block != "" {
			filtered = append(filtered, block)
		}
	}
	return strings.Join(filtered, "\n\n")
}

// sessionInstructionsInferencer decorates caller-owned session seams without
// changing their provider construction. The provider-aware runtime factory
// above handles the live provider path; injected sessions receive a generic
// session update after the provider announces that the session is open.
type sessionInstructionsInferencer struct {
	inner        messages.SessionInferencer
	instructions string
	tools        []messages.ToolDefinition
}

var _ messages.SessionInferencer = (*sessionInstructionsInferencer)(nil)

func newSessionInstructionsInferencer(inner messages.SessionInferencer, instructions string, toolDefinitions []messages.ToolDefinition) messages.SessionInferencer {
	return &sessionInstructionsInferencer{
		inner:        inner,
		instructions: instructions,
		tools:        cloneSessionToolDefinitions(toolDefinitions),
	}
}

func cloneSessionToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func (i *sessionInstructionsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newSessionInstructionsSession(inner, ctx, i.instructions, i.tools), nil
}

type sessionInstructionsSession struct {
	inner         messages.Session
	instructions  string
	tools         []messages.ToolDefinition
	receive       *messages.TypedBuffer[messages.StreamMessage]
	ctx           context.Context
	cancel        context.CancelFunc
	configureOnce sync.Once
	done          chan struct{}
	doneOnce      sync.Once
}

var _ messages.Session = (*sessionInstructionsSession)(nil)
var _ messages.SessionSendOutcomeSender = (*sessionInstructionsSession)(nil)

func newSessionInstructionsSession(inner messages.Session, parent context.Context, instructions string, toolDefinitions []messages.ToolDefinition) messages.Session {
	ctx, cancel := context.WithCancel(parent)
	session := &sessionInstructionsSession{
		inner:        inner,
		instructions: instructions,
		tools:        cloneSessionToolDefinitions(toolDefinitions),
		receive:      messages.NewTypedBuffer[messages.StreamMessage](inner.Receive().Cap()),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go session.relay()
	return session
}

func (s *sessionInstructionsSession) relay() {
	defer s.markDone()
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
					Tools:        s.tools,
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

// RequestResponse forwards the optional explicit response capability without
// changing the instruction-update lifecycle or replay behavior.
func (s *sessionInstructionsSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *sessionInstructionsSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

// SendMessage forwards the optional complete-message capability of the
// wrapped provider session. Instruction decoration must not hide the rich
// message path used to deliver a tool result on the next model turn.
func (s *sessionInstructionsSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards deferred complete messages for callers
// that batch more than one tool result before requesting the next response.
func (s *sessionInstructionsSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionInstructionsSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.inner)
	return complete
}

func (s *sessionInstructionsSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.inner)
	return withoutResponse
}

func (s *sessionInstructionsSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *sessionInstructionsSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionInstructionsSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionInstructionsSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}

func (s *sessionInstructionsSession) TerminalError() error {
	return terminalSessionError(s.inner)
}

func (s *sessionInstructionsSession) Close() error {
	s.cancel()
	err := s.inner.Close()
	s.markDone()
	return err
}

func (s *sessionInstructionsSession) markDone() {
	s.doneOnce.Do(func() { close(s.done) })
}
