package livehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// RequestDependencies supplies the host-owned resolution hooks needed to
// turn CLI flags into a neutral LiveRequest. Runtime protocol and provider
// behavior stay behind the injected session and replay services.
type RequestDependencies struct {
	ReplayService       runtimeReplay.Service
	ModelAdmission      runtimeProviders.ModelAdmission
	CredentialReference func(string) string
	InstructionService  runtimeSession.InstructionService
	PageSightToolID     string
	Capabilities        func(*config.Config) (*runtimeSession.LiveCapabilities, error)
	BindImagePreparer   func(messages.ToolExecutor) messages.ToolExecutor
	OpenImages          func([]string) ([]messages.ContentPart, error)
}

// BuildRequest resolves one CLI request at the host boundary. It performs
// only path/config admission and typed value conversion; the runtime owns
// provider session lifecycle, tool execution, and media ordering.
func BuildRequest(ctx context.Context, request serviceSession.Request, replayInspection *runtimeReplay.CaptureInspection, deps RequestDependencies) (runtimeSession.LiveRequest, error) {
	inputs, err := resolveRequestInputs(ctx, request, replayInspection, deps)
	if err != nil {
		return runtimeSession.LiveRequest{}, err
	}
	return assembleLiveRequest(request, inputs), nil
}

type requestInputs struct {
	effective       config.Config
	inspection      *runtimeReplay.CaptureInspection
	provider        string
	model           string
	baseURL         string
	credentialRef   string
	instructions    string
	capabilities    *runtimeSession.LiveCapabilities
	requestPrompt   string
	promptPresent   bool
	openingParts    []messages.ContentPart
	openingResponse runtimeSession.LiveOpeningMessageResponse
	replayPlan      *runtimeSession.LiveReplayPlan
	inputRate       int
	outputRate      int
	replayFinish    bool
}

func resolveRequestInputs(ctx context.Context, request serviceSession.Request, replayInspection *runtimeReplay.CaptureInspection, deps RequestDependencies) (requestInputs, error) {
	loaded, err := requireLiveConfig(request)
	if err != nil {
		return requestInputs{}, err
	}
	inspection, err := admitReplay(ctx, request.ReplayPath, replayInspection, deps.ReplayService)
	if err != nil {
		return requestInputs{}, err
	}
	if inspection != nil {
		request.ReplayPath = inspection.CapturePath
	}
	effective := loaded.ApplyOverrides("", request.Model, request.Provider, request.BaseURL)
	provider, model, apiKey, baseURL, err := ProviderValues(effective, request, inspection)
	if err != nil {
		return requestInputs{}, err
	}
	if deps.ModelAdmission != nil {
		if err := deps.ModelAdmission.ValidateSessionModel(provider, model); err != nil {
			return requestInputs{}, err
		}
	}
	capabilities, err := buildCapabilities(loaded, request, deps)
	if err != nil {
		return requestInputs{}, err
	}
	instructions, err := resolveAndComposeInstructions(ctx, request, capabilities, deps)
	if err != nil {
		return requestInputs{}, err
	}
	requestPrompt, promptPresent := promptValues(request)
	openingParts, openingResponse, err := openingValues(request, deps.OpenImages)
	if err != nil {
		return requestInputs{}, err
	}
	replayPlan, requestPrompt, promptPresent, err := buildReplayPlan(request, inspection, requestPrompt, promptPresent)
	if err != nil {
		return requestInputs{}, err
	}
	inputRate, outputRate := replayRates(replayPlan, request)
	return requestInputs{
		effective: effective, inspection: inspection, provider: provider, model: model, baseURL: baseURL,
		credentialRef: resolveCredentialReference(apiKey, deps.CredentialReference), instructions: instructions,
		capabilities: capabilities, requestPrompt: requestPrompt, promptPresent: promptPresent,
		openingParts: openingParts, openingResponse: openingResponse, replayPlan: replayPlan,
		inputRate: inputRate, outputRate: outputRate,
		replayFinish: inspection != nil && (replayPlan == nil || replayPlan.StopAfterResponse),
	}, nil
}

