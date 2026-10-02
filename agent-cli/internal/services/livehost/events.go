package livehost

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	cliLiveParticipantID = "cli"
	cliLiveDefaultModel  = "gpt-realtime-2.1-mini"
	cliLiveDefaultRate   = 24000
	// liveSessionsPath is the GPT-Live primary WebSocket path under /v1.
	liveSessionsPath = "/live/sessions"
	// liveModelPrefix starts every GPT-Live model id (gpt-live-1).
	liveModelPrefix = "gpt-live"
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
	model = resolveModel(cfg, request, model, replayModel, defaultModel(provider))
	apiKey, model, baseURL = applyProviderOverrides(request, apiKey, model, baseURL)
	if err := validateProviderCredential(provider, apiKey, request.ReplayPath); err != nil {
		return provider, model, "", baseURL, err
	}
	return provider, model, apiKey, baseURL, nil
}

// CredentialValues resolves the raw provider credential a live invocation
// uses, so evidence and rendered failures can redact it.
func CredentialValues(request serviceSession.Request) ([]string, error) {
	if request.LoadedConfig == nil {
		return nil, errors.New("live session configuration is unavailable")
	}
	effective := request.LoadedConfig.ApplyOverrides("", request.Model, request.Provider, request.BaseURL)
	_, _, apiKey, _, err := ProviderValues(effective, request, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, nil
	}
	return []string{apiKey}, nil
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
		if provider != config.ProviderOpenAI && provider != config.ProviderGrok && provider != config.ProviderOpenAILive {
			provider = config.ProviderOpenAI
		}
	}
	return provider, replayModel
}

// defaultModel is the live model a provider uses when none is configured.
func defaultModel(provider string) string {
	if provider == config.ProviderOpenAILive {
		return runtimeProviders.OpenAILive1Model
	}
	return cliLiveDefaultModel
}

func resolveModel(cfg config.Config, request serviceSession.Request, model, replayModel, fallback string) string {
	if cfg.Session != nil && cfg.Session.Model != "" && request.Model == "" && sessionModelApplies(fallback, cfg.Session.Model) {
		model = cfg.Session.Model
	}
	if model == "" {
		model = fallback
	}
	if model == fallback && replayModel != "" && !request.ModelProvided {
		model = replayModel
	}
	return model
}

// sessionModelApplies reports whether a configured session.model belongs to
// the provider whose default is fallback. session.model usually names a
// Realtime model, which GPT-Live admission would reject, so openai-live takes
// only a GPT-Live model from it and otherwise keeps gpt-live-1.
func sessionModelApplies(fallback, sessionModel string) bool {
	if fallback != runtimeProviders.OpenAILive1Model {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(sessionModel), liveModelPrefix)
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
		if provider == config.ProviderOpenAILive {
			return fmt.Errorf("%s requires an OpenAI API key (set AGENT_MODEL__OPENAI__API_KEY, pass --api-key, or configure model.openai.api_key in %s); a ChatGPT sign-in is not accepted for %s", provider, config.ConfigFileName, runtimeProviders.OpenAILive1Model)
		}
		if provider == config.ProviderGrok {
			return fmt.Errorf("grok API key is required for live session record mode (set AGENT_MODEL__GROK__API_KEY, pass --api-key, or configure model.grok.api_key in %s)", config.ConfigFileName)
		}
		return fmt.Errorf("%s realtime api key is missing (set AGENT_MODEL__%s__API_KEY, pass --api-key, or configure the provider in %s)", provider, strings.ToUpper(provider), config.ConfigFileName)
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
	case config.ProviderOpenAILive:
		// GPT-Live uses the OpenAI API key and base URL. model.openai.model
		// names a Realtime model, so it is not the GPT-Live default.
		if cfg.Model.OpenAI != nil {
			return "", cfg.Model.OpenAI.APIKey, cfg.Model.OpenAI.BaseURL, nil
		}
	default:
		return "", "", "", fmt.Errorf("unsupported realtime session provider %q", provider)
	}
	return "", "", "", nil
}

func replayRates(plan *runtimeSession.LiveReplayPlan, request serviceSession.Request, inspection *runtimeReplay.CaptureInspection) (int, int) {
	inputRate, outputRate := cliLiveDefaultRate, cliLiveDefaultRate
	if plan == nil && inspection != nil && inspection.Kind == runtimeReplay.CaptureKindTurn {
		// Semantic turn captures do not carry realtime negotiation metadata.
		// Keep the established PCM session default instead of treating them as
		// a 24 kHz provider transport.
		return audio.SampleRate, audio.SampleRate
	}
	if plan == nil {
		return inputRate, outputRate
	}
	if plan.InputAudioSampleRate > 0 {
		inputRate = plan.InputAudioSampleRate
	} else if hasAudioInput(request) {
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
		if request.AudioInput.Present {
			// A file/stdin source is one finite audio turn even when the
			// persistent --wait-for-close policy keeps the provider open.
			return 1
		}
		return 0
	}
	total := len(request.AudioTurns)
	if promptPresent && (len(openingParts) == 0 || openingResponse != runtimeSession.LiveOpeningMessageQueued) {
		total++
	}
	return total
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
	cloned := *value
	return &cloned
}

func ToolConfig(cfg *config.Config, request serviceSession.Request) *config.Config {
	if cfg == nil {
		return nil
	}
	copyConfig := *cfg
	copyConfig.Tools.List = append([]config.ToolEntry(nil), cfg.Tools.List...)
	copyConfig.FilesystemWorkDir = request.WorkDir
	copyConfig.FilesystemAllowPaths = append([]string(nil), request.AllowPaths...)
	copyConfig.FilesystemHomeDir = request.HomeDir
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
	suffix := ""
	switch provider {
	case config.ProviderOpenAI:
		suffix = "/realtime"
	case config.ProviderOpenAILive:
		suffix = liveSessionsPath
	}
	if suffix != "" && !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), suffix) {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + suffix
	}
	return parsed.String()
}
