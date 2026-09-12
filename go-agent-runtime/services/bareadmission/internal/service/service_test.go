package service_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission"
	serviceimpl "github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission/internal/service"
)

func testService() bareadmission.Service { return serviceimpl.New() }

func testCatalog() *bareadmission.ModelCatalog {
	return &bareadmission.ModelCatalog{Models: []bareadmission.Model{
		{Provider: bareadmission.ProviderOpenAI, ID: "gpt-realtime"},
		{Provider: bareadmission.ProviderOpenAI, ID: "gpt-realtime-2.1-mini", SupportsReasoning: true},
		{Provider: bareadmission.ProviderOpenAI, ID: "gpt-realtime-2.1", SupportsReasoning: true},
	}}
}

func baseRequest() bareadmission.Request {
	return bareadmission.Request{
		Catalog: testCatalog(), Provider: bareadmission.ProviderOpenAI,
		Model: "gpt-realtime", APIKey: "request-key",
		Config: &bareadmission.ConfigSnapshot{Path: "/tmp/bare-session-config.yaml"},
	}
}

func TestResolveDefaultsAndProviderPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		request   bareadmission.Request
		provider  string
		model     string
		transport string
	}{
		{name: "explicit provider", request: baseRequest(), provider: bareadmission.ProviderOpenAI, model: "gpt-realtime", transport: bareadmission.TransportWebSocket},
		{name: "session provider", request: func() bareadmission.Request {
			r := baseRequest()
			r.Provider = ""
			r.Model = ""
			r.Config.Session = &bareadmission.SessionConfig{Provider: " GROK ", Model: "grok-voice", Transport: "webrtc"}
			r.Config.Grok = &bareadmission.ProviderConfig{Model: "grok-config", APIKey: "grok-key"}
			return r
		}(), provider: bareadmission.ProviderGrok, model: "grok-voice", transport: bareadmission.TransportWebRTC},
		{name: "supported model provider", request: func() bareadmission.Request {
			r := baseRequest()
			r.Provider = ""
			r.Model = ""
			r.Config.ModelProvider = " GROK "
			r.Config.Grok = &bareadmission.ProviderConfig{Model: "grok-config", APIKey: "grok-key"}
			return r
		}(), provider: bareadmission.ProviderGrok, model: "grok-config", transport: bareadmission.TransportWebSocket},
		{name: "generic provider falls back to OpenAI", request: func() bareadmission.Request {
			r := baseRequest()
			r.Provider = ""
			r.Model = ""
			r.Config.ModelProvider = "openrouter"
			r.EnvironmentOpenAIAPIKey = "environment-key"
			return r
		}(), provider: bareadmission.ProviderOpenAI, model: bareadmission.DefaultOpenAIModel, transport: bareadmission.TransportWebSocket},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := testService().Resolve(context.Background(), testCase.request)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if result.Provider != testCase.provider || result.Model != testCase.model || result.Transport != testCase.transport || !result.BareLive {
				t.Fatalf("result = %+v, want provider/model/transport %q/%q/%q", result, testCase.provider, testCase.model, testCase.transport)
			}
		})
	}
}

func TestResolveCredentialPrecedenceAndRedactedIdentity(t *testing.T) {
	request := baseRequest()
	request.APIKey = ""
	request.EnvironmentOpenAIAPIKey = "environment-key"
	request.Config.OpenAI = &bareadmission.ProviderConfig{APIKey: "config-key", BaseURL: "wss://config.example"}
	result, err := testService().Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("config credential Resolve() error = %v", err)
	}
	if result.APIKey != "config-key" || result.BaseURL != "wss://config.example" {
		t.Fatalf("result credential/base URL = %q/%q, want config values", result.APIKey, result.BaseURL)
	}

	request.APIKey = "explicit-key"
	request.BaseURL = "wss://explicit.example"
	result, err = testService().Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("explicit credential Resolve() error = %v", err)
	}
	if result.APIKey != "explicit-key" || result.BaseURL != request.BaseURL {
		t.Fatalf("explicit result = %+v, want explicit values", result)
	}

	request.APIKey = ""
	request.Config.OpenAI.APIKey = ""
	request.EnvironmentOpenAIAPIKey = ""
	_, err = testService().Resolve(context.Background(), request)
	if err == nil {
		t.Fatal("missing credential Resolve() error = nil")
	}
	var credentialErr *bareadmission.CredentialError
	if !errors.As(err, &credentialErr) || !errors.Is(err, bareadmission.ErrCredentialMissing) {
		t.Fatalf("error = %T %v, want typed credential identity", err, err)
	}
	want := "openai realtime api key is missing: bare live session requires an OpenAI API key; set OPENAI_API_KEY or AGENT_MODEL__OPENAI__API_KEY, pass --api-key, or configure model.openai.api_key in /tmp/bare-session-config.yaml"
	if err.Error() != want {
		t.Fatalf("credential error = %q, want %q", err, want)
	}
	if strings.Contains(err.Error(), "environment-key") || strings.Contains(err.Error(), "config-key") || strings.Contains(err.Error(), "explicit-key") {
		t.Fatalf("credential error disclosed secret material: %q", err)
	}
}

