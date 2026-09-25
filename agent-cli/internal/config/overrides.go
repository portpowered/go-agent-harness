package config

// copyOpenAIConfig returns a copy of o, or nil if o is nil.
func copyOpenAIConfig(o *OpenAIConfig) *OpenAIConfig {
	if o == nil {
		return nil
	}
	return &OpenAIConfig{
		Model:   o.Model,
		APIKey:  o.APIKey,
		BaseURL: o.BaseURL,
	}
}

// copyGrokConfig returns a copy of g, or nil if g is nil.
func copyGrokConfig(g *GrokConfig) *GrokConfig {
	if g == nil {
		return nil
	}
	return &GrokConfig{
		Model:   g.Model,
		APIKey:  g.APIKey,
		BaseURL: g.BaseURL,
	}
}

func copySessionConfig(s *SessionConfig) *SessionConfig {
	if s == nil {
		return nil
	}
	out := *s
	if s.VAD != nil {
		vad := *s.VAD
		if s.VAD.Enabled != nil {
			enabled := *s.VAD.Enabled
			vad.Enabled = &enabled
		}
		if s.VAD.CreateResponse != nil {
			createResponse := *s.VAD.CreateResponse
			vad.CreateResponse = &createResponse
		}
		out.VAD = &vad
	}
	if s.InputTranscription != nil {
		transcription := *s.InputTranscription
		if s.InputTranscription.Enabled != nil {
			enabled := *s.InputTranscription.Enabled
			transcription.Enabled = &enabled
		}
		out.InputTranscription = &transcription
	}
	return &out
}

// ApplyOverrides returns a copy of cfg with CLI flag overrides applied.
// Empty strings mean no override. Used by ask/chat commands so that
// --api-key, --model, --provider, --base-url override config file values.
func (c Config) ApplyOverrides(apiKey, model, provider, baseURL string) Config {
	out := c
	out.Session = copySessionConfig(c.Session)

	// Determine effective provider (switch if --provider set)
	effProvider := c.Model.Provider
	if provider != "" {
		effProvider = provider
		out.Model.Provider = effProvider
	}

	// Ensure we have the right provider config struct and apply overrides.
	// Copy structs to avoid mutating cached config.
	endpoint := endpointOverrides{apiKey: apiKey, model: model, baseURL: baseURL}
	switch effProvider {
	case ProviderOpenAI:
		out.Model.OpenAI = overrideOpenAICompatibleConfig(out.Model.OpenAI, endpoint)
	case ProviderOpenRouter:
		out.Model.OpenRouter = overrideOpenAICompatibleConfig(out.Model.OpenRouter, endpoint)
	case ProviderLocal:
		out.Model.Local = overrideOpenAICompatibleConfig(out.Model.Local, endpoint)
	case ProviderFal:
		cfg := copyFalConfig(out.Model.Fal)
		if cfg == nil {
			cfg = &FalConfig{}
		}
		endpoint.apply(&cfg.APIKey, &cfg.Model, &cfg.BaseURL)
		out.Model.Fal = cfg
	case ProviderGrok:
		cfg := copyGrokConfig(out.Model.Grok)
		if cfg == nil {
			cfg = &GrokConfig{}
		}
		endpoint.apply(&cfg.APIKey, &cfg.Model, &cfg.BaseURL)
		out.Model.Grok = cfg
	}

	return out
}

// copyFalConfig returns a copy of f, or nil if f is nil.
func copyFalConfig(f *FalConfig) *FalConfig {
	if f == nil {
		return nil
	}
	return &FalConfig{
		Model:   f.Model,
		APIKey:  f.APIKey,
		BaseURL: f.BaseURL,
	}
}

// endpointOverrides holds CLI endpoint flags; empty values mean no override.
type endpointOverrides struct {
	apiKey, model, baseURL string
}

func (o endpointOverrides) apply(apiKey, model, baseURL *string) {
	overrideNonEmpty(apiKey, o.apiKey)
	overrideNonEmpty(model, o.model)
	overrideNonEmpty(baseURL, o.baseURL)
}

func overrideNonEmpty(dst *string, value string) {
	if value != "" {
		*dst = value
	}
}

// overrideOpenAICompatibleConfig copies cfg (defaulting the model when absent)
// and applies the endpoint overrides to the copy.
func overrideOpenAICompatibleConfig(cfg *OpenAIConfig, endpoint endpointOverrides) *OpenAIConfig {
	out := copyOpenAIConfig(cfg)
	if out == nil {
		out = &OpenAIConfig{Model: DefaultModelModel}
	}
	endpoint.apply(&out.APIKey, &out.Model, &out.BaseURL)
	return out
}
