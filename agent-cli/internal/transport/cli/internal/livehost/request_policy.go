package livehost

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	cliLiveParticipantID = "cli"
	cliLiveDefaultModel  = "gpt-realtime-2.1-mini"
	cliLiveDefaultRate   = 24000
)

// ProviderValues resolves provider selection using only the host's already
// loaded config and an optional replay inspection. It never reads credentials
// or capture bytes itself.
func ProviderValues(cfg config.Config, request serviceSession.Request, inspection *runtimeReplay.CaptureInspection) (string, string, string, string, error) {
	provider, replayModel := selectProvider(cfg, request, inspection)
	model, apiKey, baseURL, err := providerConfig(cfg, provider)
	if err != nil {
		return provider, "", "", "", err
	}
	model = resolveModel(cfg, request, model, replayModel)
	apiKey, model, baseURL = applyProviderOverrides(request, apiKey, model, baseURL)
	if err := validateProviderCredential(provider, apiKey, request.ReplayPath); err != nil {
		return provider, model, "", baseURL, err
	}
	return provider, model, apiKey, baseURL, nil
}

func selectProvider(cfg config.Config, request serviceSession.Request, inspection *runtimeReplay.CaptureInspection) (string, string) {
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	replayModel := ""
	if provider == "" && !request.ProviderProvided && inspection != nil {
		provider = strings.ToLower(strings.TrimSpace(inspection.Provider))
		replayModel = strings.TrimSpace(inspection.Model)
	}
	if provider == "" && cfg.Session != nil {
		provider = strings.ToLower(strings.TrimSpace(cfg.Session.Provider))
	}
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(cfg.Model.Provider))
		if provider != config.ProviderOpenAI && provider != config.ProviderGrok {
			provider = config.ProviderOpenAI
		}
	}
	return provider, replayModel
}

func resolveModel(cfg config.Config, request serviceSession.Request, model, replayModel string) string {
	if cfg.Session != nil && cfg.Session.Model != "" && request.Model == "" {
		model = cfg.Session.Model
	}
	if model == "" {
		model = cliLiveDefaultModel
	}
	if model == cliLiveDefaultModel && replayModel != "" && !request.ModelProvided {
		model = replayModel
	}
	return model
}

func applyProviderOverrides(request serviceSession.Request, apiKey, model, baseURL string) (string, string, string) {
	if request.APIKey != "" {
		apiKey = request.APIKey
	}
	if request.Model != "" {
		model = request.Model
	}
	if request.BaseURL != "" {
		baseURL = request.BaseURL
	}
	return apiKey, model, baseURL
}

func validateProviderCredential(provider, apiKey, replayPath string) error {
	if apiKey == "" && provider != config.ProviderLocal && replayPath == "" {
		return fmt.Errorf("%s realtime api key is missing", provider)
	}
	return nil
}

func providerConfig(cfg config.Config, provider string) (string, string, string, error) {
	switch provider {
	case config.ProviderOpenAI:
		if cfg.Model.OpenAI != nil {
			return cfg.Model.OpenAI.Model, cfg.Model.OpenAI.APIKey, cfg.Model.OpenAI.BaseURL, nil
		}
	case config.ProviderGrok:
		if cfg.Model.Grok != nil {
			return cfg.Model.Grok.Model, cfg.Model.Grok.APIKey, cfg.Model.Grok.BaseURL, nil
		}
	default:
		return "", "", "", fmt.Errorf("unsupported realtime session provider %q", provider)
	}
	return "", "", "", nil
}

func replayRates(plan *runtimeSession.LiveReplayPlan, request serviceSession.Request) (int, int) {
	inputRate, outputRate := cliLiveDefaultRate, cliLiveDefaultRate
	if plan == nil {
		return inputRate, outputRate
	}
	if plan.InputAudioSampleRate > 0 {
		inputRate = plan.InputAudioSampleRate
	} else if request.AudioInput.Present {
		inputRate = audio.SampleRate
	}
	if plan.OutputAudioSampleRate > 0 {
		outputRate = plan.OutputAudioSampleRate
	} else if request.AudioOutputPath != "" {
		outputRate = audio.SampleRate
	}
	return inputRate, outputRate
}