func TestResolveModelAdmissionAndProviderFailures(t *testing.T) {
	request := baseRequest()
	request.Model = "unknown-model"
	_, err := testService().Resolve(context.Background(), request)
	if err == nil || !errors.Is(err, bareadmission.ErrUnsupportedRealtimeModel) {
		t.Fatalf("unknown model error = %v, want unsupported model", err)
	}
	var modelErr *bareadmission.UnsupportedRealtimeModelError
	if !errors.As(err, &modelErr) || modelErr.Model != "unknown-model" || !reflect.DeepEqual(modelErr.SupportedModels, []string{"gpt-realtime", "gpt-realtime-2.1-mini", "gpt-realtime-2.1"}) {
		t.Fatalf("model error = %+v, want exact catalog snapshot", modelErr)
	}

	request.Model = ""
	request.ModelProvided = true
	_, err = testService().Resolve(context.Background(), request)
	if err == nil || !errors.As(err, &modelErr) || modelErr.Model != "" {
		t.Fatalf("explicit empty model error = %v, want typed empty-model rejection", err)
	}

	request = baseRequest()
	request.Provider = " openrouter "
	_, err = testService().Resolve(context.Background(), request)
	if err == nil || !errors.Is(err, bareadmission.ErrUnsupportedProvider) || err.Error() != `unsupported bare live session provider: "openrouter" (bare sessions support "openai" and "grok")` {
		t.Fatalf("unsupported provider error = %v, want exact classification", err)
	}

	request = baseRequest()
	request.Catalog = nil
	_, err = testService().Resolve(context.Background(), request)
	if err == nil || !errors.Is(err, bareadmission.ErrModelCatalogRequired) || !strings.Contains(err.Error(), "OpenAI realtime model admission") {
		t.Fatalf("nil catalog error = %v, want fail-closed model admission", err)
	}
}

func TestResolveServerVADAndCopiedBooleans(t *testing.T) {
	createResponse := false
	interruptResponse := true
	request := baseRequest()
	request.Config.Session = &bareadmission.SessionConfig{
		Transport: "webrtc",
		VAD:       &bareadmission.VADConfig{Type: "server_vad", Threshold: 0.72, PrefixPaddingMs: 120, SilenceDurationMs: 640, CreateResponse: &createResponse, InterruptResponse: &interruptResponse},
	}
	result, err := testService().Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("server VAD Resolve() error = %v", err)
	}
	if result.Transport != bareadmission.TransportWebRTC || result.TurnDetection == nil || result.TurnDetection.Type != "server_vad" || result.TurnDetection.Threshold != 0.72 || result.TurnDetection.PrefixPaddingMs != 120 || result.TurnDetection.SilenceDurationMs != 640 || result.TurnDetection.CreateResponse == nil || *result.TurnDetection.CreateResponse || result.TurnDetection.InterruptResponse == nil || !*result.TurnDetection.InterruptResponse {
		t.Fatalf("server VAD result = %+v, want copied policy", result)
	}
	*result.TurnDetection.CreateResponse = true
	if createResponse {
		t.Fatal("result mutation changed caller VAD boolean")
	}
}

func TestResolveSemanticVADValidation(t *testing.T) {
	request := baseRequest()
	request.Transport = "ws"
	request.TransportProvided = true
	request.Config.Session = &bareadmission.SessionConfig{VAD: &bareadmission.VADConfig{Type: "semantic_vad", Eagerness: " LOW "}}
	result, err := testService().Resolve(context.Background(), request)
	if err != nil || result.Transport != bareadmission.TransportWebSocket || result.TurnDetection == nil || result.TurnDetection.Type != "semantic_vad" || result.TurnDetection.Eagerness != "low" {
		t.Fatalf("semantic VAD result/error = %+v/%v", result, err)
	}

	request.Config.Session.VAD = &bareadmission.VADConfig{Type: "semantic_vad", SilenceDurationMs: 1}
	if _, err = testService().Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("semantic incompatible VAD error = %v", err)
	}
	request.Config.Session.VAD = &bareadmission.VADConfig{Type: "semantic_vad", Eagerness: "instant"}
	if _, err = testService().Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "eagerness") {
		t.Fatalf("semantic eagerness error = %v", err)
	}
}

