package config

// Default values for model configuration
const (
	DefaultModelProvider = "openrouter"
	DefaultModelModel    = "z-ai/glm-4.7"
)

const (
	ProviderOpenAI     = "openai"
	ProviderOpenRouter = "openrouter"
	ProviderLocal      = "local"
	ProviderFal        = "fal"
	ProviderGrok       = "grok"
)

// Configuration directory and file names
const (
	ConfigDirName  = ".agent-cli"
	ConfigFileName = "config.yaml"
)

// Environment variable prefix
const EnvPrefix = "AGENT_"

// structTag is the struct tag used by koanf for unmarshaling
const structTag = "koanf"

// Config holds the agent CLI configuration.
type Config struct {
	Model ModelConfig `koanf:"model" yaml:"model"`
	Tools ToolsConfig `koanf:"tools" yaml:"tools"`
}

// ModelConfig holds which provider is active and per-provider settings.
// Use ActiveOpenAI() / ActiveClaude() or ActiveProviderConfig() to get the selected provider's config.
type ModelConfig struct {
	Provider   string        `koanf:"provider" yaml:"provider"`
	OpenAI     *OpenAIConfig `koanf:"openai" yaml:"openai"`
	Claude     *ClaudeConfig `koanf:"claude" yaml:"claude"`
	OpenRouter *OpenAIConfig `koanf:"openrouter" yaml:"openrouter"`
	Local      *OpenAIConfig `koanf:"local" yaml:"local"`
	Fal        *FalConfig    `koanf:"fal" yaml:"fal"`
	Grok       *GrokConfig   `koanf:"grok" yaml:"grok"`

	// ContinuationNudgeEnabled enables automatic re-invocation when a model stops
	// early (no tool call and no stop-word in the response). Default: false.
	ContinuationNudgeEnabled bool `koanf:"continuation_nudge_enabled" yaml:"continuation_nudge_enabled"`
	// ContinuationNudgeMessage is the message enqueued in the TodoQueue when an
	// early stop is detected. Ignored when ContinuationNudgeEnabled is false.
	// Default (when empty): "Please continue where you left off."
	ContinuationNudgeMessage string `koanf:"continuation_nudge_message" yaml:"continuation_nudge_message"`

	// RepetitionPenalty penalizes repeated tokens in model output.
	// Valid range: 1.0–2.0; default 1.0 (disabled). Mapped to frequency_penalty
	// in outgoing OpenAI-compatible requests.
	RepetitionPenalty float64 `koanf:"repetition_penalty" yaml:"repetition_penalty"`
}

// OpenAIConfig holds OpenAI-compatible provider settings (OpenAI, OpenRouter, etc.).
type OpenAIConfig struct {
	Model   string `koanf:"model" yaml:"model"`
	APIKey  string `koanf:"api_key" yaml:"api_key"`
	BaseURL string `koanf:"base_url" yaml:"base_url"`
}

// ClaudeConfig holds Claude provider settings (placeholder for future fields).
type ClaudeConfig struct {
	Model  string `koanf:"model" yaml:"model"`
	APIKey string `koanf:"api_key" yaml:"api_key"`
}

// FalConfig holds fal.ai provider settings.
type FalConfig struct {
	Model   string `koanf:"model" yaml:"model"`
	APIKey  string `koanf:"api_key" yaml:"api_key"`
	BaseURL string `koanf:"base_url" yaml:"base_url"`
}

// GrokConfig holds xAI Grok realtime session provider settings.
type GrokConfig struct {
	Model   string `koanf:"model" yaml:"model"`
	APIKey  string `koanf:"api_key" yaml:"api_key"`
	BaseURL string `koanf:"base_url" yaml:"base_url"`
}

type WebToolsConfig struct {
	Brave      BraveConfig      `koanf:"brave" yaml:"brave"`
	DuckDuckGo DuckDuckGoConfig `koanf:"duckduckgo" yaml:"duckduckgo"`
}

type BraveConfig struct {
	Enabled    bool   `koanf:"enabled" yaml:"enabled"`
	APIKey     string `koanf:"api_key" yaml:"api_key"`
	MaxResults int    `koanf:"max_results" yaml:"max_results"`
}

type DuckDuckGoConfig struct {
	Enabled    bool `koanf:"enabled" yaml:"enabled"`
	MaxResults int  `koanf:"max_results" yaml:"max_results"`
}

type CronToolsConfig struct {
	ExecTimeoutMinutes int `koanf:"exec_timeout_minutes" yaml:"exec_timeout_minutes"`
}

type ExecConfig struct {
	EnableDenyPatterns bool     `koanf:"enable_deny_patterns" yaml:"enable_deny_patterns"`
	CustomDenyPatterns []string `koanf:"custom_deny_patterns" yaml:"custom_deny_patterns"`
}

// DefaultToolIDs is the ordered list of all tool IDs. Used to build the default tools list.
var DefaultToolIDs = []string{
	"exec", "read_file", "write_file", "edit_file", "append_file", "list_dir",
	"web_fetch", "web_search", "show", "mouse", "load_skill", "sleep",
}

// DefaultToolsList returns the default tools list (all enabled). Used when creating a new config file.
func DefaultToolsList() []ToolEntry {
	out := make([]ToolEntry, 0, len(DefaultToolIDs))
	for _, id := range DefaultToolIDs {
		out = append(out, ToolEntry{ID: id, Enabled: true})
	}
	return out
}

// ToolEntry configures a single tool by id and whether it is enabled.
// Tools not listed default to enabled. Set enabled: false to disable a tool.
type ToolEntry struct {
	ID      string `koanf:"id" yaml:"id"`
	Enabled bool   `koanf:"enabled" yaml:"enabled"`
}

// ToolsConfig holds tool feature config (web, cron, exec) and the list of tools with per-tool enabled flag.
type ToolsConfig struct {
	Web  WebToolsConfig  `koanf:"web" yaml:"web"`
	Cron CronToolsConfig `koanf:"cron" yaml:"cron"`
	Exec ExecConfig      `koanf:"exec" yaml:"exec"`
	// List defines which tools are enabled. Each entry has id and enabled (default true if omitted).
	// Tools not in the list are enabled by default. Set enabled: false to disable a tool.
	List []ToolEntry `koanf:"list" yaml:"list"`
}

// ToolEnabled returns whether a tool with the given id should be enabled.
// If List is empty, all tools are enabled. Otherwise, an entry with enabled: false disables that tool.
func (c ToolsConfig) ToolEnabled(id string) bool {
	if len(c.List) == 0 {
		return true
	}
	for _, e := range c.List {
		if e.ID == id {
			return e.Enabled
		}
	}
	return true
}
