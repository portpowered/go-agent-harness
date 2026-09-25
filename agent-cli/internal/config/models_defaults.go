package config

// Context window sizes (input + output tokens) of the built-in model registry.
const (
	contextWindow64Ki  = 65536
	contextWindow128K  = 128000
	contextWindow200K  = 200000
	contextWindow512Ki = 524288
	contextWindow1M    = 1000000
)

// defaultModelSets holds the modality and MIME type sets shared by the
// built-in model registry entries.
type defaultModelSets struct {
	text, textImage, textImageAudioVideo []string
	// Common MIME type sets for file upload validation.
	imageStandard, imageGemini, audioGemini, videoGemini, geminiAll []string
}

func newDefaultModelSets() defaultModelSets {
	imageGemini := []string{"image/png", "image/jpeg", "image/gif", "image/webp", "image/heic", "image/heif"}
	audioGemini := []string{"audio/mpeg", "audio/wav", "audio/ogg", "audio/flac", "audio/aac"}
	videoGemini := []string{"video/mp4", "video/mpeg", "video/webm", "video/quicktime"}
	return defaultModelSets{
		text:                []string{"text"},
		textImage:           []string{"text", "image"},
		textImageAudioVideo: []string{"text", "image", "audio", "video"},
		imageStandard:       []string{"image/png", "image/jpeg", "image/gif", "image/webp"},
		imageGemini:         imageGemini,
		audioGemini:         audioGemini,
		videoGemini:         videoGemini,
		geminiAll:           append(append(append([]string{}, imageGemini...), audioGemini...), videoGemini...),
	}
}

// defaultModelsConfig returns a pre-filled registry of well-known models.
func defaultModelsConfig() *ModelsConfig {
	sets := newDefaultModelSets()
	var models []ModelInfo
	models = append(models, defaultOpenAIModels(sets)...)
	models = append(models, defaultAnthropicModels(sets)...)
	models = append(models, defaultGoogleModels(sets)...)
	models = append(models, defaultMetaModels(sets)...)
	models = append(models, defaultDeepSeekModels(sets)...)
	models = append(models, defaultMistralModels(sets)...)
	models = append(models, defaultFalModels(sets)...)
	models = append(models, defaultZAIModels(sets)...)
	return &ModelsConfig{Models: models}
}

