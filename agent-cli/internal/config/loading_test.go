package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleYAML = `
model:
  provider: openrouter
  openai:
    model: gpt-4
    api_key: file-openai-key
    base_url: https://api.openai.com/v1
  openrouter:
    model: custom-model-v1
    api_key: file-openrouter-key
    base_url: https://openrouter.example.com/v1
  claude: {}
`

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(sampleYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	storage := NewConfigStorage(configPath)
	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Model.Provider != "openrouter" {
		t.Errorf("Model.Provider: got %q, want openrouter", cfg.Model.Provider)
	}
	if cfg.Model.OpenRouter == nil {
		t.Fatal("Model.OpenRouter: expected non-nil")
	}
	if cfg.Model.OpenRouter.Model != "custom-model-v1" {
		t.Errorf("Model.OpenRouter.Model: got %q, want custom-model-v1", cfg.Model.OpenRouter.Model)
	}
	if cfg.Model.OpenRouter.APIKey != "file-openrouter-key" {
		t.Errorf("Model.OpenRouter.APIKey: got %q, want file-openrouter-key", cfg.Model.OpenRouter.APIKey)
	}
	if cfg.Model.OpenRouter.BaseURL != "https://openrouter.example.com/v1" {
		t.Errorf("Model.OpenRouter.BaseURL: got %q", cfg.Model.OpenRouter.BaseURL)
	}
	if cfg.Model.OpenAI == nil {
		t.Fatal("Model.OpenAI: expected non-nil")
	}
	if cfg.Model.OpenAI.Model != "gpt-4" {
		t.Errorf("Model.OpenAI.Model: got %q, want gpt-4", cfg.Model.OpenAI.Model)
	}
}

