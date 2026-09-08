package agent

import (
	"context"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	looplogging "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"os"
)

// BuildLoop constructs an agent loop from the given configuration.
func (e *Executor) BuildLoop(ctx context.Context, cfg *Config) (*RunData, error) {
	storage, err := e.getSessionStorage()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("session execution config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	workDir := e.resolvedWorkspace
	allowPaths := append([]string(nil), e.resolvedAllowPaths...)

	inf, capture, err := e.buildInferencer(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Resolve the request-scoped tool capability after the inferencer is ready.
	// Embedded hosts may intentionally supply no tool surface; in that shape the
	// service is not asked to inspect process state or construct defaults.
	toolCapability, err := e.resolveToolCapability(ctx, workDir, allowPaths, inf)
	if err != nil {
		return nil, fmt.Errorf("resolve tools: %w", err)
	}
	sessionStorage := storage

	// Get initial history and session ID
	var initialHistory []messages.Message
	var sessionID string
	if len(cfg.InitialHistory) > 0 && cfg.SessionID != "" {
		// Explicit session override (for chat)
		sessionID = cfg.SessionID
		initialHistory = cfg.InitialHistory
	} else {
		initialHistory, sessionID, err = e.getInitialHistory(cfg, sessionStorage)
		if err != nil {
			return nil, err
		}
	}

	systemPrompt, err := e.resolveSystemPrompt(ctx, cfg)
	if err != nil {
		return nil, err
	}

	loopOpts := e.loopOptions(cfg, inf, toolCapability, systemPrompt, initialHistory)

	loop, err := agentloop.New(loopOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent loop: %w", err)
	}

	return &RunData{
		SessionID:      sessionID,
		Capture:        capture,
		Loop:           loop,
		sessionManager: sessionStorage,
		modelCatalog:   cloneModelCatalog(e.resolvedCatalog),
	}, nil
}

func (e *Executor) buildInferencer(ctx context.Context, cfg *Config) (messages.Inferencer, runtimeproviders.CaptureWriter, error) {
	var inf messages.Inferencer
	var capture runtimeproviders.CaptureWriter
	if e.inferencerOverride != nil {
		return e.inferencerOverride, nil, nil
	}
	if e.resolvedProviderService == nil {
		return nil, nil, fmt.Errorf("session provider is required")
	}
	providerConfig := e.resolvedProvider
	provider, err := e.resolvedProviderService.Build(ctx, runtimeproviders.Config{
		Provider:   providerConfig.Provider,
		Model:      providerConfig.Model,
		APIKey:     providerConfig.APIKey,
		BaseURL:    providerConfig.BaseURL,
		RecordPath: cfg.RecordCapturePath, ReplayPath: cfg.ReplayCapturePath,
		Fal: func() *runtimeproviders.FalConfig {
			if providerConfig.Fal == nil {
				return nil
			}
			return &runtimeproviders.FalConfig{
				Model: providerConfig.Fal.Model, APIKey: providerConfig.Fal.APIKey,
				BaseURL: providerConfig.Fal.BaseURL,
			}
		}(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build provider: %w", err)
	}
	if writer, ok := provider.(runtimeproviders.CaptureWriter); ok {
		capture = writer
	}
	gw, err := gateway.NewGateway(gateway.WithProvider(provider))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create gateway: %w", err)
	}
	infOpts := []inference.Option{}
	if cfg.ModelConfig != "" {
		infOpts = append(infOpts, inference.WithModelConfig(cfg.ModelConfig))
	}
	if providerConfig.Provider == "fal" && providerConfig.Fal != nil {
		infOpts = append(infOpts, inference.WithModel(providerConfig.Fal.Model))
	}
	inf = inference.NewGatewayInferencer(gw, infOpts...)
	return inf, capture, nil
}

func (e *Executor) resolveSystemPrompt(ctx context.Context, cfg *Config) (string, error) {
	systemPrompt := cfg.SystemPrompt
	if systemPrompt != "" && len(e.resolvedSkillRoots) > 0 && e.toolService != nil {
		summary, summaryErr := e.toolService.BuildSkillsSummary(ctx, runtimeTools.SkillSummaryRequest{
			SkillRoots: append([]runtimeTools.SkillRoot(nil), e.resolvedSkillRoots...),
		})
		if summaryErr != nil {
			return "", fmt.Errorf("build skills summary: %w", summaryErr)
		}
		if summary != "" {
			systemPrompt += "\n\n---\n\n" + summary
		}
	}

	if cfg.SystemPromptSuffix != "" {
		if systemPrompt != "" {
			systemPrompt += "\n\n"
		}
		systemPrompt += cfg.SystemPromptSuffix
	}
	return systemPrompt, nil
}

func (e *Executor) loopOptions(cfg *Config, inf messages.Inferencer, capability runtimeTools.Capability, systemPrompt string, initialHistory []messages.Message) []agentloop.Option {
	loopLogger := e.logger
	if loopLogger == nil {
		loopLogger = looplogging.DummyLogger()
	}
	loopOpts := []agentloop.Option{
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(capability.Executor),
		agentloop.WithTools(capability.Definitions),
		agentloop.WithLogger(loopLogger),
	}
	if systemPrompt != "" {
		loopOpts = append(loopOpts, agentloop.WithSystemPrompt(systemPrompt))
	}
	if len(initialHistory) > 0 {
		loopOpts = append(loopOpts, agentloop.WithInitialHistory(initialHistory))
	}

	// Apply inference defaults from model config (e.g. repetition penalty).
	defaults := buildInferenceDefaultsForPenalty(e.resolvedModelPolicy.RepetitionPenalty)
	if defaults != nil {
		loopOpts = append(loopOpts, agentloop.WithInferenceDefaults(*defaults))
	}

	// Wire session replay into the agent loop if a session capture file exists.
	// Replay takes priority over record (same as HTTP capture).
	if cfg.ReplayCapturePath != "" {
		sessionCapturePath := cfg.ReplayCapturePath + ".session.json"
		if _, statErr := os.Stat(sessionCapturePath); statErr == nil {
			replayInf := testing.NewReplaySessionInferencer(sessionCapturePath)
			loopOpts = append(loopOpts, agentloop.WithSessionInferencer(replayInf))
		}
	}

	return loopOpts
}
