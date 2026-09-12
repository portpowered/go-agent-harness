package service

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// Service implements the provider-neutral session configuration policy. It
// stores only the immutable model-catalog port supplied by Wire.
type Service struct {
	catalog providers.ModelCatalog
}

func New(catalog providers.ModelCatalog) *Service {
	return &Service{catalog: catalog}
}

func (s *Service) Validate(request sessionconfig.Request) error {
	if err := s.ValidateOpenAIRealtimeVoice(request.Voice); err != nil {
		return err
	}
	if err := s.ValidateOpenAIRealtimeReasoningEffort(request.ReasoningEffort); err != nil {
		return err
	}
	if _, err := s.ResolveRuntimeSelection(request); err != nil {
		return err
	}
	if err := s.ValidateCapture(request); err != nil {
		return err
	}
	return s.validateReplayCapture(request)
}

func (*Service) NormalizeReplayTiming(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", sessionconfig.ReplayTimingImmediate:
		return sessionconfig.ReplayTimingImmediate
	case sessionconfig.ReplayTimingRecorded:
		return sessionconfig.ReplayTimingRecorded
	default:
		return ""
	}
}

func (*Service) SupportedOpenAIRealtimeVoices() []string {
	voices := [...]string{"alloy", "ash", "ballad", "cedar", "coral", "echo", "marin", "sage", "shimmer", "verse"}
	return append([]string(nil), voices[:]...)
}

func (s *Service) ValidateOpenAIRealtimeVoice(voice string) error {
	if voice == "" {
		return nil
	}
	for _, supported := range s.SupportedOpenAIRealtimeVoices() {
		if voice == supported {
			return nil
		}
	}
	return &sessionconfig.InvalidOpenAIRealtimeVoiceError{
		Voice:           voice,
		SupportedVoices: s.SupportedOpenAIRealtimeVoices(),
	}
}

func (*Service) ValidateOpenAIRealtimeReasoningEffort(effort string) error {
	switch strings.TrimSpace(effort) {
	case "", "minimal", "low", "medium", "high", "xhigh":
		return nil
	default:
		return fmt.Errorf("--reasoning-effort must be one of minimal, low, medium, high, or xhigh; got %q", effort)
	}
}

func (*Service) CloneTurnDetection(policy *models.TurnDetectionConfig) *models.TurnDetectionConfig {
	if policy == nil {
		return nil
	}
	copy := *policy
	if policy.CreateResponse != nil {
		value := *policy.CreateResponse
		copy.CreateResponse = &value
	}
	if policy.InterruptResponse != nil {
		value := *policy.InterruptResponse
		copy.InterruptResponse = &value
	}
	return &copy
}