func TestLoad_BasePathOverride(t *testing.T) {
	customDir := t.TempDir()
	configPath := filepath.Join(customDir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(sampleYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	storage, err := NewDefaultConfigStorage(customDir)
	if err != nil {
		t.Fatalf("NewDefaultConfigStorage: %v", err)
	}

	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Model.Provider != "openrouter" {
		t.Errorf("Model.Provider: got %q, want openrouter", cfg.Model.Provider)
	}
	if cfg.Model.OpenRouter == nil || cfg.Model.OpenRouter.APIKey != "file-openrouter-key" {
		t.Errorf("expected openrouter config from file, got %+v", cfg.Model.OpenRouter)
	}
}

func TestLoad_EnvOverrides_ProviderAndOpenAI(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(sampleYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	// Override provider and openai block via environment (use __ for each nesting level)
	setEnv(t, "AGENT_MODEL__PROVIDER", "openai")
	setEnv(t, "AGENT_MODEL__OPENAI__MODEL", "env-model")
	setEnv(t, "AGENT_MODEL__OPENAI__API_KEY", "env-api-key")
	setEnv(t, "AGENT_MODEL__OPENAI__BASE_URL", "https://env.openai.example.com")
	defer unsetEnv(t, "AGENT_MODEL__PROVIDER", "AGENT_MODEL__OPENAI__MODEL", "AGENT_MODEL__OPENAI__API_KEY", "AGENT_MODEL__OPENAI__BASE_URL")

	storage := NewConfigStorage(configPath)
	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Model.Provider != "openai" {
		t.Errorf("Model.Provider (env override): got %q, want openai", cfg.Model.Provider)
	}
	if cfg.Model.OpenAI == nil {
		t.Fatal("Model.OpenAI: expected non-nil")
	}
	if cfg.Model.OpenAI.Model != "env-model" {
		t.Errorf("Model.OpenAI.Model (env override): got %q, want env-model", cfg.Model.OpenAI.Model)
	}
	if cfg.Model.OpenAI.APIKey != "env-api-key" {
		t.Errorf("Model.OpenAI.APIKey (env override): got %q, want env-api-key", cfg.Model.OpenAI.APIKey)
	}
	if cfg.Model.OpenAI.BaseURL != "https://env.openai.example.com" {
		t.Errorf("Model.OpenAI.BaseURL (env override): got %q", cfg.Model.OpenAI.BaseURL)
	}
}

func TestLoad_EnvOverrides_OpenRouter(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(sampleYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	setEnv(t, "AGENT_MODEL__PROVIDER", "openrouter")
	setEnv(t, "AGENT_MODEL__OPENROUTER__MODEL", "env-openrouter-model")
	setEnv(t, "AGENT_MODEL__OPENROUTER__API_KEY", "env-openrouter-key")
	setEnv(t, "AGENT_MODEL__OPENROUTER__BASE_URL", "https://env.openrouter.example.com")
	defer unsetEnv(t, "AGENT_MODEL__PROVIDER", "AGENT_MODEL__OPENROUTER__MODEL", "AGENT_MODEL__OPENROUTER__API_KEY", "AGENT_MODEL__OPENROUTER__BASE_URL")

	storage := NewConfigStorage(configPath)
	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Model.Provider != "openrouter" {
		t.Errorf("Model.Provider: got %q, want openrouter", cfg.Model.Provider)
	}
	if cfg.Model.OpenRouter == nil {
		t.Fatal("Model.OpenRouter: expected non-nil")
	}
	if cfg.Model.OpenRouter.Model != "env-openrouter-model" {
		t.Errorf("Model.OpenRouter.Model (env override): got %q, want env-openrouter-model", cfg.Model.OpenRouter.Model)
	}
	if cfg.Model.OpenRouter.APIKey != "env-openrouter-key" {
		t.Errorf("Model.OpenRouter.APIKey (env override): got %q, want env-openrouter-key", cfg.Model.OpenRouter.APIKey)
	}
	if cfg.Model.OpenRouter.BaseURL != "https://env.openrouter.example.com" {
		t.Errorf("Model.OpenRouter.BaseURL (env override): got %q", cfg.Model.OpenRouter.BaseURL)
	}
}

func TestLoad_GrokSessionProvider_FromFile(t *testing.T) {
	const grokYAML = `
model:
  provider: grok
  grok:
    model: grok-2-vision-1212
    api_key: file-grok-key
    base_url: wss://grok.example.test/realtime
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(grokYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	storage := NewConfigStorage(configPath)
	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Model.Provider != ProviderGrok {
		t.Errorf("Model.Provider: got %q, want %s", cfg.Model.Provider, ProviderGrok)
	}
	if cfg.Model.Grok == nil {
		t.Fatal("Model.Grok: expected non-nil")
	}
	if cfg.Model.Grok.Model != "grok-2-vision-1212" {
		t.Errorf("Model.Grok.Model: got %q", cfg.Model.Grok.Model)
	}
	if cfg.Model.Grok.APIKey != "file-grok-key" {
		t.Errorf("Model.Grok.APIKey: got %q", cfg.Model.Grok.APIKey)
	}
	if cfg.Model.Grok.BaseURL != "wss://grok.example.test/realtime" {
		t.Errorf("Model.Grok.BaseURL: got %q", cfg.Model.Grok.BaseURL)
	}
}

func TestLoad_SessionConfigAndEffectivePath(t *testing.T) {
	const sessionYAML = `
session:
  provider: openai
  model: gpt-realtime-2.1-mini
  transport: ws
  input_device: virtual:mic
  output_device: virtual:speakers
  vad:
    enabled: true
    type: semantic_vad
    eagerness: low
    create_response: false
    interrupt_response: true
  input_transcription:
    enabled: false
    model: custom-transcriber
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(sessionYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	storage := NewConfigStorage(configPath)
	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.ConfigPath != configPath || storage.Path() != configPath {
		t.Fatalf("config path = %q / storage path = %q, want %q", cfg.ConfigPath, storage.Path(), configPath)
	}
	if cfg.Session == nil {
		t.Fatal("Session: expected persisted session config")
	}
	if cfg.Session.Provider != ProviderOpenAI || cfg.Session.Model != "gpt-realtime-2.1-mini" || cfg.Session.InputDevice != "virtual:mic" || cfg.Session.OutputDevice != "virtual:speakers" {
		t.Fatalf("Session identity = %#v", cfg.Session)
	}
	if cfg.Session.VAD == nil || cfg.Session.VAD.Enabled == nil || !*cfg.Session.VAD.Enabled || cfg.Session.VAD.Type != "semantic_vad" || cfg.Session.VAD.Eagerness != "low" || cfg.Session.VAD.CreateResponse == nil || *cfg.Session.VAD.CreateResponse || cfg.Session.VAD.InterruptResponse == nil || !*cfg.Session.VAD.InterruptResponse {
		t.Fatalf("Session.VAD = %#v, want decoded policy", cfg.Session.VAD)
	}
	if cfg.Session.InputTranscription == nil || cfg.Session.InputTranscription.Enabled == nil || *cfg.Session.InputTranscription.Enabled || cfg.Session.InputTranscription.Model != "custom-transcriber" {
		t.Fatalf("Session.InputTranscription = %#v, want decoded policy", cfg.Session.InputTranscription)
	}
}

func TestLoad_EnvOverrides_SessionDefaults(t *testing.T) {
	const sessionYAML = `
session:
  provider: grok
  model: file-session-model
  vad:
    enabled: true
  input_transcription:
    enabled: false
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(sessionYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	setEnv(t, "AGENT_SESSION__PROVIDER", ProviderOpenAI)
	setEnv(t, "AGENT_SESSION__MODEL", "env-session-model")
	setEnv(t, "AGENT_SESSION__VAD__ENABLED", "false")
	setEnv(t, "AGENT_SESSION__INPUT_TRANSCRIPTION__ENABLED", "true")
	defer unsetEnv(t, "AGENT_SESSION__PROVIDER", "AGENT_SESSION__MODEL", "AGENT_SESSION__VAD__ENABLED", "AGENT_SESSION__INPUT_TRANSCRIPTION__ENABLED")

	cfg, err := NewConfigStorage(configPath).Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Session == nil || cfg.Session.Provider != ProviderOpenAI || cfg.Session.Model != "env-session-model" {
		t.Fatalf("Session identity = %#v, want environment values", cfg.Session)
	}
	if cfg.Session.VAD == nil || cfg.Session.VAD.Enabled == nil || *cfg.Session.VAD.Enabled {
		t.Fatalf("Session.VAD = %#v, want environment disabled value", cfg.Session.VAD)
	}
	if cfg.Session.InputTranscription == nil || cfg.Session.InputTranscription.Enabled == nil || !*cfg.Session.InputTranscription.Enabled {
		t.Fatalf("Session.InputTranscription = %#v, want environment enabled value", cfg.Session.InputTranscription)
	}
}

func TestLoad_EnvOverrides_Grok(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(sampleYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	setEnv(t, "AGENT_MODEL__PROVIDER", ProviderGrok)
	setEnv(t, "AGENT_MODEL__GROK__MODEL", "env-grok-model")
	setEnv(t, "AGENT_MODEL__GROK__API_KEY", "env-grok-key")
	setEnv(t, "AGENT_MODEL__GROK__BASE_URL", "wss://env.grok.example.test/realtime")
	defer unsetEnv(t, "AGENT_MODEL__PROVIDER", "AGENT_MODEL__GROK__MODEL", "AGENT_MODEL__GROK__API_KEY", "AGENT_MODEL__GROK__BASE_URL")

	storage := NewConfigStorage(configPath)
	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Model.Provider != ProviderGrok {
		t.Errorf("Model.Provider: got %q, want %s", cfg.Model.Provider, ProviderGrok)
	}
	if cfg.Model.Grok == nil {
		t.Fatal("Model.Grok: expected non-nil")
	}
	if cfg.Model.Grok.Model != "env-grok-model" {
		t.Errorf("Model.Grok.Model: got %q", cfg.Model.Grok.Model)
	}
	if cfg.Model.Grok.APIKey != "env-grok-key" {
		t.Errorf("Model.Grok.APIKey: got %q", cfg.Model.Grok.APIKey)
	}
	if cfg.Model.Grok.BaseURL != "wss://env.grok.example.test/realtime" {
		t.Errorf("Model.Grok.BaseURL: got %q", cfg.Model.Grok.BaseURL)
	}
}

func TestLoad_NoFile_UsesDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "nonexistent.yaml")
	storage := NewConfigStorage(configPath)

	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Model.Provider != DefaultModelProvider {
		t.Errorf("Model.Provider: got %q, want %q", cfg.Model.Provider, DefaultModelProvider)
	}
	if cfg.Model.OpenRouter == nil {
		t.Fatal("Model.OpenRouter: expected non-nil default")
	}
	if cfg.Model.OpenRouter.Model != DefaultModelModel {
		t.Errorf("Model.OpenRouter.Model: got %q, want %q", cfg.Model.OpenRouter.Model, DefaultModelModel)
	}
}

func TestLoad_Cached(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(sampleYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	storage := NewConfigStorage(configPath)
	cfg1, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	cfg2, err := storage.Load()
	if err != nil {
		t.Fatalf("Load() second call: %v", err)
	}

	if cfg1 != cfg2 {
		t.Error("Load() should return cached config (same pointer)")
	}
}

func TestNewDefaultConfigStorage_EmptyUsesHome(t *testing.T) {
	storage, err := NewDefaultConfigStorage("")
	if err != nil {
		t.Fatalf("NewDefaultConfigStorage: %v", err)
	}
	if storage == nil {
		t.Fatal("expected non-nil ConfigStorage")
	}
}

func TestValidate_RequiresAPIKey(t *testing.T) {
	c := Config{Model: ModelConfig{Provider: "openai"}}
	if err := c.Validate(); err == nil {
		t.Error("Validate() with openai but no openai config should error")
	}

	c.Model.OpenAI = &OpenAIConfig{}
	if err := c.Validate(); err == nil {
		t.Error("Validate() with empty API key should error")
	}

	c.Model.OpenAI.APIKey = "sk-xxx"
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() with API key set: %v", err)
	}
}

func TestValidate_OpenRouter(t *testing.T) {
	c := Config{Model: ModelConfig{Provider: "openrouter"}}
	if err := c.Validate(); err == nil {
		t.Error("Validate() with openrouter but no openrouter config should error")
	}

	c.Model.OpenRouter = &OpenAIConfig{APIKey: "sk-xxx"}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() with openrouter API key: %v", err)
	}
}

func TestValidate_GrokIsSessionOnlyForOneShot(t *testing.T) {
	c := Config{
		Model: ModelConfig{
			Provider: ProviderGrok,
			Grok:     &GrokConfig{Model: "grok-session-model", APIKey: "xai-key"},
		},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() with grok should error for one-shot inference")
	} else if !strings.Contains(err.Error(), "session-only") {
		t.Fatalf("Validate() grok error should explain session-only provider, got: %v", err)
	}
}

func TestValidateGrokSession_RequiresLiveCredentials(t *testing.T) {
	c := Config{Model: ModelConfig{Provider: ProviderGrok, Grok: &GrokConfig{Model: "grok-session-model"}}}
	if err := c.ValidateGrokSession(); err == nil {
		t.Fatal("ValidateGrokSession() without API key should error")
	}

	c.Model.Grok.APIKey = "xai-key"
	if err := c.ValidateGrokSession(); err != nil {
		t.Fatalf("ValidateGrokSession() with model and API key: %v", err)
	}
}

func TestActiveOpenAIConfig_OpenRouter(t *testing.T) {
	cfg := Config{
		Model: ModelConfig{
			Provider:   "openrouter",
			OpenRouter: &OpenAIConfig{Model: "z-ai/glm-4.7", APIKey: "sk-xxx"},
		},
	}
	active, err := cfg.ActiveOpenAIConfig()
	if err != nil {
		t.Fatalf("ActiveOpenAIConfig: %v", err)
	}
	if active.Model != "z-ai/glm-4.7" || active.APIKey != "sk-xxx" {
		t.Errorf("ActiveOpenAIConfig: got %+v", active)
	}
}

func TestActiveOpenAIConfig_OpenAI(t *testing.T) {
	cfg := Config{
		Model: ModelConfig{
			Provider: "openai",
			OpenAI:   &OpenAIConfig{Model: "gpt-4", APIKey: "sk-xxx"},
		},
	}
	active, err := cfg.ActiveOpenAIConfig()
	if err != nil {
		t.Fatalf("ActiveOpenAIConfig: %v", err)
	}
	if active.Model != "gpt-4" || active.APIKey != "sk-xxx" {
		t.Errorf("ActiveOpenAIConfig: got %+v", active)
	}
}

func TestActiveOpenAIConfig_Local(t *testing.T) {
	cfg := Config{
		Model: ModelConfig{
			Provider: "local",
			Local:    &OpenAIConfig{Model: "llama3", BaseURL: "http://localhost:11434/v1"},
		},
	}
	active, err := cfg.ActiveOpenAIConfig()
	if err != nil {
		t.Fatalf("ActiveOpenAIConfig: %v", err)
	}
	if active.Model != "llama3" || active.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("ActiveOpenAIConfig: got %+v", active)
	}
}

func TestActiveOpenAIConfig_Local_NilConfig(t *testing.T) {
	cfg := Config{Model: ModelConfig{Provider: "local"}}
	_, err := cfg.ActiveOpenAIConfig()
	if err == nil {
		t.Error("ActiveOpenAIConfig(local) with nil config should error")
	}
}

func TestValidate_Local_NoAPIKeyRequired(t *testing.T) {
	c := Config{
		Model: ModelConfig{
			Provider: "local",
			Local:    &OpenAIConfig{Model: "llama3", BaseURL: "http://localhost:11434/v1"},
		},
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() local provider without API key should pass: %v", err)
	}
}

func TestValidate_Local_WithAPIKeyAlsoValid(t *testing.T) {
	c := Config{
		Model: ModelConfig{
			Provider: "local",
			Local:    &OpenAIConfig{Model: "llama3", BaseURL: "http://localhost:11434/v1", APIKey: "optional-key"},
		},
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() local provider with API key should pass: %v", err)
	}
}

func TestActiveOpenAIConfig_UnsupportedProvider(t *testing.T) {
	cfg := Config{Model: ModelConfig{Provider: "claude"}}
	_, err := cfg.ActiveOpenAIConfig()
	if err == nil {
		t.Error("ActiveOpenAIConfig(claude) should error")
	}
}

func TestToolsConfig_ToolEnabled(t *testing.T) {
	// Empty list: all enabled
	var empty ToolsConfig
	if !empty.ToolEnabled("exec") || !empty.ToolEnabled("read_file") {
		t.Error("empty list should enable all tools")
	}
	// List with one disabled
	cfg := ToolsConfig{
		List: []ToolEntry{
			{ID: "exec", Enabled: false},
			{ID: "read_file", Enabled: true},
		},
	}
	if cfg.ToolEnabled("exec") {
		t.Error("exec should be disabled")
	}
	if !cfg.ToolEnabled("read_file") {
		t.Error("read_file should be enabled")
	}
	if !cfg.ToolEnabled("mouse") {
		t.Error("tool not in list should be enabled by default")
	}
}

func TestDefaultToolsList_ContainsAllToolIDs(t *testing.T) {
	list := DefaultToolsList()
	if len(list) != len(DefaultToolIDs) {
		t.Errorf("DefaultToolsList length %d, want %d", len(list), len(DefaultToolIDs))
	}
	ids := make(map[string]bool)
	for _, e := range list {
		if !e.Enabled {
			t.Errorf("default tool %q should be enabled", e.ID)
		}
		ids[e.ID] = true
	}
	for _, id := range DefaultToolIDs {
		if !ids[id] {
			t.Errorf("DefaultToolIDs contains %q but DefaultToolsList does not", id)
		}
	}
}

func TestLoad_LocalProvider_FromFile(t *testing.T) {
	const localYAML = `
model:
  provider: local
  local:
    model: llama3
    base_url: http://localhost:11434/v1
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(configPath, []byte(localYAML), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	storage := NewConfigStorage(configPath)
	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Model.Provider != "local" {
		t.Errorf("Model.Provider: got %q, want local", cfg.Model.Provider)
	}
	if cfg.Model.Local == nil {
		t.Fatal("Model.Local: expected non-nil")
	}
	if cfg.Model.Local.Model != "llama3" {
		t.Errorf("Model.Local.Model: got %q, want llama3", cfg.Model.Local.Model)
	}
	if cfg.Model.Local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("Model.Local.BaseURL: got %q", cfg.Model.Local.BaseURL)
	}
	if cfg.Model.Local.APIKey != "" {
		t.Errorf("Model.Local.APIKey: got %q, want empty", cfg.Model.Local.APIKey)
	}

	// Validate should pass without API key
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() local provider: %v", err)
	}
}

func TestValidate_Local_RequiresBaseURL(t *testing.T) {
	c := Config{
		Model: ModelConfig{
			Provider: "local",
			Local:    &OpenAIConfig{Model: "llama3"},
		},
	}
	if err := c.Validate(); err == nil {
		t.Error("Validate() local provider without base URL should error")
	}
}

func TestApplyOverrides_LocalProvider(t *testing.T) {
	base := Config{
		Model: ModelConfig{
			Provider:   "openrouter",
			OpenRouter: &OpenAIConfig{Model: "z-ai/glm-4.7", APIKey: "sk-xxx"},
		},
	}
	out := base.ApplyOverrides("", "llama3", "local", "http://localhost:11434/v1")
	if out.Model.Provider != "local" {
		t.Errorf("Provider: got %q, want local", out.Model.Provider)
	}
	if out.Model.Local == nil {
		t.Fatal("Model.Local: expected non-nil")
	}
	if out.Model.Local.Model != "llama3" {
		t.Errorf("Model: got %q, want llama3", out.Model.Local.Model)
	}
	if out.Model.Local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL: got %q", out.Model.Local.BaseURL)
	}
	if out.Model.Local.APIKey != "" {
		t.Errorf("APIKey: got %q, want empty", out.Model.Local.APIKey)
	}
}

func TestApplyOverrides_GrokProvider(t *testing.T) {
	base := Config{
		Model: ModelConfig{
			Provider:   ProviderOpenRouter,
			OpenRouter: &OpenAIConfig{Model: "z-ai/glm-4.7", APIKey: "sk-xxx"},
		},
	}
	out := base.ApplyOverrides("xai-key", "grok-session-model", ProviderGrok, "wss://grok.example.test/realtime")
	if out.Model.Provider != ProviderGrok {
		t.Errorf("Provider: got %q, want %s", out.Model.Provider, ProviderGrok)
	}
	if out.Model.Grok == nil {
		t.Fatal("Model.Grok: expected non-nil")
	}
	if out.Model.Grok.Model != "grok-session-model" {
		t.Errorf("Model: got %q", out.Model.Grok.Model)
	}
	if out.Model.Grok.APIKey != "xai-key" {
		t.Errorf("APIKey: got %q", out.Model.Grok.APIKey)
	}
	if out.Model.Grok.BaseURL != "wss://grok.example.test/realtime" {
		t.Errorf("BaseURL: got %q", out.Model.Grok.BaseURL)
	}
}

func TestApplyOverrides_LocalProvider_PreservesExisting(t *testing.T) {
	base := Config{
		Model: ModelConfig{
			Provider: "local",
			Local:    &OpenAIConfig{Model: "llama3", BaseURL: "http://localhost:11434/v1"},
		},
	}
	// Override only model
	out := base.ApplyOverrides("", "mistral", "", "")
	if out.Model.Local.Model != "mistral" {
		t.Errorf("Model: got %q, want mistral", out.Model.Local.Model)
	}
	if out.Model.Local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL should be preserved: got %q", out.Model.Local.BaseURL)
	}
}

func TestApplyOverrides_BaseURL_OnOpenAI(t *testing.T) {
	base := Config{
		Model: ModelConfig{
			Provider: "openai",
			OpenAI:   &OpenAIConfig{Model: "gpt-4", APIKey: "sk-xxx"},
		},
	}
	out := base.ApplyOverrides("", "", "", "http://custom-endpoint/v1")
	if out.Model.OpenAI.BaseURL != "http://custom-endpoint/v1" {
		t.Errorf("BaseURL: got %q", out.Model.OpenAI.BaseURL)
	}
}

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("Setenv(%q): %v", key, err)
	}
}

func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("Unsetenv(%q): %v", k, err)
		}
	}
}
