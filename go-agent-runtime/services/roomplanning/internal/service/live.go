package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type livePlanState struct {
	options     roomplanning.Options
	scope       roomplanning.FilesystemScope
	credentials map[string]string
}

func planLive(ctx context.Context, options roomplanning.Options, scope roomplanning.FilesystemScope) (roomplanning.PlanResult, error) {
	state, err := newLivePlanState(options, scope)
	if err != nil {
		return roomplanning.PlanResult{}, err
	}
	plans := make([]*roomplanning.ParticipantPlan, 0, len(options.Manifest.Participants))
	for _, participant := range options.Manifest.Participants {
		if err := ctx.Err(); err != nil {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(err, closeCapabilities(plans))
		}
		builder := liveParticipantBuilder{state: state, participant: participant}
		plan, currentClose, fatalErr := builder.build(ctx)
		if fatalErr != nil {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(fatalErr, closeCapabilities(plans), closeCapability(currentClose))
		}
		plans = append(plans, plan)
	}
	return roomplanning.PlanResult{Plans: plans}, nil
}

func newLivePlanState(options roomplanning.Options, scope roomplanning.FilesystemScope) (*livePlanState, error) {
	if options.LookupCredential == nil {
		options.LookupCredential = func(string) (string, bool) { return "", false }
	}
	credentials := collectLiveCredentials(options)
	if err := validateInferencers(options); err != nil {
		return nil, err
	}
	if options.SessionFactory == nil && liveNeedsSessionFactory(options) {
		return nil, roomplanning.ErrSessionFactory
	}
	return &livePlanState{options: options, scope: scope, credentials: credentials}, nil
}

func collectLiveCredentials(options roomplanning.Options) map[string]string {
	credentials := make(map[string]string, len(options.Manifest.Participants))
	for _, participant := range options.Manifest.Participants {
		if normalizeKind(participant.Kind) == rooms.ParticipantKindHuman || participant.APIKeyEnv == "" {
			continue
		}
		if value, ok := options.LookupCredential(participant.APIKeyEnv); ok && value != "" {
			credentials[participant.ID] = value
		}
	}
	return credentials
}

func validateInferencers(options roomplanning.Options) error {
	known := make(map[string]struct{}, len(options.Manifest.Participants))
	for _, participant := range options.Manifest.Participants {
		known[participant.ID] = struct{}{}
	}
	for id := range options.SessionInferencers {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("room session inferencer provided for unknown participant %q", id)
		}
	}
	return nil
}

func liveNeedsSessionFactory(options roomplanning.Options) bool {
	for _, participant := range options.Manifest.Participants {
		if normalizeKind(participant.Kind) == rooms.ParticipantKindHuman {
			continue
		}
		if _, injected := options.SessionInferencers[participant.ID]; !injected {
			return true
		}
	}
	return false
}

type liveParticipantBuilder struct {
	state       *livePlanState
	participant rooms.Participant
}

func (b liveParticipantBuilder) build(ctx context.Context) (*roomplanning.ParticipantPlan, func() error, error) {
	if normalizeKind(b.participant.Kind) == rooms.ParticipantKindHuman {
		return &roomplanning.ParticipantPlan{Participant: b.participant}, nil, nil
	}
	sessionOptions := b.sessionOptions()
	plan := &roomplanning.ParticipantPlan{Participant: b.participant, Options: sessionOptions}
	static, toolErr, fatal := b.configureTools(&sessionOptions)
	if toolErr != nil {
		if fatal {
			return nil, nil, toolErr
		}
		b.markFailure(plan, sessionOptions, toolErr)
		return plan, nil, nil
	}
	b.configureHostSeams(&sessionOptions)
	currentClose, fatalBrowser, browserErr := b.configureBrowser(ctx, &sessionOptions, static)
	if browserErr != nil {
		if fatalBrowser {
			return nil, currentClose, browserErr
		}
		b.markFailure(plan, sessionOptions, browserErr)
		return plan, nil, nil
	}
	plan.Options = sessionOptions
	if err := b.configureSession(plan, sessionOptions); err != nil {
		b.markFailure(plan, sessionOptions, err)
	}
	if plan.StartupErr == nil {
		if err := b.configureSampleRate(plan, sessionOptions); err != nil {
			b.markFailure(plan, sessionOptions, err)
		}
	}
	return plan, nil, nil
}

func (b liveParticipantBuilder) sessionOptions() roomplanning.SessionOptions {
	return roomplanning.SessionOptions{
		Provider: b.participant.Provider, Model: b.participant.Model, ModelProvided: true,
		BaseURL: b.state.options.BaseURL, ConfigDir: b.state.options.ConfigDir,
		WorkDir: b.state.scope.PrimaryRoot, AllowPaths: append([]string(nil), b.state.scope.AdditionalRoots...), Filesystem: b.state.scope,
		Prompt: b.participant.OpeningPrompt, Voice: b.participant.Voice,
		WebSocketDialer: b.state.options.WebSocketDialer, WaitForClose: true,
	}
}