func TestResolveDisabledVADAndInvalidTransport(t *testing.T) {
	request := baseRequest()
	request.Config.Session = &bareadmission.SessionConfig{VAD: &bareadmission.VADConfig{Type: "server_vad", Eagerness: "low"}}
	if _, err := testService().Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "server_vad does not support eagerness") {
		t.Fatalf("server eagerness error = %v", err)
	}

	request.Config.Session.VAD = &bareadmission.VADConfig{Enabled: func() *bool { value := false; return &value }(), Type: "semantic_vad", SilenceDurationMs: 1}
	result, err := testService().Resolve(context.Background(), request)
	if err != nil || result.TurnDetection != nil {
		t.Fatalf("disabled VAD result/error = %+v/%v, want nil VAD", result.TurnDetection, err)
	}

	request.Transport = "invalid"
	request.TransportProvided = true
	if _, err = testService().Resolve(context.Background(), request); err == nil || !errors.Is(err, bareadmission.ErrInvalidTransport) || !strings.Contains(err.Error(), `"invalid"`) {
		t.Fatalf("invalid transport error = %v, want typed transport identity", err)
	}
}

func TestResolveGrokDefaultsTranscriptionAndDevices(t *testing.T) {
	disabled := false
	request := baseRequest()
	request.Provider = bareadmission.ProviderGrok
	request.Model = ""
	request.APIKey = ""
	request.Config.OpenAI = nil
	request.Config.Grok = &bareadmission.ProviderConfig{Model: "grok-voice", APIKey: "grok-key"}
	request.Config.Session = &bareadmission.SessionConfig{
		InputDevice: "persisted:mic", OutputDevice: "persisted:speakers",
		InputTranscription: &bareadmission.TranscriptionConfig{Enabled: &disabled, Model: "custom-transcriber"},
	}
	request.Device = bareadmission.DeviceSelection{InputDevice: "cli:mic", OutputDevice: "", InputPresent: true, OutputPresent: true}
	result, err := testService().Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("Grok Resolve() error = %v", err)
	}
	if result.Model != "grok-voice" || result.APIKey != "grok-key" || result.TurnDetection == nil || result.TurnDetection.Type != "server_vad" || result.InputAudioTranscription == nil || result.InputAudioTranscription.Enabled || result.InputAudioTranscription.Model != "custom-transcriber" {
		t.Fatalf("Grok result = %+v, want server VAD and persisted transcription", result)
	}
	if result.Device.InputDevice != "cli:mic" || result.Device.OutputDevice != "" || !result.Device.InputPresent || !result.Device.OutputPresent {
		t.Fatalf("device result = %+v, want explicit CLI selectors and both directions", result.Device)
	}

	request.NoInputTranscription = true
	result, err = testService().Resolve(context.Background(), request)
	if err != nil || result.InputAudioTranscription == nil || result.InputAudioTranscription.Enabled || result.InputAudioTranscription.Model != "" {
		t.Fatalf("explicit transcription disable result/error = %+v/%v", result.InputAudioTranscription, err)
	}

	request = baseRequest()
	request.Provider = bareadmission.ProviderGrok
	request.Model = "grok-voice"
	request.Config = nil
	request.APIKey = "grok-key"
	result, err = testService().Resolve(context.Background(), request)
	if err != nil || result.TurnDetection == nil || result.TurnDetection.Type != "server_vad" {
		t.Fatalf("nil config Grok result/error = %+v/%v, want built-in default", result, err)
	}
}

func TestResolveCopiesInputsAndIsDeterministic(t *testing.T) {
	enabled := true
	createResponse := false
	request := baseRequest()
	request.Config.Session = &bareadmission.SessionConfig{
		VAD:                &bareadmission.VADConfig{Enabled: &enabled, Type: "server_vad", CreateResponse: &createResponse},
		InputTranscription: &bareadmission.TranscriptionConfig{Enabled: &enabled, Model: " custom "},
	}
	first, err := testService().Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("first Resolve() error = %v", err)
	}
	expected, err := testService().Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("expected Resolve() error = %v", err)
	}
	first.TurnDetection.Type = "mutated"
	*first.TurnDetection.CreateResponse = true
	first.InputAudioTranscription.Model = "mutated"
	second, err := testService().Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if !reflect.DeepEqual(expected, second) {
		t.Fatalf("second result changed after first result mutation: expected=%+v second=%+v", expected, second)
	}
	if *second.TurnDetection.CreateResponse {
		t.Fatal("second result reused first result's copied boolean")
	}
	if second.InputAudioTranscription.Model != "custom" {
		t.Fatalf("transcription model = %q, want trimmed caller snapshot", second.InputAudioTranscription.Model)
	}

	uncanceled, err := testService().Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("deterministic Resolve() error = %v", err)
	}
	if !reflect.DeepEqual(second, uncanceled) {
		t.Fatalf("repeated resolution changed result: second=%+v repeated=%+v", second, uncanceled)
	}
}

func TestResolveCancellationHasNoResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := testService().Resolve(ctx, baseRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Resolve() error = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(result, bareadmission.Result{}) {
		t.Fatalf("canceled Resolve() result = %+v, want zero result", result)
	}
}
