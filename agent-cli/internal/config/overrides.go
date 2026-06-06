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

// ApplyOverrides returns a copy of cfg with CLI flag overrides applied.
// Empty strings mean no override. Used by ask/chat commands so that
// --api-key, --model, --provider, --base-url override config file values.
func (c Config) ApplyOverrides(apiKey, model, provider, baseURL string) Config {
	out := c

	// Determine effective provider (switch if --provider set)
	effProvider := c.Model.Provider
	if provider != "" {
		effProvider = provider
		out.Model.Provider = effProvider
	}

	// Ensure we have the right provider config struct and apply overrides.
	// Copy structs to avoid mutating cached config.
	switch effProvider {
	case ProviderOpenAI:
		cfg := copyOpenAIConfig(out.Model.OpenAI)
		if cfg == nil {
			cfg = &OpenAIConfig{Model: DefaultModelModel}
		}
		if apiKey != "" {
			cfg.APIKey = apiKey
		}
		if model != "" {
			cfg.Model = model
		}
		if baseURL != "" {
			cfg.BaseURL = baseURL
		}
		out.Model.OpenAI = cfg
	case ProviderOpenRouter:
		cfg := copyOpenAIConfig(out.Model.OpenRouter)
		if cfg == nil {
			cfg = &OpenAIConfig{Model: DefaultModelModel}
		}
		if apiKey != "" {
			cfg.APIKey = apiKey
		}
		if model != "" {
			cfg.Model = model
		}
		if baseURL != "" {
			cfg.BaseURL = baseURL
		}
		out.Model.OpenRouter = cfg
	case ProviderLocal:
		cfg := copyOpenAIConfig(out.Model.Local)
		if cfg == nil {
			cfg = &OpenAIConfig{Model: DefaultModelModel}
		}
		if apiKey != "" {
			cfg.APIKey = apiKey
		}
		if model != "" {
			cfg.Model = model
		}
		if baseURL != "" {
			cfg.BaseURL = baseURL
		}
		out.Model.Local = cfg
	case ProviderFal:
		cfg := copyFalConfig(out.Model.Fal)
		if cfg == nil {
			cfg = &FalConfig{}
		}
		if apiKey != "" {
			cfg.APIKey = apiKey
		}
		if model != "" {
			cfg.Model = model
		}
		if baseURL != "" {
			cfg.BaseURL = baseURL
		}
		out.Model.Fal = cfg
	case ProviderGrok:
		cfg := copyGrokConfig(out.Model.Grok)
		if cfg == nil {
			cfg = &GrokConfig{}
		}
		if apiKey != "" {
			cfg.APIKey = apiKey
		}
		if model != "" {
			cfg.Model = model
		}
		if baseURL != "" {
			cfg.BaseURL = baseURL
		}
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
