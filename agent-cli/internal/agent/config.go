package agent

import (
	"fmt"
	"io"
	"os"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// Config holds all configuration parameters for constructing and executing an agent loop.
type Config struct {
	// System prompt configuration
	SystemPrompt        string // System prompt: file path, raw string, or empty to use AGENTS.md
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
	Stream                bool   // Stream response tokens
	OutputJSON            bool   // Output response as JSON
	OutputReasoningTokens bool   // Emit reasoning/thinking tokens to stdout when streaming
	OutputModality        string // Output modality: text, image, audio, video, embedding
	ModelConfig           string // Model-specific config as JSON (forwarded to provider via Config field)

	// Testing/debugging configuration
	RecordCapturePath string // Path to record LLM request/response captures
	ReplayCapturePath string // Path to replay captures from file

	// Loop configuration
	SystemPromptSuffix   string // Appended to the final system prompt (for iterative loop annotations)
	MaxContinuationDepth int    // Maximum number of TODO queue re-invocations per turn (default: 3)

	// I/O configuration
	StderrWriter io.Writer // Writer for stderr output (refusals, warnings); defaults to os.Stderr when nil

	// Global configuration
	ConfigDir      string // Override directory for agent CLI config
	Verbose        bool   // Enable verbose/debug output (deprecated, use VerbosityLevel)
	VerbosityLevel int    // Verbosity level: 0 = none, 1 = info, 2+ = debug
	LogToStdout    bool   // If true, log to stdout/stderr instead of file
}

// Stderr returns the configured stderr writer, defaulting to os.Stderr.
func (c *Config) Stderr() io.Writer {
	if c.StderrWriter != nil {
		return c.StderrWriter
	}
	return os.Stderr
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