// defaultOpenAIModels lists the built-in OpenAI models.
func defaultOpenAIModels(sets defaultModelSets) []ModelInfo {
	return []ModelInfo{
		{
			Name:                    "gpt-4o",
			Aliases:                 []string{"gpt-4o-2024-11-20", "gpt-4o-2024-08-06"},
			Providers:               []string{"openai", "openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow128K,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "o200k_base",
			SupportedInputMimeTypes: sets.imageStandard,
		},
		{
			Name:                    "gpt-4o-mini",
			Aliases:                 []string{"gpt-4o-mini-2024-07-18"},
			Providers:               []string{"openai", "openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow128K,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "o200k_base",
			SupportedInputMimeTypes: sets.imageStandard,
		},
		{
			Name:                    "o1",
			Aliases:                 []string{"o1-2024-12-17"},
			Providers:               []string{"openai", "openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow200K,
			SupportsToolUse:         true,
			SupportsReasoning:       true,
			Tokenizer:               "o200k_base",
			SupportedInputMimeTypes: sets.imageStandard,
		},
		{
			Name:                    "o3",
			Providers:               []string{"openai", "openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow200K,
			SupportsToolUse:         true,
			SupportsReasoning:       true,
			Tokenizer:               "o200k_base",
			SupportedInputMimeTypes: sets.imageStandard,
		},
		{
			Name:              "o3-mini",
			Aliases:           []string{"o3-mini-2025-01-31"},
			Providers:         []string{"openai", "openrouter"},
			InputModalities:   sets.text,
			OutputModalities:  sets.text,
			MaxTokenCount:     contextWindow200K,
			SupportsToolUse:   true,
			SupportsReasoning: true,
			Tokenizer:         "o200k_base",
		},
		{
			Name:                    "o4-mini",
			Providers:               []string{"openai", "openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow200K,
			SupportsToolUse:         true,
			SupportsReasoning:       true,
			Tokenizer:               "o200k_base",
			SupportedInputMimeTypes: sets.imageStandard,
		},
	}
}

// defaultAnthropicModels lists the built-in Anthropic models.
func defaultAnthropicModels(sets defaultModelSets) []ModelInfo {
	return []ModelInfo{
		{
			Name:                    "claude-opus-4-6",
			Aliases:                 []string{"claude-opus-4-6-20251101"},
			Providers:               []string{"anthropic", "openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow200K,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "claude",
			SupportedInputMimeTypes: sets.imageStandard,
		},
		{
			Name:                    "claude-sonnet-4-6",
			Aliases:                 []string{"claude-sonnet-4-6-20251101"},
			Providers:               []string{"anthropic", "openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow200K,
			SupportsToolUse:         true,
			SupportsReasoning:       true,
			Tokenizer:               "claude",
			SupportedInputMimeTypes: sets.imageStandard,
		},
		{
			Name:                    "claude-haiku-4-5",
			Aliases:                 []string{"claude-haiku-4-5-20251001"},
			Providers:               []string{"anthropic", "openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow200K,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "claude",
			SupportedInputMimeTypes: sets.imageStandard,
		},
	}
}

// defaultGoogleModels lists the built-in Google models.
func defaultGoogleModels(sets defaultModelSets) []ModelInfo {
	return []ModelInfo{
		{
			Name:                    "google/gemini-2.5-pro-preview",
			Providers:               []string{"openrouter", "google"},
			InputModalities:         sets.textImageAudioVideo,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow1M,
			SupportsToolUse:         true,
			SupportsReasoning:       true,
			Tokenizer:               "sentencepiece",
			SupportedInputMimeTypes: sets.geminiAll,
		},
		{
			Name:                    "google/gemini-2.5-flash-preview",
			Providers:               []string{"openrouter", "google"},
			InputModalities:         sets.textImageAudioVideo,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow1M,
			SupportsToolUse:         true,
			SupportsReasoning:       true,
			Tokenizer:               "sentencepiece",
			SupportedInputMimeTypes: sets.geminiAll,
		},
		{
			Name:                    "google/gemini-2.0-flash",
			Aliases:                 []string{"google/gemini-2.0-flash-001"},
			Providers:               []string{"openrouter", "google"},
			InputModalities:         sets.textImageAudioVideo,
			OutputModalities:        sets.textImage,
			MaxTokenCount:           contextWindow1M,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "sentencepiece",
			SupportedInputMimeTypes: sets.geminiAll,
		},
		{
			Name:                    "google/gemini-2.0-flash-thinking-exp",
			Providers:               []string{"openrouter", "google"},
			InputModalities:         sets.textImageAudioVideo,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow1M,
			SupportsToolUse:         false,
			SupportsReasoning:       true,
			Tokenizer:               "sentencepiece",
			SupportedInputMimeTypes: sets.geminiAll,
		},
		{
			Name:                    "google/gemini-flash-1.5",
			Aliases:                 []string{"google/gemini-flash-1.5-8b"},
			Providers:               []string{"openrouter", "google"},
			InputModalities:         sets.textImageAudioVideo,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow1M,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "sentencepiece",
			SupportedInputMimeTypes: sets.geminiAll,
		},
	}
}

// defaultMetaModels lists the built-in Meta models.
func defaultMetaModels(sets defaultModelSets) []ModelInfo {
	return []ModelInfo{
		{
			Name:                    "meta-llama/llama-4-maverick",
			Providers:               []string{"openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow512Ki,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "llama",
			SupportedInputMimeTypes: sets.imageStandard,
		},
		{
			Name:                    "meta-llama/llama-4-scout",
			Providers:               []string{"openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow512Ki,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "llama",
			SupportedInputMimeTypes: sets.imageStandard,
		},
		{
			Name:              "meta-llama/llama-3.3-70b-instruct",
			Providers:         []string{"openrouter"},
			InputModalities:   sets.text,
			OutputModalities:  sets.text,
			MaxTokenCount:     contextWindow128K,
			SupportsToolUse:   true,
			SupportsReasoning: false,
			Tokenizer:         "llama",
		},
		{
			Name:              "meta-llama/llama-3.1-8b-instruct",
			Providers:         []string{"openrouter"},
			InputModalities:   sets.text,
			OutputModalities:  sets.text,
			MaxTokenCount:     contextWindow128K,
			SupportsToolUse:   true,
			SupportsReasoning: false,
			Tokenizer:         "llama",
		},
	}
}

// defaultDeepSeekModels lists the built-in DeepSeek models.
func defaultDeepSeekModels(sets defaultModelSets) []ModelInfo {
	return []ModelInfo{
		{
			Name:              "deepseek/deepseek-chat-v3-0324",
			Aliases:           []string{"deepseek/deepseek-chat"},
			Providers:         []string{"openrouter"},
			InputModalities:   sets.text,
			OutputModalities:  sets.text,
			MaxTokenCount:     contextWindow64Ki,
			SupportsToolUse:   true,
			SupportsReasoning: false,
			Tokenizer:         "deepseek",
		},
		{
			Name:              "deepseek/deepseek-r1",
			Providers:         []string{"openrouter"},
			InputModalities:   sets.text,
			OutputModalities:  sets.text,
			MaxTokenCount:     contextWindow128K,
			SupportsToolUse:   true,
			SupportsReasoning: true,
			Tokenizer:         "deepseek",
		},
	}
}

// defaultMistralModels lists the built-in Mistral models.
func defaultMistralModels(sets defaultModelSets) []ModelInfo {
	return []ModelInfo{
		{
			Name:              "mistralai/mistral-large",
			Providers:         []string{"openrouter"},
			InputModalities:   sets.text,
			OutputModalities:  sets.text,
			MaxTokenCount:     contextWindow128K,
			SupportsToolUse:   true,
			SupportsReasoning: false,
			Tokenizer:         "sentencepiece",
		},
		{
			Name:                    "mistralai/mistral-small-3.1-24b-instruct",
			Aliases:                 []string{"mistralai/mistral-small"},
			Providers:               []string{"openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow128K,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			Tokenizer:               "sentencepiece",
			SupportedInputMimeTypes: sets.imageStandard,
		},
	}
}

// defaultFalModels lists the built-in fal.ai models.
func defaultFalModels(_ defaultModelSets) []ModelInfo {
	return []ModelInfo{
		{
			Name:              "fal-ai/qwen-3-tts/text-to-speech/1.7b",
			Providers:         []string{"fal"},
			InputModalities:   []string{"embedding", "text"},
			OutputModalities:  []string{"audio"},
			MaxTokenCount:     0,
			SupportsToolUse:   false,
			SupportsReasoning: false,
		},
	}
}

// defaultZAIModels lists the built-in Z AI models.
func defaultZAIModels(sets defaultModelSets) []ModelInfo {
	return []ModelInfo{
		{
			Name:                    "z-ai/glm-4.7",
			Providers:               []string{"openrouter"},
			InputModalities:         sets.textImage,
			OutputModalities:        sets.text,
			MaxTokenCount:           contextWindow128K,
			SupportsToolUse:         true,
			SupportsReasoning:       false,
			SupportedInputMimeTypes: sets.imageStandard,
		},
	}
}