func resolveAndComposeInstructions(ctx context.Context, request serviceSession.Request, capabilities *runtimeSession.LiveCapabilities, deps RequestDependencies) (string, error) {
	instructionService := deps.InstructionService
	if instructionService == nil {
		instructionService = runtimeSessionWire.NewInstructionService()
	}
	workspaceDir := request.WorkDir
	if workspaceDir == "" && request.LoadedConfig != nil {
		workspaceDir = request.LoadedConfig.FilesystemWorkDir
	}
	var scopeDescription string
	var scopeSet bool
	if workspaceDir != "" {
		policy, err := cliTools.ResolveFilesystemPolicy(workspaceDir, request.AllowPaths...)
		if err != nil {
			return "", fmt.Errorf("resolve live filesystem scope: %w", err)
		}
		workspaceDir = policy.PrimaryRoot()
		scopeDescription = policy.ScopeDescription()
		scopeSet = true
	}
	loader := liveInstructionLoader{workspaceDir: workspaceDir, configDir: request.ConfigDir}
	var instructionLoader runtimeSession.InstructionLoader = loader
	if request.SystemPrompt == "" && workspaceDir == "" {
		// An unconfigured embedded caller has no workspace source to select.
		// Leave the loader nil so the runtime preserves the exact empty prompt
		// identity instead of reading a process-relative AGENTS.md.
		instructionLoader = nil
	}
	resolved, err := instructionService.Resolve(ctx, runtimeSession.InstructionRequest{
		Prompt:                     request.SystemPrompt,
		WorkspaceDir:               workspaceDir,
		FilesystemScopeDescription: scopeDescription,
		FilesystemScopeSet:         scopeSet,
		Loader:                     instructionLoader,
	})
	if err != nil {
		return "", err
	}
	composition := runtimeSession.InstructionComposition{
		Instructions:        resolved.Instructions,
		BrowserToolsEnabled: request.BrowserToolsEnabled,
		PageSightToolID:     deps.PageSightToolID,
	}
	if capabilities != nil {
		composition.ToolDefinitions = append([]messages.ToolDefinition(nil), capabilities.Definitions...)
		composition.BrowserCapabilityState = capabilities.BrowserCapabilityState
	}
	return instructionService.Compose(composition), nil
}

type liveInstructionLoader struct {
	workspaceDir string
	configDir    string
}

func (l liveInstructionLoader) Stat(path string) error { _, err := os.Stat(path); return err }

