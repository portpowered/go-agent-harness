package wire

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const (
	probeOpenAIKey   = "sk-probe"
	probeGrokKey     = "xai-probe"
	probeGrokModel   = "grok-realtime-probe"
	probeCustomModel = "gpt-realtime-custom"
)

// clearProbeCredentials isolates resolution from the developer environment.
func clearProbeCredentials(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AGENT_MODEL__PROVIDER", "AGENT_MODEL__OPENAI__API_KEY", "AGENT_MODEL__OPENAI__MODEL",
		"AGENT_MODEL__GROK__API_KEY", "AGENT_MODEL__GROK__MODEL", "AGENT_SESSION__PROVIDER",
	} {
		t.Setenv(name, "")
	}
}

func probeRequest(t *testing.T, provider, apiKey, model string) serviceDevices.DeviceProbeRequest {
	t.Helper()
	return serviceDevices.DeviceProbeRequest{ConfigDir: t.TempDir(), Provider: provider, APIKey: apiKey, Model: model}
}

func TestDeviceProbeProviderNamePrecedence(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		cfg       config.Config
		want      string
	}{
		{name: "explicit provider wins", requested: " Grok ", cfg: config.Config{Session: &config.SessionConfig{Provider: config.ProviderOpenAI}}, want: config.ProviderGrok},
		{name: "session provider", cfg: config.Config{Session: &config.SessionConfig{Provider: "GROK"}, Model: config.ModelConfig{Provider: config.ProviderOpenAI}}, want: config.ProviderGrok},
		{name: "realtime-capable model provider", cfg: config.Config{Model: config.ModelConfig{Provider: config.ProviderGrok}}, want: config.ProviderGrok},
		{name: "non-realtime model provider falls back to openai", cfg: config.Config{Model: config.ModelConfig{Provider: "claude"}}, want: config.ProviderOpenAI},
		{name: "empty config defaults to openai", want: config.ProviderOpenAI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceProbeProviderName(tc.requested, &tc.cfg); got != tc.want {
				t.Fatalf("deviceProbeProviderName(%q) = %q, want %q", tc.requested, got, tc.want)
			}
		})
	}
}

func TestResolveDeviceProbeProviderOpenAIDefaultsAndOverrides(t *testing.T) {
	clearProbeCredentials(t)
	resolved, err := resolveDeviceProbeProvider(probeRequest(t, "", probeOpenAIKey, ""))
	if err != nil {
		t.Fatalf("resolve default OpenAI probe: %v", err)
	}
	if resolved.provider != config.ProviderOpenAI || resolved.apiKey != probeOpenAIKey {
		t.Fatalf("default probe provider = %+v, want OpenAI with the explicit key", resolved)
	}
	if resolved.model != runtimeProviders.OpenAIRealtimeDefaultModel {
		t.Fatalf("default probe model = %q, want realtime default %q", resolved.model, runtimeProviders.OpenAIRealtimeDefaultModel)
	}
	custom, err := resolveDeviceProbeProvider(probeRequest(t, config.ProviderOpenAI, probeOpenAIKey, probeCustomModel))
	if err != nil {
		t.Fatalf("resolve explicit OpenAI probe: %v", err)
	}
	if custom.model != probeCustomModel {
		t.Fatalf("explicit probe model = %q, want %q", custom.model, probeCustomModel)
	}
}

func TestResolveDeviceProbeProviderMissingOpenAIKey(t *testing.T) {
	clearProbeCredentials(t)
	_, err := resolveDeviceProbeProvider(probeRequest(t, config.ProviderOpenAI, "", ""))
	if err == nil || !strings.Contains(err.Error(), "openai realtime api key is missing") {
		t.Fatalf("missing key error = %v, want the OpenAI realtime key diagnostic", err)
	}
}

func TestResolveDeviceProbeProviderGrok(t *testing.T) {
	clearProbeCredentials(t)
	resolved, err := resolveDeviceProbeProvider(probeRequest(t, config.ProviderGrok, probeGrokKey, probeGrokModel))
	if err != nil {
		t.Fatalf("resolve Grok probe: %v", err)
	}
	if resolved.provider != config.ProviderGrok || resolved.apiKey != probeGrokKey || resolved.model != probeGrokModel {
		t.Fatalf("Grok probe provider = %+v", resolved)
	}
	if _, err := resolveDeviceProbeProvider(probeRequest(t, config.ProviderGrok, "", probeGrokModel)); err == nil || !strings.Contains(err.Error(), "grok API key is required") {
		t.Fatalf("missing Grok key error = %v, want the Grok key diagnostic", err)
	}
}

func TestResolveDeviceProbeProviderRejectsUnsupportedProvider(t *testing.T) {
	clearProbeCredentials(t)
	if _, err := resolveDeviceProbeProvider(probeRequest(t, "claude", probeOpenAIKey, "")); err == nil || !strings.Contains(err.Error(), "supports realtime providers") {
		t.Fatalf("unsupported provider error = %v", err)
	}
}

type capturingProbeProviders struct {
	config runtimeProviders.SessionConfig
	err    error
}

func (p *capturingProbeProviders) BuildSession(_ context.Context, cfg runtimeProviders.SessionConfig) (messages.SessionInferencer, error) {
	p.config = cfg
	return nil, p.err
}

func TestDeviceProbeSessionFactoryBuildsPCM16Session(t *testing.T) {
	clearProbeCredentials(t)
	providers := &capturingProbeProviders{}
	factory := NewDeviceProbeSessionFactory(providers, audioiowire.NewService())
	_, model, err := factory(probeRequest(t, "", probeOpenAIKey, ""), "probe instructions")
	if err != nil {
		t.Fatalf("device probe factory: %v", err)
	}
	if model != runtimeProviders.OpenAIRealtimeDefaultModel {
		t.Fatalf("probe model = %q", model)
	}
	got := providers.config
	if got.Instructions != "probe instructions" || got.APIKey != probeOpenAIKey || got.InputAudioFormat != models.AudioFormatPCM16 ||
		got.OutputAudioFormat != models.AudioFormatPCM16 || got.InputAudioSampleRate != models.SampleRate24000 || got.OutputAudioSampleRate != models.SampleRate24000 {
		t.Fatalf("probe session config = %+v, want PCM16/24 kHz in both directions", got)
	}
	if got.InputTranscription == nil {
		t.Fatal("probe session config has no input transcription policy")
	}
	providers.err = errors.New("provider unavailable")
	if _, _, err := factory(probeRequest(t, "", probeOpenAIKey, ""), ""); !errors.Is(err, providers.err) {
		t.Fatalf("provider failure = %v, want %v", err, providers.err)
	}
	if _, _, err := NewDeviceProbeSessionFactory(nil, nil)(probeRequest(t, "", probeOpenAIKey, ""), ""); err == nil {
		t.Fatal("factory without services built a session")
	}
}
