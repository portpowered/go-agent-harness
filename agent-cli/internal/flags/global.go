package flags

// GlobalFlags holds persistent CLI flags for the agent CLI.
type GlobalFlags struct {
	VerboseMode   int      // Verbosity level: 0 = none, 1 = info, 2+ = debug
	ConfigDirPath string   // Override directory for agent CLI config (default: ~/.agent-cli)
	LogToStdout   bool     // Override default file logging and log to stdout/stderr instead
	WorkDirPath   string   // Filesystem-tool workdir (default: process current directory)
	AllowPathList []string // Additional filesystem-tool roots (repeatable)
}

// NewGlobalFlags returns default global flags.
func NewGlobalFlags() *GlobalFlags {
	return &GlobalFlags{
		VerboseMode:   0,
		ConfigDirPath: "",
		LogToStdout:   false,
		WorkDirPath:   "",
		AllowPathList: nil,
	}
}

// ConfigDir returns the config directory override (empty means use default ~/.agent-cli).
func (f *GlobalFlags) ConfigDir() string {
	if f == nil {
		return ""
	}
	return f.ConfigDirPath
}

// WorkDir returns the requested filesystem-tool workdir. An empty value means
// the process current working directory and is resolved at run startup.
func (f *GlobalFlags) WorkDir() string {
	if f == nil {
		return ""
	}
	return f.WorkDirPath
}

// AllowPaths returns a copy of the additional filesystem-tool roots supplied
// on the command line.
func (f *GlobalFlags) AllowPaths() []string {
	if f == nil {
		return nil
	}
	return append([]string(nil), f.AllowPathList...)
}

// AskFlags holds flags specific to the ask command (overrides config).
type AskFlags struct {
	Stream                bool   // Stream response tokens
	SystemPrompt          string // System prompt: file path or raw string
	ContinueLastSession   bool   // Continue from the most recent session
	SessionID             string // Specific session ID to continue
	Model                 string // Model ID (overrides config)
	Provider              string // Provider ID (overrides config)
	ShowToolUse           bool   // Show tool invocations in output
	APIKey                string // API key (overrides config)
	BaseURL               string // Base URL (overrides config, required for --provider local)
	RecordCapturePath     string // Path to record LLM request/response captures (for testing)
	ReplayCapturePath     string // Path to replay captures from file (for testing)
	OutputReasoningTokens bool   // Emit reasoning/thinking tokens to stdout when streaming
	NoSystemInformation   bool   // Disable injection of runtime system info into the system prompt
	OutputJSON            bool   // Output response as JSON; when combined with --stream, emits NDJSON delta events
	OutputModality        string // Output modality: text, image, audio, video, embedding (default: text behavior)
	ModelConfig           string // Model-specific config as JSON string (forwarded to provider)
}

// NewAskFlags returns default ask flags.
func NewAskFlags() *AskFlags {
	return &AskFlags{}
}

// ChatFlags holds flags specific to the chat command.
type ChatFlags struct {
	ActivateAudioIn  bool // Enable audio input
	ActivateAudioOut bool // Enable audio output
}

// NewChatFlags returns default chat flags.
func NewChatFlags() *ChatFlags {
	return &ChatFlags{}
}

// LoopFlags holds flags for the iterative loop mode (--loop).
type LoopFlags struct {
	Loop                     bool    // Enable iterative loop mode
	MaxIterations            int     // Maximum number of iterations (default 5)
	StopWord                 string  // Stop word to detect completion
	ContextPressureThreshold float64 // Context pressure threshold (0-1, default 0.8)
	ContextPressureMessage   string  // Custom context pressure warning message
	TraceID                  string  // Resume from an existing trace ID
}

// NewLoopFlags returns default loop flags.
func NewLoopFlags() *LoopFlags {
	return &LoopFlags{
		MaxIterations:            5,
		ContextPressureThreshold: 0.8,
	}
}