func (b liveParticipantBuilder) configureTools(sessionOptions *roomplanning.SessionOptions) (roomplanning.ToolCapabilities, error, bool) {
	if len(b.participant.Tools) == 0 {
		return roomplanning.ToolCapabilities{}, nil, false
	}
	if b.state.options.ToolFactory == nil {
		return roomplanning.ToolCapabilities{}, roomplanning.ErrParticipantTools, false
	}
	capabilities, err := b.state.options.ToolFactory(b.participant)
	if err != nil {
		return roomplanning.ToolCapabilities{}, fmt.Errorf("configure participant tools: %w", err), false
	}
	if err := validateTools(b.participant, capabilities); err != nil {
		if errors.Is(err, roomplanning.ErrParticipantToolMatch) {
			return roomplanning.ToolCapabilities{}, fmt.Errorf("room participant %q capability contract: %w", b.participant.ID, err), true
		}
		return roomplanning.ToolCapabilities{}, err, false
	}
	sessionOptions.ToolExecutor = capabilities.Executor
	sessionOptions.ToolDefinitions = cloneDefinitions(capabilities.Definitions)
	return capabilities, nil, false
}

func (b liveParticipantBuilder) configureHostSeams(sessionOptions *roomplanning.SessionOptions) {
	if b.state.options.WebSocketDialerFactory != nil {
		sessionOptions.WebSocketDialer = b.state.options.WebSocketDialerFactory(b.participant)
	}
	if b.state.options.ResolveCapturePath != nil {
		if capture, ok := b.state.options.ResolveCapturePath(b.participant.ID); ok {
			sessionOptions.RecordSessionCapturePath = capture
		}
	}
}

func (b liveParticipantBuilder) configureBrowser(ctx context.Context, sessionOptions *roomplanning.SessionOptions, static roomplanning.ToolCapabilities) (func() error, bool, error) {
	if b.participant.BrowserTools == nil {
		return nil, false, nil
	}
	if b.state.options.BrowserFactory == nil {
		return nil, false, roomplanning.ErrParticipantBrowser
	}
	browser, err := b.state.options.BrowserFactory(b.participant, static)
	if err != nil {
		return nil, false, fmt.Errorf("configure browser tools: %w", err)
	}
	sessionOptions.CapabilityClose = browser.Close
	if err := validateBrowser(browser); err != nil {
		return browser.Close, true, fmt.Errorf("room participant %q browser capability contract: %w", b.participant.ID, err)
	}
	if browser.Initialize != nil {
		if err := browser.Initialize(ctx); err != nil {
			return nil, false, fmt.Errorf("initialize browser tools: %w", err)
		}
	}
	if browser.RefreshToolDefinitions != nil {
		refreshed, refreshErr := browser.RefreshToolDefinitions(ctx)
		if refreshErr == nil {
			browser.Definitions = cloneDefinitions(refreshed)
		} else if ctx.Err() != nil {
			return nil, false, fmt.Errorf("refresh browser tools: %w", refreshErr)
		}
	}
	sessionOptions.ToolExecutor = browser.Executor
	sessionOptions.ToolDefinitions = cloneDefinitions(browser.Definitions)
	sessionOptions.ToolDefinitionBase = cloneDefinitions(browser.ToolDefinitionBase)
	sessionOptions.RefreshTools = browser.RefreshToolDefinitions
	sessionOptions.BrowserWatch = browser.BrowserWatch
	sessionOptions.BrowserEventWatch = browser.BrowserEventWatch
	sessionOptions.BrowserTools = true
	return nil, false, nil
}

func (b liveParticipantBuilder) configureSession(plan *roomplanning.ParticipantPlan, options roomplanning.SessionOptions) error {
	if inferencer, injected := b.state.options.SessionInferencers[b.participant.ID]; injected {
		if isNil(inferencer) {
			return errors.New("injected session inferencer is nil")
		}
		plan.Inferencer = inferencer
		return nil
	}
	inferencer, err := b.state.options.SessionFactory(roomplanning.LiveSessionRequest{Participant: b.participant, Options: options, Credential: b.state.credentials[b.participant.ID]})
	if err != nil {
		return fmt.Errorf("construct live session: %w", err)
	}
	if isNil(inferencer) {
		return errors.New("session factory returned a nil inferencer")
	}
	plan.Inferencer = inferencer
	return nil
}

func (b liveParticipantBuilder) configureSampleRate(plan *roomplanning.ParticipantPlan, options roomplanning.SessionOptions) error {
	if b.state.options.ResolveSampleRate == nil || (b.state.options.SessionInferencers[b.participant.ID] != nil && !b.state.options.ResolveSampleRateForInjected) {
		return nil
	}
	rate, err := b.state.options.ResolveSampleRate(options, plan.Inferencer)
	if err != nil {
		return err
	}
	plan.InputAudioSampleRate = rate
	return nil
}

func (b liveParticipantBuilder) markFailure(plan *roomplanning.ParticipantPlan, options roomplanning.SessionOptions, cause error) {
	if cause == nil {
		return
	}
	plan.Options = options
	plan.StartupErr = participantError(b.state.options, b.participant.ID, cause)
}
