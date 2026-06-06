package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	yamlv3 "gopkg.in/yaml.v3"
)

const ModelsFileName = "models.yaml"

// ModelInfo describes the capabilities and configuration of a single model.
type ModelInfo struct {
	// Name is the canonical model identifier (e.g. "gpt-4o", "google/gemini-2.0-flash").
	Name string `yaml:"name"`
	// Aliases lists alternative identifiers for this model (e.g. versioned names).
	Aliases []string `yaml:"aliases,omitempty"`
	// Providers lists the provider IDs that can serve this model.
	Providers []string `yaml:"providers"`
	// InputModalities lists the media types accepted as input: "text", "image", "audio", "video".
	InputModalities []string `yaml:"input_modalities"`
	// OutputModalities lists the media types the model can produce: "text", "image", "audio", "video".
	OutputModalities []string `yaml:"output_modalities"`
	// MaxTokenCount is the model's context window size (input + output tokens combined).
	MaxTokenCount int `yaml:"max_token_count"`
	// SupportsToolUse indicates whether the model supports function/tool calling.
	SupportsToolUse bool `yaml:"supports_tool_use"`
	// SupportsReasoning indicates whether the model supports extended reasoning / chain-of-thought tokens.
	SupportsReasoning bool `yaml:"supports_reasoning"`
	// Tokenizer is the canonical tokenizer name used for local token counting (e.g. "o200k_base", "cl100k_base").
	Tokenizer string `yaml:"tokenizer,omitempty"`
	// SupportedInputMimeTypes lists the specific MIME types accepted for file uploads (e.g. "image/png", "image/jpeg").
	// When nil or empty, all file types are accepted (backward compatible).
	SupportedInputMimeTypes []string `yaml:"supportedInputMimeTypes,omitempty"`
}

// SupportsOutputModality returns true if the model can produce the given modality (e.g. "image", "audio").
func (m *ModelInfo) SupportsOutputModality(modality string) bool {
	return slices.Contains(m.OutputModalities, modality)
}

// SupportsInputMimeType returns true if the model accepts the given MIME type for file uploads.
// When SupportedInputMimeTypes is nil or empty, all types are accepted (backward compatible).
func (m *ModelInfo) SupportsInputMimeType(mimeType string) bool {
	if len(m.SupportedInputMimeTypes) == 0 {
		return true
	}
	return slices.Contains(m.SupportedInputMimeTypes, mimeType)
}

// ModelsConfig holds the registry of known models.
type ModelsConfig struct {
	Models []ModelInfo `yaml:"models"`
}

// Lookup returns the ModelInfo matching name (by canonical name or alias), or nil if not found.
func (c *ModelsConfig) Lookup(name string) *ModelInfo {
	for i := range c.Models {
		m := &c.Models[i]
		if m.Name == name {
			return m
		}
		if slices.Contains(m.Aliases, name) {
			return m
		}
	}
	return nil
}

// ModelsConfigStorage handles loading and creation of models.yaml.
type ModelsConfigStorage struct {
	path string
}

// NewModelsConfigStorage creates a storage handler for models.yaml.
// If configDir is empty the default ~/.agent-cli directory is used.
func NewModelsConfigStorage(configDir string) (*ModelsConfigStorage, error) {
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		configDir = filepath.Join(home, ConfigDirName)
	}
	abs, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("resolve config dir: %w", err)
	}
	return &ModelsConfigStorage{path: filepath.Join(abs, ModelsFileName)}, nil
}

// Load reads models.yaml, creating it with built-in defaults when it does not exist.
func (s *ModelsConfigStorage) Load() (*ModelsConfig, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		defaults := defaultModelsConfig()
		if writeErr := s.write(defaults); writeErr != nil {
			return nil, fmt.Errorf("create default models.yaml: %w", writeErr)
		}
		return defaults, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read models.yaml: %w", err)
	}
	var cfg ModelsConfig
	if err := yamlv3.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse models.yaml: %w", err)
	}
	return &cfg, nil
}

func (s *ModelsConfigStorage) write(cfg *ModelsConfig) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := yamlv3.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