func expectedResponses(request serviceSession.Request, promptPresent bool, openingParts []messages.ContentPart, openingResponse runtimeSession.LiveOpeningMessageResponse) int {
	if len(request.AudioTurns) == 0 {
		return 0
	}
	total := len(request.AudioTurns)
	if promptPresent && !(len(openingParts) > 0 && openingResponse == runtimeSession.LiveOpeningMessageQueued) {
		total++
	}
	return total
}

func ResolvePrompt(value, workDir string) (string, error) {
	if value == "none" {
		return "", nil
	}
	if value != "" {
		if _, err := os.Stat(value); err == nil {
			data, readErr := os.ReadFile(value)
			if readErr != nil {
				return "", fmt.Errorf("read system prompt %s: %w", value, readErr)
			}
			return string(data), nil
		}
		return value, nil
	}
	if workDir == "" {
		return "", errors.New("resolve live prompt workspace: workdir is required")
	}
	path := filepath.Join(workDir, "AGENTS.md")
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("read AGENTS.md %s: %w", path, err)
}

func turnDetectionPolicy(cfg config.Config) *runtimeSession.LiveTurnDetection {
	if cfg.Session == nil || cfg.Session.VAD == nil || (cfg.Session.VAD.Enabled != nil && !*cfg.Session.VAD.Enabled) {
		return nil
	}
	policy := cfg.Session.VAD
	return &runtimeSession.LiveTurnDetection{
		Type: policy.Type, Threshold: policy.Threshold, PrefixPaddingMs: policy.PrefixPaddingMs,
		SilenceDurationMs: policy.SilenceDurationMs, CreateResponse: cloneBool(policy.CreateResponse),
		InterruptResponse: cloneBool(policy.InterruptResponse), Eagerness: policy.Eagerness,
	}
}

func inputTranscriptionModel(cfg config.Config) string {
	if cfg.Session == nil || cfg.Session.InputTranscription == nil {
		return ""
	}
	return cfg.Session.InputTranscription.Model
}

func reasoningEffort(cfg config.Config, request serviceSession.Request) string {
	if request.ReasoningEffort != "" {
		return request.ReasoningEffort
	}
	if cfg.Session != nil {
		return cfg.Session.ReasoningEffort
	}
	return ""
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func replayTiming(value string) runtimeSession.LiveReplayTiming {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "realtime", "recorded":
		return runtimeSession.LiveReplayTimingRealtime
	case "step":
		return runtimeSession.LiveReplayTimingStep
	default:
		return runtimeSession.LiveReplayTimingFast
	}
}

func ToolConfig(cfg *config.Config, request serviceSession.Request) *config.Config {
	if cfg == nil {
		return nil
	}
	copyConfig := *cfg
	copyConfig.Tools.List = append([]config.ToolEntry(nil), cfg.Tools.List...)
	copyConfig.FilesystemWorkDir = request.WorkDir
	copyConfig.FilesystemAllowPaths = append([]string(nil), request.AllowPaths...)
	set := func(id string, enabled bool) {
		for index := range copyConfig.Tools.List {
			if copyConfig.Tools.List[index].ID == id {
				copyConfig.Tools.List[index].Enabled = enabled
				return
			}
		}
		copyConfig.Tools.List = append(copyConfig.Tools.List, config.ToolEntry{ID: id, Enabled: enabled})
	}
	if !request.ComputerUse {
		set("show", false)
		set("mouse", false)
	}
	if !request.ExperimentalTools {
		for _, id := range []string{"load_skill", "sleep", "web_fetch", "web_search"} {
			set(id, false)
		}
	}
	if request.NoTerminalTools {
		for _, id := range []string{"exec", "read_file", "read_image", "write_file", "edit_file", "append_file", "list_dir"} {
			set(id, false)
		}
	}
	return &copyConfig
}

func realtimeEndpoint(provider, baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" {
		return baseURL
	}
	if parsed.Scheme == "http" {
		parsed.Scheme = "ws"
	}
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	}
	if provider == config.ProviderOpenAI && !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/realtime") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/realtime"
	}
	return parsed.String()
}

func appendToolNames(result *runtimeSession.LiveRequest, capabilities *runtimeSession.LiveCapabilities) {
	if result == nil || capabilities == nil {
		return
	}
	for _, definition := range capabilities.Definitions {
		result.ToolNames = append(result.ToolNames, definition.Name)
	}
}
