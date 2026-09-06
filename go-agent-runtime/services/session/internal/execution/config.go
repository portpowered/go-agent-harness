package agent

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ProviderConfig is the normalized provider configuration supplied to a
// provider builder. It intentionally contains values rather than the
// runtime's file-backed configuration tree so embedders do not need to import
// or construct an implementation package.
type ProviderConfig struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
	Fal      *FalProviderConfig
}

// FalProviderConfig contains the provider-specific values used by fal.ai.
type FalProviderConfig struct {
	Model   string
	APIKey  string
	BaseURL string
}

// ModelInfo is the provider-neutral model capability data needed by session
// admission. Hosts resolve this catalog before entering the execution service;
// the execution path does not load models.yaml or discover a config directory.
type ModelInfo struct {
	Name                    string
	Aliases                 []string
	Providers               []string
	InputModalities         []string
	OutputModalities        []string
	SupportedInputMimeTypes []string
}

// SupportsOutputModality reports whether the model advertises the requested
// output modality. An empty catalog deliberately leaves validation to the
// provider, which is useful for embedded hosts with their own policy.
func (m *ModelInfo) SupportsOutputModality(modality string) bool {
	if m == nil {
		return false
	}
	for _, candidate := range m.OutputModalities {
		if candidate == modality {
			return true
		}
	}
	return false
}

// SupportsInputMimeType reports whether the model accepts the given MIME
// type. An omitted list keeps the historical permissive behavior.
func (m *ModelInfo) SupportsInputMimeType(mimeType string) bool {
	if m == nil || len(m.SupportedInputMimeTypes) == 0 {
		return true
	}
	for _, candidate := range m.SupportedInputMimeTypes {
		if candidate == mimeType {
			return true
		}
	}
	return false
}

// ModelCatalog is an invocation-scoped copy of model capability metadata.
type ModelCatalog struct {
	Models []ModelInfo
}

func (c ModelCatalog) Lookup(name string) *ModelInfo {
	for index := range c.Models {
		model := &c.Models[index]
		if model.Name == name {
			return model
		}
		for _, alias := range model.Aliases {
			if alias == name {
				return model
			}
		}
	}
	return nil
}

// ModelPolicy contains resolved model behavior that used to be read from the
// file-backed config tree during execution.
type ModelPolicy struct {
	ContinuationNudgeEnabled bool
	ContinuationNudgeMessage string
	RepetitionPenalty        float64
}

// Config holds all configuration parameters for constructing and executing an agent loop.
type Config struct {
	// System prompt configuration
	SystemPrompt        string // Host-resolved literal system prompt.
	NoSystemInformation bool   // Disable injection of runtime system info into the system prompt

	// Session configuration
	SessionID           string             // Specific session ID to continue
	ContinueLastSession bool               // Continue from the most recent session
	InitialHistory      []messages.Message // Pre-loaded conversation history (for chat)

	// Model/Provider configuration
	Model    string // Model ID (overrides config)
	Provider string // Provider ID (overrides config)
	APIKey   string // API key (overrides config)
	BaseURL  string // Base URL (overrides config)

	// Execution configuration
	OutputReasoningTokens bool   // Emit reasoning/thinking tokens to stdout when streaming
	OutputModality        string // Output modality: text, image, audio, video, embedding
	ModelConfig           string // Model-specific config as JSON (forwarded to provider via Config field)

	// Testing/debugging configuration
	RecordCapturePath string // Path to record LLM request/response captures
	ReplayCapturePath string // Path to replay captures from file

	// Loop configuration
	SystemPromptSuffix   string // Appended to the final system prompt (for iterative loop annotations)
	MaxContinuationDepth int    // Maximum number of TODO queue re-invocations per turn (default: 3)

}

// Validate checks that the configuration is valid.
func (c *Config) Validate() error {
	if c.SystemPrompt != "" && c.ContinueLastSession {
		return fmt.Errorf("cannot use system prompt and continue last session together")
	}
	if c.SessionID != "" && c.ContinueLastSession {
		return fmt.Errorf("cannot use session ID and continue last session together")
	}
	if c.SessionID != "" && c.SystemPrompt != "" {
		return fmt.Errorf("cannot use session ID and system prompt together")
	}
	return nil
}