func (l liveInstructionLoader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (l liveInstructionLoader) SkillsSummary() (string, error) {
	return skills.NewLoader(l.workspaceDir, l.configDir).BuildSummary()
}

func requireLiveConfig(request serviceSession.Request) (*config.Config, error) {
	if request.RecordPath != "" && !strings.EqualFold(filepath.Ext(request.RecordPath), ".json") {
		return nil, fmt.Errorf("--record path %q must end with .json", request.RecordPath)
	}
	if request.LoadedConfig == nil {
		return nil, errors.New("live session configuration is unavailable")
	}
	return request.LoadedConfig, nil
}

func promptValues(request serviceSession.Request) (string, bool) {
	value := request.TextSeed.Value
	if !request.TextSeed.Present {
		value = request.Prompt
	}
	return value, request.TextSeed.Present || request.PromptProvided || value != ""
}

func openingValues(request serviceSession.Request, opener func([]string) ([]messages.ContentPart, error)) ([]messages.ContentPart, runtimeSession.LiveOpeningMessageResponse, error) {
	parts, err := openImages(request.ImagePaths, opener)
	if err != nil {
		return nil, runtimeSession.LiveOpeningMessageRespond, err
	}
	response := runtimeSession.LiveOpeningMessageRespond
	if len(parts) > 0 && hasAudioInput(request) {
		response = runtimeSession.LiveOpeningMessageQueued
	}
	return parts, response, nil
}

func assembleLiveRequest(request serviceSession.Request, inputs requestInputs) runtimeSession.LiveRequest {
	inputCapturePath := request.ReplayPath
	if inputs.inspection != nil {
		inputCapturePath = inputs.inspection.CapturePath
	}
	result := runtimeSession.LiveRequest{
		SessionID:           cliLiveParticipantID,
		ParticipantID:       cliLiveParticipantID,
		Provider:            inputs.provider,
		Model:               inputs.model,
		BaseURL:             inputs.baseURL,
		RealtimeURL:         realtimeEndpoint(inputs.provider, inputs.baseURL),
		CredentialReference: inputs.credentialRef,
		Instructions:        inputs.instructions,
		OpeningPrompt:       inputs.requestPrompt, OpeningPromptPresent: inputs.promptPresent,
		OpeningContentParts: inputs.openingParts, OpeningMessageResponse: inputs.openingResponse,
		Voice:                         request.Voice,
		ReasoningEffort:               reasoningEffort(inputs.effective, request),
		InputTranscription:            !request.NoInputTranscription,
		InputTranscriptionModel:       inputTranscriptionModel(inputs.effective),
		InputAudioFormat:              string(models.AudioFormatPCM16),
		OutputAudioFormat:             string(models.AudioFormatPCM16),
		InputAudioSampleRate:          inputs.inputRate,
		OutputAudioSampleRate:         inputs.outputRate,
		OutputAudioContinuous:         request.AudioOutputPath == "-",
		TurnDetection:                 turnDetectionPolicy(inputs.effective),
		ClientOwnsAudioTurnBoundaries: request.ClientOwnsAudioTurnBoundaries || len(request.AudioTurns) > 0,
		Replay: runtimeSession.LiveReplayPolicy{
			InputCapturePath: inputCapturePath, OutputCapturePath: request.RecordPath,
			Timing: replayTiming(request.ReplayTiming),
		},
		ReplayPlan:            inputs.replayPlan,
		MaxDuration:           request.MaxDuration,
		SessionUpdatedTimeout: request.SessionUpdatedTimeout,
		Capabilities:          inputs.capabilities,
		// --wait-for-close is an explicit persistent-session policy. It must
		// override the ordinary finite audio/output policy so a completed
		// response cannot cancel the provider stream before later stdin audio
		// reaches the same session.
		FinishAfterResponse: !request.WaitForClose && (inputs.promptPresent || hasAudioInput(request) || len(inputs.openingParts) > 0 || inputs.replayFinish || request.AudioOutputPath != ""),
		RateLimitRetry:      scheduledAudioRateLimitRetryPolicy(request),
		ExpectedResponses:   expectedResponses(request, inputs.promptPresent, inputs.openingParts, inputs.openingResponse),
	}
	appendToolNames(&result, inputs.capabilities)
	return result
}

func admitReplay(ctx context.Context, path string, inspection *runtimeReplay.CaptureInspection, service runtimeReplay.Service) (*runtimeReplay.CaptureInspection, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	if inspection != nil {
		if !inspection.IsRealtime() {
			return nil, fmt.Errorf("replay capture %s is not a realtime session", path)
		}
		return inspection, nil
	}
	if service == nil {
		return nil, errors.New("live replay service is unavailable")
	}
	loaded, err := service.InspectCapture(ctx, path)
	if err != nil {
		return nil, err
	}
	if !loaded.IsRealtime() {
		return nil, fmt.Errorf("replay capture %s is not a realtime session", path)
	}
	return &loaded, nil
}

func resolveCredentialReference(apiKey string, resolve func(string) string) string {
	if apiKey == "" || resolve == nil {
		return ""
	}
	return resolve(apiKey)
}

func buildCapabilities(cfg *config.Config, request serviceSession.Request, deps RequestDependencies) (*runtimeSession.LiveCapabilities, error) {
	if deps.Capabilities == nil {
		return nil, nil
	}
	capabilities, err := deps.Capabilities(ToolConfig(cfg, request))
	if err != nil {
		return nil, err
	}
	if capabilities != nil && deps.BindImagePreparer != nil {
		capabilities.Executor = deps.BindImagePreparer(capabilities.Executor)
	}
	return capabilities, nil
}

func openImages(paths []string, opener func([]string) ([]messages.ContentPart, error)) ([]messages.ContentPart, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if opener == nil {
		return nil, errors.New("live image opener is unavailable")
	}
	return opener(paths)
}

func hasAudioInput(request serviceSession.Request) bool {
	return request.AudioInput.Present || len(request.AudioTurns) > 0
}

func scheduledAudioRateLimitRetryPolicy(request serviceSession.Request) runtimeSession.LiveRateLimitRetryPolicy {
	if len(request.AudioTurns) == 0 {
		return runtimeSession.LiveRateLimitRetryPolicy{}
	}
	// Explicit finite turns are caller-owned and replayable. Opt them into the
	// runtime's bounded provider retry; ordinary live audio remains opt-in at
	// its owning host because retrying an arbitrary turn can duplicate effects.
	return runtimeSession.LiveRateLimitRetryPolicy{Enabled: true, MaxRetries: 1}
}

func buildReplayPlan(request serviceSession.Request, inspection *runtimeReplay.CaptureInspection, requestPrompt string, promptPresent bool) (*runtimeSession.LiveReplayPlan, string, bool, error) {
	if inspection == nil {
		return nil, requestPrompt, promptPresent, nil
	}
	if inspection.LivePlan == nil {
		// InspectCapture classifies caller-driven realtime captures even when
		// their recorded client actions cannot be reproduced by the narrow
		// self-driving plan. An explicit prompt, image, or audio input supplies
		// the actions for this invocation, so strict provider replay can still
		// consume the captured transport without inventing a plan.
		if promptPresent || len(request.ImagePaths) > 0 || hasAudioInput(request) {
			return nil, requestPrompt, promptPresent, nil
		}
		return nil, requestPrompt, promptPresent, errors.New("live replay plan is unavailable")
	}
	plan := *inspection.LivePlan
	// Explicit caller input is checked by the strict replay transport, which
	// retains event sequence, JSON location, and bounded divergence details.
	if hasAudioInput(request) {
		plan.AudioTurns = nil
	}
	if !replayPlanHasActions(plan) {
		return nil, requestPrompt, promptPresent, nil
	}
	if !promptPresent && plan.OpeningPromptPresent {
		requestPrompt = plan.OpeningPrompt
		promptPresent = true
	}
	return &plan, requestPrompt, promptPresent, nil
}

func replayPlanHasActions(plan runtimeSession.LiveReplayPlan) bool {
	return plan.OpeningPromptPresent || len(plan.AudioTurns) > 0 || plan.StopAfterResponse || plan.ProviderCloseExpected
}
