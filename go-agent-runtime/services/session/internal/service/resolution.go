package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	agent "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/execution"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func (s *Service) resolve(ctx context.Context, request session.Request) (session.Resolution, error) {
	ctx = normalizeContext(ctx)
	if s != nil && s.resolver != nil {
		resolution, err := s.resolver.Resolve(ctx, request)
		if err != nil {
			return session.Resolution{}, err
		}
		if resolution.Store == nil {
			resolution.Store = s.store
		}
		if resolution.ProviderService == nil {
			resolution.ProviderService = s.providerService
		}
		if resolution.TraceStore == nil {
			resolution.TraceStore = s.traceStore
		}
		return ensureResolutionDefaults(request, resolution), nil
	}
	resolution := session.Resolution{
		Provider:        session.ProviderConfig{Provider: request.Provider, Model: request.Model, APIKey: request.APIKey, BaseURL: request.BaseURL},
		SystemPrompt:    request.SystemPrompt,
		Store:           s.store,
		TraceStore:      s.traceStore,
		ProviderService: s.providerService,
	}
	return ensureResolutionDefaults(request, resolution), nil
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func ensureResolutionDefaults(request session.Request, resolution session.Resolution) session.Resolution {
	if resolution.Provider.Provider == "" {
		resolution.Provider.Provider = request.Provider
	}
	if resolution.Provider.Model == "" {
		resolution.Provider.Model = request.Model
	}
	if resolution.Provider.APIKey == "" {
		resolution.Provider.APIKey = request.APIKey
	}
	if resolution.Provider.BaseURL == "" {
		resolution.Provider.BaseURL = request.BaseURL
	}
	if resolution.SystemPrompt == "" && !resolution.SystemPromptResolved {
		resolution.SystemPrompt = request.SystemPrompt
	}
	if resolution.Store == nil {
		resolution.Store = newMemoryStore()
	}
	if resolution.TraceStore == nil {
		if traceStore, ok := resolution.Store.(session.TraceStore); ok {
			resolution.TraceStore = traceStore
		} else {
			resolution.TraceStore = newMemoryStore()
		}
	}
	return resolution
}

func toExecutionConfig(request session.Request, resolution session.Resolution) agent.Config {
	provider := resolution.Provider
	return agent.Config{
		SystemPrompt: resolution.SystemPrompt, NoSystemInformation: true,
		SessionID: request.SessionID, ContinueLastSession: request.ContinueLastSession,
		InitialHistory: append([]messages.Message(nil), request.InitialHistory...),
		Model:          provider.Model, Provider: provider.Provider, APIKey: provider.APIKey, BaseURL: provider.BaseURL,
		OutputModality: request.OutputModality, ModelConfig: request.ModelConfig,
		OutputReasoningTokens: request.OutputReasoningTokens,
		RecordCapturePath:     request.RecordCapturePath, ReplayCapturePath: request.ReplayCapturePath,
		SystemPromptSuffix: request.SystemPromptSuffix, MaxContinuationDepth: request.MaxContinuationDepth,
	}
}

func toRuntimeResolution(ctx context.Context, resolution session.Resolution) agent.RuntimeResolution {
	provider := resolution.Provider
	executionProvider := agent.ProviderConfig{
		Provider: provider.Provider,
		Model:    provider.Model,
		APIKey:   provider.APIKey,
		BaseURL:  provider.BaseURL,
	}
	if provider.Fal != nil {
		executionProvider.Fal = &agent.FalProviderConfig{
			Model: provider.Fal.Model, APIKey: provider.Fal.APIKey, BaseURL: provider.Fal.BaseURL,
		}
	}
	models := make([]agent.ModelInfo, 0, len(resolution.Models))
	for _, model := range resolution.Models {
		models = append(models, agent.ModelInfo{
			Name: model.Name, Aliases: append([]string(nil), model.Aliases...), Providers: append([]string(nil), model.Providers...),
			InputModalities: append([]string(nil), model.InputModalities...), OutputModalities: append([]string(nil), model.OutputModalities...),
			SupportedInputMimeTypes: append([]string(nil), model.SupportedInputMimeTypes...),
		})
	}
	return agent.RuntimeResolution{
		Resolved:        true,
		Provider:        executionProvider,
		ModelCatalog:    agent.ModelCatalog{Models: models},
		ModelPolicy:     agent.ModelPolicy{ContinuationNudgeEnabled: resolution.ContinuationNudgeEnabled, ContinuationNudgeMessage: resolution.ContinuationNudgeMessage, RepetitionPenalty: resolution.RepetitionPenalty},
		ProviderService: resolution.ProviderService,
		Storage:         newStorageAdapter(ctx, resolution.Store, resolution.TraceStore, resolution.WorkspaceDir),
		WorkspaceDir:    resolution.WorkspaceDir,
		AllowPaths:      append([]string(nil), resolution.AllowPaths...),
		SkillRoots:      append([]tools.SkillRoot(nil), resolution.SkillRoots...),
		Logger:          resolution.Logger,
		PromptResolved:  true,
	}
}