// defaultModelsConfig returns a pre-filled registry of well-known models.
func defaultModelsConfig() *ModelsConfig {
	text := []string{"text"}
	textImage := []string{"text", "image"}
	textImageAudioVideo := []string{"text", "image", "audio", "video"}

	// Common MIME type sets for file upload validation.
	imageStandard := []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
	imageGemini := []string{"image/png", "image/jpeg", "image/gif", "image/webp", "image/heic", "image/heif"}
	audioGemini := []string{"audio/mpeg", "audio/wav", "audio/ogg", "audio/flac", "audio/aac"}
	videoGemini := []string{"video/mp4", "video/mpeg", "video/webm", "video/quicktime"}
	geminiAll := append(append(append([]string{}, imageGemini...), audioGemini...), videoGemini...)

	return &ModelsConfig{
		Models: []ModelInfo{
			// ── OpenAI ──────────────────────────────────────────────────────────
			{
				Name:                    "gpt-4o",
				Aliases:                 []string{"gpt-4o-2024-11-20", "gpt-4o-2024-08-06"},
				Providers:               []string{"openai", "openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           128000,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "o200k_base",
				SupportedInputMimeTypes: imageStandard,
			},
			{
				Name:                    "gpt-4o-mini",
				Aliases:                 []string{"gpt-4o-mini-2024-07-18"},
				Providers:               []string{"openai", "openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           128000,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "o200k_base",
				SupportedInputMimeTypes: imageStandard,
			},
			{
				Name:                    "o1",
				Aliases:                 []string{"o1-2024-12-17"},
				Providers:               []string{"openai", "openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           200000,
				SupportsToolUse:         true,
				SupportsReasoning:       true,
				Tokenizer:               "o200k_base",
				SupportedInputMimeTypes: imageStandard,
			},
			{
				Name:                    "o3",
				Providers:               []string{"openai", "openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           200000,
				SupportsToolUse:         true,
				SupportsReasoning:       true,
				Tokenizer:               "o200k_base",
				SupportedInputMimeTypes: imageStandard,
			},
			{
				Name:              "o3-mini",
				Aliases:           []string{"o3-mini-2025-01-31"},
				Providers:         []string{"openai", "openrouter"},
				InputModalities:   text,
				OutputModalities:  text,
				MaxTokenCount:     200000,
				SupportsToolUse:   true,
				SupportsReasoning: true,
				Tokenizer:         "o200k_base",
			},
			{
				Name:                    "o4-mini",
				Providers:               []string{"openai", "openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           200000,
				SupportsToolUse:         true,
				SupportsReasoning:       true,
				Tokenizer:               "o200k_base",
				SupportedInputMimeTypes: imageStandard,
			},

			// ── Anthropic ────────────────────────────────────────────────────────
			{
				Name:                    "claude-opus-4-6",
				Aliases:                 []string{"claude-opus-4-6-20251101"},
				Providers:               []string{"anthropic", "openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           200000,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "claude",
				SupportedInputMimeTypes: imageStandard,
			},
			{
				Name:                    "claude-sonnet-4-6",
				Aliases:                 []string{"claude-sonnet-4-6-20251101"},
				Providers:               []string{"anthropic", "openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           200000,
				SupportsToolUse:         true,
				SupportsReasoning:       true,
				Tokenizer:               "claude",
				SupportedInputMimeTypes: imageStandard,
			},
			{
				Name:                    "claude-haiku-4-5",
				Aliases:                 []string{"claude-haiku-4-5-20251001"},
				Providers:               []string{"anthropic", "openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           200000,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "claude",
				SupportedInputMimeTypes: imageStandard,
			},

			// ── Google ───────────────────────────────────────────────────────────
			{
				Name:                    "google/gemini-2.5-pro-preview",
				Providers:               []string{"openrouter", "google"},
				InputModalities:         textImageAudioVideo,
				OutputModalities:        text,
				MaxTokenCount:           1000000,
				SupportsToolUse:         true,
				SupportsReasoning:       true,
				Tokenizer:               "sentencepiece",
				SupportedInputMimeTypes: geminiAll,
			},
			{
				Name:                    "google/gemini-2.5-flash-preview",
				Providers:               []string{"openrouter", "google"},
				InputModalities:         textImageAudioVideo,
				OutputModalities:        text,
				MaxTokenCount:           1000000,
				SupportsToolUse:         true,
				SupportsReasoning:       true,
				Tokenizer:               "sentencepiece",
				SupportedInputMimeTypes: geminiAll,
			},
			{
				Name:                    "google/gemini-2.0-flash",
				Aliases:                 []string{"google/gemini-2.0-flash-001"},
				Providers:               []string{"openrouter", "google"},
				InputModalities:         textImageAudioVideo,
				OutputModalities:        textImage,
				MaxTokenCount:           1000000,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "sentencepiece",
				SupportedInputMimeTypes: geminiAll,
			},
			{
				Name:                    "google/gemini-2.0-flash-thinking-exp",
				Providers:               []string{"openrouter", "google"},
				InputModalities:         textImageAudioVideo,
				OutputModalities:        text,
				MaxTokenCount:           1000000,
				SupportsToolUse:         false,
				SupportsReasoning:       true,
				Tokenizer:               "sentencepiece",
				SupportedInputMimeTypes: geminiAll,
			},
			{
				Name:                    "google/gemini-flash-1.5",
				Aliases:                 []string{"google/gemini-flash-1.5-8b"},
				Providers:               []string{"openrouter", "google"},
				InputModalities:         textImageAudioVideo,
				OutputModalities:        text,
				MaxTokenCount:           1000000,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "sentencepiece",
				SupportedInputMimeTypes: geminiAll,
			},

			// ── Meta ─────────────────────────────────────────────────────────────
			{
				Name:                    "meta-llama/llama-4-maverick",
				Providers:               []string{"openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           524288,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "llama",
				SupportedInputMimeTypes: imageStandard,
			},
			{
				Name:                    "meta-llama/llama-4-scout",
				Providers:               []string{"openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           524288,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "llama",
				SupportedInputMimeTypes: imageStandard,
			},
			{
				Name:              "meta-llama/llama-3.3-70b-instruct",
				Providers:         []string{"openrouter"},
				InputModalities:   text,
				OutputModalities:  text,
				MaxTokenCount:     128000,
				SupportsToolUse:   true,
				SupportsReasoning: false,
				Tokenizer:         "llama",
			},
			{
				Name:              "meta-llama/llama-3.1-8b-instruct",
				Providers:         []string{"openrouter"},
				InputModalities:   text,
				OutputModalities:  text,
				MaxTokenCount:     128000,
				SupportsToolUse:   true,
				SupportsReasoning: false,
				Tokenizer:         "llama",
			},

			// ── DeepSeek ─────────────────────────────────────────────────────────
			{
				Name:              "deepseek/deepseek-chat-v3-0324",
				Aliases:           []string{"deepseek/deepseek-chat"},
				Providers:         []string{"openrouter"},
				InputModalities:   text,
				OutputModalities:  text,
				MaxTokenCount:     65536,
				SupportsToolUse:   true,
				SupportsReasoning: false,
				Tokenizer:         "deepseek",
			},
			{
				Name:              "deepseek/deepseek-r1",
				Providers:         []string{"openrouter"},
				InputModalities:   text,
				OutputModalities:  text,
				MaxTokenCount:     128000,
				SupportsToolUse:   true,
				SupportsReasoning: true,
				Tokenizer:         "deepseek",
			},

			// ── Mistral ──────────────────────────────────────────────────────────
			{
				Name:              "mistralai/mistral-large",
				Providers:         []string{"openrouter"},
				InputModalities:   text,
				OutputModalities:  text,
				MaxTokenCount:     128000,
				SupportsToolUse:   true,
				SupportsReasoning: false,
				Tokenizer:         "sentencepiece",
			},
			{
				Name:                    "mistralai/mistral-small-3.1-24b-instruct",
				Aliases:                 []string{"mistralai/mistral-small"},
				Providers:               []string{"openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           128000,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				Tokenizer:               "sentencepiece",
				SupportedInputMimeTypes: imageStandard,
			},

			// ── fal.ai ───────────────────────────────────────────────────────────
			{
				Name:              "fal-ai/qwen-3-tts/text-to-speech/1.7b",
				Providers:         []string{"fal"},
				InputModalities:   []string{"embedding", "text"},
				OutputModalities:  []string{"audio"},
				MaxTokenCount:     0,
				SupportsToolUse:   false,
				SupportsReasoning: false,
			},

			// ── Z AI ─────────────────────────────────────────────────────────────
			{
				Name:                    "z-ai/glm-4.7",
				Providers:               []string{"openrouter"},
				InputModalities:         textImage,
				OutputModalities:        text,
				MaxTokenCount:           128000,
				SupportsToolUse:         true,
				SupportsReasoning:       false,
				SupportedInputMimeTypes: imageStandard,
			},
		},
	}
}
