package config

import "time"

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
	// Browser contains the opt-in WebMCP browser capability configuration.
	// Browser settings never activate a model session by themselves; the
	// session command owns that admission decision.
	Browser BrowserConfig `koanf:"browser" yaml:"browser"`
	// ConfigDir is runtime metadata supplied by the command that loaded this
	// snapshot. It is intentionally excluded from persisted configuration; the
	// session capability factory uses it to bind selection persistence to the
	// same -C directory as the effective browser configuration.
	ConfigDir string `koanf:"-" yaml:"-" json:"-"`
}

const (
	// BrowserToolsBackendWebMCP is the only browser capability backend frozen by
	// the C0 contract.
	BrowserToolsBackendWebMCP = "webmcp"

	BrowserAutoSelectOff       = "off"
	BrowserAutoSelectSingle    = "single"
	BrowserAutoSelectPersisted = "persisted"

	BrowserApprovalAlways = "always"
	BrowserApprovalWrites = "writes"
	BrowserApprovalNever  = "never"

	BrowserCancelOnInterruptNever    = "never"
	BrowserCancelOnInterruptReadOnly = "read-only"
	BrowserCancelOnInterruptAlways   = "always"
)

// BrowserConfig is the complete browser configuration tree defined by the
// WebMCP C0 operator contract.
type BrowserConfig struct {
	Tools      BrowserToolsConfig      `koanf:"tools" yaml:"tools"`
	Connection BrowserConnectionConfig `koanf:"connection" yaml:"connection"`
	Selection  BrowserSelectionConfig  `koanf:"selection" yaml:"selection"`
	Policy     BrowserPolicyConfig     `koanf:"policy" yaml:"policy"`
	Limits     BrowserLimitsConfig     `koanf:"limits" yaml:"limits"`
	Recording  BrowserRecordingConfig  `koanf:"recording" yaml:"recording"`
	Replay     BrowserReplayConfig     `koanf:"replay" yaml:"replay"`
}

// BrowserToolsConfig controls whether browser tools are available to a
// session. Backend remains explicit even when the capability is disabled so a
// generated config has a stable, forward-compatible shape.
type BrowserToolsConfig struct {
	Enabled bool   `koanf:"enabled" yaml:"enabled"`
	Backend string `koanf:"backend" yaml:"backend"`
}

// BrowserConnectionConfig controls the ordered browser discovery sources.
type BrowserConnectionConfig struct {
	CDPURL           string `koanf:"cdp_url" yaml:"cdp_url"`
	WSEndpoint       string `koanf:"ws_endpoint" yaml:"ws_endpoint"`
	UserDataDir      string `koanf:"user_data_dir" yaml:"user_data_dir"`
	AllowProcessScan bool   `koanf:"allow_process_scan" yaml:"allow_process_scan"`
	AllowRemoteCDP   bool   `koanf:"allow_remote_cdp" yaml:"allow_remote_cdp"`
}

// BrowserSelectionConfig controls exact browser/target selection and
// persistence behavior.
type BrowserSelectionConfig struct {
	Browser     string `koanf:"browser" yaml:"browser"`
	Tab         string `koanf:"tab" yaml:"tab"`
	Origin      string `koanf:"origin" yaml:"origin"`
	AutoSelect  string `koanf:"auto_select" yaml:"auto_select"`
	ActivateTab bool   `koanf:"activate_tab" yaml:"activate_tab"`
	Persist     bool   `koanf:"persist" yaml:"persist"`
}

// BrowserPolicyConfig contains origin and interactive approval/cancellation
// policy for page invocations.
type BrowserPolicyConfig struct {
	AllowedOrigins    []string `koanf:"allowed_origins" yaml:"allowed_origins"`
	DeniedOrigins     []string `koanf:"denied_origins" yaml:"denied_origins"`
	Approval          string   `koanf:"approval" yaml:"approval"`
	CancelOnInterrupt string   `koanf:"cancel_on_interrupt" yaml:"cancel_on_interrupt"`
}

// BrowserLimitsConfig bounds local WebMCP input, result, and invocation work.
type BrowserLimitsConfig struct {
	InvocationTimeout  time.Duration `koanf:"invocation_timeout" yaml:"invocation_timeout"`
	MaxInputBytes      int           `koanf:"max_input_bytes" yaml:"max_input_bytes"`
	MaxResultBytes     int           `koanf:"max_result_bytes" yaml:"max_result_bytes"`
	SerializePerTarget bool          `koanf:"serialize_per_target" yaml:"serialize_per_target"`
}

// BrowserRecordingConfig controls semantic browser event recording and URL
// redaction within the existing session recording bundle.
type BrowserRecordingConfig struct {
	Enabled           bool `koanf:"enabled" yaml:"enabled"`
	IncludeArguments  bool `koanf:"include_arguments" yaml:"include_arguments"`
	IncludeResults    bool `koanf:"include_results" yaml:"include_results"`
	RedactURLQuery    bool `koanf:"redact_url_query" yaml:"redact_url_query"`
	RedactURLFragment bool `koanf:"redact_url_fragment" yaml:"redact_url_fragment"`
}

// BrowserReplayConfig selects a semantic browser fixture and its strictness.
type BrowserReplayConfig struct {
	Path   string `koanf:"path" yaml:"path"`
	Strict bool   `koanf:"strict" yaml:"strict"`
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
	"exec", "read_file", "read_image", "write_file", "edit_file", "append_file", "list_dir",
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