func (*Service) OpenAIRealtimeURL(config sessionconfig.ProviderConfig) string {
	base := strings.TrimSpace(config.BaseURL)
	if base == "" {
		base = sessionconfig.OpenAIRealtimeBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	if query.Get("model") == "" {
		query.Set("model", config.Model)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Service) ValidateCapture(request sessionconfig.Request) error {
	if request.RecordPath != "" && request.ReplayPath != "" {
		return fmt.Errorf("agent session does not support --record and --replay together; choose one capture mode")
	}
	if request.RecordPath != "" && !isJSONCapturePath(request.RecordPath) {
		return fmt.Errorf("--record path %q must end with .json", request.RecordPath)
	}
	if request.ReplayPath != "" && !isJSONCapturePath(request.ReplayPath) {
		return fmt.Errorf("--replay path %q must end with .json", request.ReplayPath)
	}
	if timing := s.NormalizeReplayTiming(request.ReplayTiming); timing == "" {
		return fmt.Errorf("--replay-timing %q is invalid; use immediate or recorded", request.ReplayTiming)
	} else if timing == sessionconfig.ReplayTimingRecorded && request.ReplayPath == "" {
		return fmt.Errorf("--replay-timing recorded requires --replay")
	}
	return nil
}

func (s *Service) validateReplayCapture(request sessionconfig.Request) error {
	if request.ReplayPath != "" && request.SessionInferencer == nil {
		if _, err := testing.LoadSessionCaptureForReplay(request.ReplayPath); err != nil {
			return fmt.Errorf("replay session capture %s: %w", request.ReplayPath, err)
		}
	}
	return nil
}

func (s *Service) ResolveRuntimeSelection(request sessionconfig.Request) (sessionconfig.RuntimeSelection, error) {
	transport, err := normalizeTransport(request.Transport)
	if err != nil {
		return sessionconfig.RuntimeSelection{}, err
	}
	signaling, err := resolveSignaling(request)
	if err != nil {
		return sessionconfig.RuntimeSelection{}, err
	}
	if err := validateRuntimePrerequisites(transport, signaling, request.MediaSource); err != nil {
		return sessionconfig.RuntimeSelection{}, err
	}
	return sessionconfig.RuntimeSelection{
		Transport:         transport,
		SignalingEndpoint: signaling,
		MediaSource:       request.MediaSource,
	}, nil
}

func normalizeTransport(value string) (string, error) {
	transport := strings.ToLower(strings.TrimSpace(value))
	if transport == "" {
		return sessionconfig.TransportWebSocket, nil
	}
	if transport != sessionconfig.TransportWebSocket && transport != sessionconfig.TransportWebRTC {
		return "", selectionError(
			[]string{"transport"},
			fmt.Errorf("%w: %q (want %q or %q)", sessionconfig.ErrInvalidSessionTransport, value, sessionconfig.TransportWebSocket, sessionconfig.TransportWebRTC),
		)
	}
	return transport, nil
}

func resolveSignaling(request sessionconfig.Request) (string, error) {
	signaling := request.Signaling
	if request.SignalingEndpoint == "" {
		return signaling, nil
	}
	if signaling != "" && signaling != request.SignalingEndpoint {
		return "", selectionError(
			[]string{"signaling", "signaling-endpoint"},
			sessionconfig.ErrSessionRuntimeSelectionConflict,
		)
	}
	return request.SignalingEndpoint, nil
}

func validateRuntimePrerequisites(transport, signaling, media string) error {
	var fields []string
	var causes []error
	if transport == sessionconfig.TransportWebSocket {
		if strings.TrimSpace(signaling) != "" {
			fields = append(fields, "transport", "signaling")
			causes = append(causes, sessionconfig.ErrSessionSignalingRequiresWebRTC)
		}
		if strings.TrimSpace(media) != "" {
			fields = append(fields, "transport", "media-source")
			causes = append(causes, sessionconfig.ErrSessionMediaSourceRequiresWebRTC)
		}
	} else {
		if strings.TrimSpace(signaling) == "" {
			fields = append(fields, "transport", "signaling")
			causes = append(causes, sessionconfig.ErrSessionWebRTCRequiresSignaling)
		}
		if strings.TrimSpace(media) == "" {
			fields = append(fields, "transport", "media-source")
			causes = append(causes, sessionconfig.ErrSessionWebRTCRequiresMediaSource)
		}
	}
	if len(causes) == 0 {
		return nil
	}
	return selectionError(uniqueFields(fields), errors.Join(causes...))
}

func selectionError(fields []string, err error) error {
	return &sessionconfig.RuntimeSelectionError{Fields: append([]string(nil), fields...), Err: err}
}

func uniqueFields(fields []string) []string {
	seen := make(map[string]struct{}, len(fields))
	unique := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		unique = append(unique, field)
	}
	return unique
}

func isJSONCapturePath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

func (s *Service) ResolveProvider(request sessionconfig.Request) string {
	if provider := strings.ToLower(strings.TrimSpace(request.Provider)); provider != "" {
		return provider
	}
	if request.Defaults.Session != nil {
		if provider := strings.ToLower(strings.TrimSpace(request.Defaults.Session.Provider)); provider != "" {
			return provider
		}
	}
	switch provider := strings.ToLower(strings.TrimSpace(request.Defaults.Provider)); provider {
	case sessionconfig.ProviderOpenAI, sessionconfig.ProviderGrok:
		return provider
	default:
		return sessionconfig.ProviderOpenAI
	}
}

func (s *Service) ResolveOpenAIRealtimeConfig(request sessionconfig.Request) (sessionconfig.ProviderConfig, error) {
	if request.ModelProvided && request.Model == "" {
		return sessionconfig.ProviderConfig{}, s.unsupportedModel(request.Model)
	}
	resolved, err := s.openAIProviderConfig(request)
	if err != nil {
		return sessionconfig.ProviderConfig{}, err
	}
	if strings.TrimSpace(resolved.APIKey) == "" {
		return sessionconfig.ProviderConfig{}, fmt.Errorf("%w: OpenAI API key is required for live realtime session mode (set %s, pass --api-key, or configure model.openai.api_key in %s)", sessionconfig.ErrOpenAIRealtimeAPIKeyMissing, sessionconfig.OpenAIRealtimeAPIKeyEnv, sessionconfig.ConfigFileName)
	}
	resolved.Model, err = s.resolveOpenAIModel(resolved.Model, request)
	if err != nil {
		return sessionconfig.ProviderConfig{}, err
	}
	metadata, ok := s.lookupModel(resolved.Model)
	if !ok {
		return sessionconfig.ProviderConfig{}, s.unsupportedModel(resolved.Model)
	}
	if err := s.resolveOpenAIReasoning(&resolved, request, metadata); err != nil {
		return sessionconfig.ProviderConfig{}, err
	}
	return resolved, nil
}

func (s *Service) openAIProviderConfig(request sessionconfig.Request) (sessionconfig.ProviderConfig, error) {
	provider := s.ResolveProvider(request)
	if provider == "" {
		return sessionconfig.ProviderConfig{}, missingProviderError()
	}
	if provider != sessionconfig.ProviderOpenAI {
		return sessionconfig.ProviderConfig{}, fmt.Errorf("--record supports provider %q only for OpenAI realtime sessions; got %q", sessionconfig.ProviderOpenAI, provider)
	}
	resolved := copyProviderConfig(request.Defaults.OpenAI)
	if request.Defaults.OpenAI == nil {
		resolved.Model = sessionconfig.OpenAIRealtimeDefaultModel
	}
	applyRequestOverrides(&resolved, request)
	return resolved, nil
}

func (s *Service) resolveOpenAIModel(model string, request sessionconfig.Request) (string, error) {
	if strings.TrimSpace(model) != "" {
		return model, nil
	}
	if !request.ModelProvided && request.Model == "" {
		return sessionconfig.OpenAIRealtimeDefaultModel, nil
	}
	return "", s.unsupportedModel(model)
}

func (s *Service) resolveOpenAIReasoning(config *sessionconfig.ProviderConfig, request sessionconfig.Request, metadata providers.RealtimeModel) error {
	config.ReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
	if config.ReasoningEffort == "" && request.Defaults.Session != nil {
		config.ReasoningEffort = strings.TrimSpace(request.Defaults.Session.ReasoningEffort)
	}
	if err := s.ValidateOpenAIRealtimeReasoningEffort(config.ReasoningEffort); err != nil {
		return err
	}
	if config.ReasoningEffort != "" && !metadata.SupportsReasoning {
		return fmt.Errorf("OpenAI model %q does not support --reasoning-effort; use %q", config.Model, sessionconfig.OpenAIRealtimeReasoningModel)
	}
	return nil
}

func (s *Service) ResolveGrokConfig(request sessionconfig.Request) (sessionconfig.ProviderConfig, error) {
	provider := s.ResolveProvider(request)
	if provider == "" {
		return sessionconfig.ProviderConfig{}, missingProviderError()
	}
	if provider != sessionconfig.ProviderGrok {
		return sessionconfig.ProviderConfig{}, fmt.Errorf("--record supports provider %q only; got %q", sessionconfig.ProviderGrok, provider)
	}
	resolved := copyProviderConfig(request.Defaults.Grok)
	applyRequestOverrides(&resolved, request)
	if resolved.APIKey == "" {
		return sessionconfig.ProviderConfig{}, fmt.Errorf("grok API key is required for live session record mode (set AGENT_MODEL__GROK__API_KEY, pass --api-key, or configure model.grok.api_key in %s)", sessionconfig.ConfigFileName)
	}
	if resolved.Model == "" {
		return sessionconfig.ProviderConfig{}, fmt.Errorf("grok session model is required for live session record mode (set AGENT_MODEL__GROK__MODEL, pass --model, or configure model.grok.model in %s)", sessionconfig.ConfigFileName)
	}
	return resolved, nil
}

func (s *Service) ResolveOpenAI(request sessionconfig.Request) (sessionconfig.ProviderConfig, error) {
	return s.ResolveOpenAIRealtimeConfig(request)
}

func (s *Service) ResolveGrok(request sessionconfig.Request) (sessionconfig.ProviderConfig, error) {
	return s.ResolveGrokConfig(request)
}

func (s *Service) lookupModel(model string) (providers.RealtimeModel, bool) {
	if s == nil || s.catalog == nil {
		return providers.RealtimeModel{}, false
	}
	return s.catalog.LookupRealtimeModel(sessionconfig.ProviderOpenAI, strings.TrimSpace(model))
}

func (s *Service) unsupportedModel(model string) error {
	if s == nil || s.catalog == nil {
		return providers.ErrModelCatalogRequired
	}
	return &providers.UnsupportedRealtimeModelError{
		Provider:        "OpenAI",
		Model:           model,
		SupportedModels: append([]string(nil), s.catalog.SupportedRealtimeModelIDs(sessionconfig.ProviderOpenAI)...),
	}
}

func copyProviderConfig(config *sessionconfig.ProviderConfig) sessionconfig.ProviderConfig {
	if config == nil {
		return sessionconfig.ProviderConfig{}
	}
	return *config
}

func applyRequestOverrides(config *sessionconfig.ProviderConfig, request sessionconfig.Request) {
	if request.APIKey != "" {
		config.APIKey = request.APIKey
	}
	if request.Model != "" {
		config.Model = request.Model
	}
	if request.BaseURL != "" {
		config.BaseURL = request.BaseURL
	}
}

func missingProviderError() error {
	return fmt.Errorf("--record requires --provider %s or --provider %s for live session inference", sessionconfig.ProviderGrok, sessionconfig.ProviderOpenAI)
}

var _ sessionconfig.Service = (*Service)(nil)
