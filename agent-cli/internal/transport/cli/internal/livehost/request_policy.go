package livehost

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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
		if request.AudioInput.Present {
			// A file/stdin source is one finite audio turn even when the
			// persistent --wait-for-close policy keeps the provider open.
			return 1
		}
		return 0
	}
	total := len(request.AudioTurns)
	if promptPresent && !(len(openingParts) > 0 && openingResponse == runtimeSession.LiveOpeningMessageQueued) {
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

func projectProviderCapture(trace *recording.Trace, path string) error {
	if trace == nil {
		return errors.New("live audio trace is unavailable")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("provider capture path is unavailable for live audio trace")
	}
	loaded, err := gatewaytesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return fmt.Errorf("load provider capture for audio trace: %w", err)
	}
	calls := make(map[string]string)
	for _, event := range loaded.Capture.Records {
		if err := projectProviderEvent(trace, event, calls); err != nil {
			return err
		}
	}
	return nil
}

func projectProviderEvent(trace *recording.Trace, event gatewaytesting.CapturedSessionEvent, calls map[string]string) error {
	wire, err := traceWireEnvelope(event)
	if err != nil {
		return fmt.Errorf("project provider wire sequence %d: %w", event.Sequence, err)
	}
	kind := "provider_wire_receive"
	if event.Direction == gatewaytesting.DirectionClientToServer {
		kind = "provider_wire_send"
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: kind, Tick: uint64(event.Sequence), Clean: true, Payload: wire})
	return projectProviderToolEvent(trace, event, calls)
}

func projectProviderToolEvent(trace *recording.Trace, event gatewaytesting.CapturedSessionEvent, calls map[string]string) error {
	switch event.Type {
	case "response.function_call_arguments.done":
		callPayload, callID, callName, err := traceToolCall(event.Payload)
		if err != nil {
			return fmt.Errorf("project tool call sequence %d: %w", event.Sequence, err)
		}
		calls[callID] = callName
		trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_call", Tick: uint64(event.Sequence), Clean: true, Payload: callPayload})
	case "conversation.item.create":
		resultPayload, callID, ok, err := traceToolResult(event.Payload, calls)
		if err != nil {
			return fmt.Errorf("project tool result sequence %d: %w", event.Sequence, err)
		}
		if ok {
			trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_result", Tick: uint64(event.Sequence), Clean: true, Payload: resultPayload})
			delete(calls, callID)
		}
	}
	return nil
}

func traceWireEnvelope(event gatewaytesting.CapturedSessionEvent) ([]byte, error) {
	payload := event.Payload
	if len(payload) == 0 {
		payload = event.Data
	}
	if len(payload) == 0 {
		return nil, errors.New("provider payload is empty")
	}
	messageType := websocketTextMessage
	var textPayload json.RawMessage
	var binaryPayload []byte
	if json.Valid(payload) {
		textPayload = append(json.RawMessage(nil), payload...)
	} else {
		messageType = websocketBinaryMessage
		binaryPayload = append([]byte(nil), payload...)
	}
	return json.Marshal(struct {
		MessageType   int             `json:"message_type"`
		Payload       json.RawMessage `json:"payload,omitempty"`
		BinaryPayload []byte          `json:"binary_payload,omitempty"`
	}{MessageType: messageType, Payload: textPayload, BinaryPayload: binaryPayload})
}

func traceToolCall(payload []byte) ([]byte, string, string, error) {
	var raw struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, "", "", err
	}
	raw.CallID = strings.TrimSpace(raw.CallID)
	raw.Name = strings.TrimSpace(raw.Name)
	if raw.CallID == "" || raw.Name == "" || !json.Valid([]byte(raw.Arguments)) {
		return nil, "", "", errors.New("function call identity or arguments are invalid")
	}
	encoded, err := json.Marshal(struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{ID: raw.CallID, Name: raw.Name, Arguments: raw.Arguments})
	return encoded, raw.CallID, raw.Name, err
}

func traceToolResult(payload []byte, calls map[string]string) ([]byte, string, bool, error) {
	var raw struct {
		Item struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, "", false, err
	}
	if raw.Item.Type != "function_call_output" {
		return nil, "", false, nil
	}
	callID := strings.TrimSpace(raw.Item.CallID)
	name := strings.TrimSpace(calls[callID])
	if callID == "" || name == "" || len(raw.Item.Output) == 0 {
		return nil, "", false, errors.New("function call output identity is incomplete")
	}
	content := ""
	if err := json.Unmarshal(raw.Item.Output, &content); err != nil {
		if !json.Valid(raw.Item.Output) {
			return nil, "", false, fmt.Errorf("function call output is invalid: %w", err)
		}
		content = string(raw.Item.Output)
	}
	encoded, err := json.Marshal(struct {
		CallID   string `json:"call_id"`
		Name     string `json:"name"`
		Failed   bool   `json:"failed"`
		Response struct {
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
			Content    string `json:"content"`
		} `json:"response"`
	}{CallID: callID, Name: name, Response: struct {
		ToolCallID string `json:"tool_call_id"`
		Name       string `json:"name"`
		Content    string `json:"content"`
	}{ToolCallID: callID, Name: name, Content: content}})
	return encoded, callID, true, err
}
