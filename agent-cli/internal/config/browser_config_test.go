package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultBrowserConfig_IsComplete(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStorage(filepath.Join(dir, ConfigFileName)).Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	want := DefaultBrowserConfig()
	if cfg.Browser.Tools != want.Tools {
		t.Errorf("tools = %+v, want %+v", cfg.Browser.Tools, want.Tools)
	}
	if cfg.Browser.Connection != want.Connection {
		t.Errorf("connection = %+v, want %+v", cfg.Browser.Connection, want.Connection)
	}
	if cfg.Browser.Selection != want.Selection {
		t.Errorf("selection = %+v, want %+v", cfg.Browser.Selection, want.Selection)
	}
	if cfg.Browser.Policy.Approval != want.Policy.Approval || cfg.Browser.Policy.CancelOnInterrupt != want.Policy.CancelOnInterrupt {
		t.Errorf("policy enums = %+v, want %+v", cfg.Browser.Policy, want.Policy)
	}
	if len(cfg.Browser.Policy.AllowedOrigins) != 0 || len(cfg.Browser.Policy.DeniedOrigins) != 0 {
		t.Fatalf("default origin lists = allowed:%v denied:%v, want empty lists", cfg.Browser.Policy.AllowedOrigins, cfg.Browser.Policy.DeniedOrigins)
	}
	if cfg.Browser.Limits != want.Limits {
		t.Errorf("limits = %+v, want %+v", cfg.Browser.Limits, want.Limits)
	}
	if cfg.Browser.Recording != want.Recording || cfg.Browser.Replay != want.Replay {
		t.Errorf("recording/replay = %+v/%+v, want %+v/%+v", cfg.Browser.Recording, cfg.Browser.Replay, want.Recording, want.Replay)
	}
	if cfg.Browser.Limits.InvocationTimeout != 30*time.Second {
		t.Fatalf("default timeout = %s, want 30s", cfg.Browser.Limits.InvocationTimeout)
	}

	data, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	for _, wantLine := range []string{
		"browser:",
		"enabled: false",
		"backend: webmcp",
		"cdp_url: \"\"",
		"ws_endpoint: \"\"",
		"auto_select: \"off\"",
		"invocation_timeout: 30s",
		"max_input_bytes: 262144",
		"max_result_bytes: 262144",
		"strict: true",
	} {
		if !strings.Contains(string(data), wantLine) {
			t.Errorf("generated config missing %q:\n%s", wantLine, data)
		}
	}
}

func TestLoadBrowserConfig_FromYAML(t *testing.T) {
	const browserYAML = `
browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: http://127.0.0.1:9222
    ws_endpoint: ws://127.0.0.1:9222/devtools/browser/abc
    user_data_dir: /tmp/agent-browser
    allow_process_scan: true
    allow_remote_cdp: true
  selection:
    browser: browser-1
    tab: target-1
    origin: https://app.example
    auto_select: persisted
    activate_tab: true
    persist: false
  policy:
    allowed_origins:
      - https://app.example
      - https://docs.example
    denied_origins: [https://blocked.example]
    approval: always
    cancel_on_interrupt: always
  limits:
    invocation_timeout: 2m30s
    max_input_bytes: 1024
    max_result_bytes: 2048
    serialize_per_target: false
  recording:
    enabled: true
    include_arguments: false
    include_results: false
    redact_url_query: false
    redact_url_fragment: false
  replay:
    path: /tmp/browser-events.jsonl
    strict: false
`
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(browserYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := NewConfigStorage(path).Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	got := cfg.Browser
	if !got.BrowserBackendEnabled() || got.Tools.Backend != BrowserToolsBackendWebMCP {
		t.Fatalf("tools = %+v, want enabled WebMCP", got.Tools)
	}
	if got.Connection.CDPURL != "http://127.0.0.1:9222" || got.Connection.WSEndpoint == "" || !got.Connection.AllowRemoteCDP {
		t.Errorf("connection = %+v", got.Connection)
	}
	if got.Selection.Browser != "browser-1" || got.Selection.Tab != "target-1" || got.Selection.AutoSelect != BrowserAutoSelectPersisted || got.Selection.Persist {
		t.Errorf("selection = %+v", got.Selection)
	}
	if len(got.Policy.AllowedOrigins) != 2 || got.Policy.AllowedOrigins[1] != "https://docs.example" || got.Policy.DeniedOrigins[0] != "https://blocked.example" {
		t.Errorf("policy origins = %+v", got.Policy)
	}
	if got.Limits.InvocationTimeout != 150*time.Second || got.Limits.MaxInputBytes != 1024 || got.Limits.MaxResultBytes != 2048 || got.Limits.SerializePerTarget {
		t.Errorf("limits = %+v", got.Limits)
	}
	if !got.Recording.Enabled || got.Recording.IncludeArguments || got.Recording.IncludeResults || got.Recording.RedactURLQuery || got.Recording.RedactURLFragment {
		t.Errorf("recording = %+v", got.Recording)
	}
	if got.Replay.Path != "/tmp/browser-events.jsonl" || got.Replay.Strict {
		t.Errorf("replay = %+v", got.Replay)
	}
}

func TestLoadBrowserConfig_EnvironmentOverridesEachNestedValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	const yaml = `
browser:
  connection:
    cdp_url: http://file.example:9222
    user_data_dir: /file/profile
  selection:
    browser: file-browser
    tab: file-tab
  policy:
    allowed_origins: [https://file.example]
  limits:
    max_input_bytes: 10
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env := map[string]string{
		"AGENT_BROWSER__TOOLS__ENABLED":                 "true",
		"AGENT_BROWSER__TOOLS__BACKEND":                 "webmcp",
		"AGENT_BROWSER__CONNECTION__CDP_URL":            "http://env.example:9222",
		"AGENT_BROWSER__CONNECTION__WS_ENDPOINT":        "ws://env.example/devtools/browser/id",
		"AGENT_BROWSER__CONNECTION__USER_DATA_DIR":      "/env/profile",
		"AGENT_BROWSER__CONNECTION__ALLOW_PROCESS_SCAN": "true",
		"AGENT_BROWSER__CONNECTION__ALLOW_REMOTE_CDP":   "true",
		"AGENT_BROWSER__SELECTION__BROWSER":             "env-browser",
		"AGENT_BROWSER__SELECTION__TAB":                 "env-tab",
		"AGENT_BROWSER__SELECTION__ORIGIN":              "https://env.example",
		"AGENT_BROWSER__SELECTION__AUTO_SELECT":         "single",
		"AGENT_BROWSER__SELECTION__ACTIVATE_TAB":        "true",
		"AGENT_BROWSER__SELECTION__PERSIST":             "false",
		"AGENT_BROWSER__POLICY__ALLOWED_ORIGINS":        `["https://allowed.example","https://other.example"]`,
		"AGENT_BROWSER__POLICY__DENIED_ORIGINS":         `["https://denied.example"]`,
		"AGENT_BROWSER__POLICY__APPROVAL":               "never",
		"AGENT_BROWSER__POLICY__CANCEL_ON_INTERRUPT":    "never",
		"AGENT_BROWSER__LIMITS__INVOCATION_TIMEOUT":     "45s",
		"AGENT_BROWSER__LIMITS__MAX_INPUT_BYTES":        "100",
		"AGENT_BROWSER__LIMITS__MAX_RESULT_BYTES":       "200",
		"AGENT_BROWSER__LIMITS__SERIALIZE_PER_TARGET":   "false",
		"AGENT_BROWSER__RECORDING__ENABLED":             "true",
		"AGENT_BROWSER__RECORDING__INCLUDE_ARGUMENTS":   "false",
		"AGENT_BROWSER__RECORDING__INCLUDE_RESULTS":     "false",
		"AGENT_BROWSER__RECORDING__REDACT_URL_QUERY":    "false",
		"AGENT_BROWSER__RECORDING__REDACT_URL_FRAGMENT": "false",
		"AGENT_BROWSER__REPLAY__PATH":                   "/env/replay.jsonl",
		"AGENT_BROWSER__REPLAY__STRICT":                 "false",
	}
	for name, value := range env {
		t.Setenv(name, value)
	}

	cfg, err := NewConfigStorage(path).Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	got := cfg.Browser
	if !got.BrowserBackendEnabled() || got.Connection.CDPURL != "http://env.example:9222" || got.Connection.WSEndpoint == "" || got.Connection.UserDataDir != "/env/profile" || !got.Connection.AllowProcessScan || !got.Connection.AllowRemoteCDP {
		t.Errorf("environment connection/tools = %+v/%+v", got.Connection, got.Tools)
	}
	if got.Selection.Browser != "env-browser" || got.Selection.Tab != "env-tab" || got.Selection.Origin != "https://env.example" || got.Selection.AutoSelect != BrowserAutoSelectSingle || !got.Selection.ActivateTab || got.Selection.Persist {
		t.Errorf("environment selection = %+v", got.Selection)
	}
	if len(got.Policy.AllowedOrigins) != 2 || got.Policy.AllowedOrigins[0] != "https://allowed.example" || len(got.Policy.DeniedOrigins) != 1 || got.Policy.Approval != BrowserApprovalNever || got.Policy.CancelOnInterrupt != BrowserCancelOnInterruptNever {
		t.Errorf("environment policy = %+v", got.Policy)
	}
	if got.Limits.InvocationTimeout != 45*time.Second || got.Limits.MaxInputBytes != 100 || got.Limits.MaxResultBytes != 200 || got.Limits.SerializePerTarget {
		t.Errorf("environment limits = %+v", got.Limits)
	}
	if !got.Recording.Enabled || got.Recording.IncludeArguments || got.Recording.IncludeResults || got.Recording.RedactURLQuery || got.Recording.RedactURLFragment || got.Replay.Path != "/env/replay.jsonl" || got.Replay.Strict {
		t.Errorf("environment recording/replay = %+v/%+v", got.Recording, got.Replay)
	}

	// Values not supplied by the environment retain their YAML values only if
	// the environment did not override them; this is checked independently by
	// the focused precedence test below.
	if got.Connection.CDPURL == "http://file.example:9222" || got.Limits.MaxInputBytes == 10 {
		t.Fatal("environment values did not override YAML")
	}
}

func TestLoadBrowserConfig_EnvironmentPrecedencePreservesSiblings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	const yaml = `
browser:
  connection:
    cdp_url: http://file.example:9222
    user_data_dir: /file/profile
  selection:
    browser: file-browser
    tab: file-tab
  policy:
    allowed_origins: [https://file.example]
  limits:
    max_input_bytes: 10
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("AGENT_BROWSER__CONNECTION__CDP_URL", "http://env.example:9222")

	cfg, err := NewConfigStorage(path).Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Browser.Connection.CDPURL != "http://env.example:9222" {
		t.Fatalf("CDP URL = %q, want env value", cfg.Browser.Connection.CDPURL)
	}
	if cfg.Browser.Connection.UserDataDir != "/file/profile" || cfg.Browser.Selection.Browser != "file-browser" || cfg.Browser.Selection.Tab != "file-tab" || len(cfg.Browser.Policy.AllowedOrigins) != 1 || cfg.Browser.Limits.MaxInputBytes != 10 {
		t.Fatalf("unrelated YAML values were cleared: browser=%+v", cfg.Browser)
	}
}

func TestLoadBrowserConfig_RejectsInvalidYAMLValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "backend", value: "browser:\n  tools:\n    backend: chrome\n", want: "browser.tools.backend"},
		{name: "boolean", value: "browser:\n  tools:\n    enabled: yes\n", want: "strict boolean"},
		{name: "auto select", value: "browser:\n  selection:\n    auto_select: many\n", want: "browser.selection.auto_select"},
		{name: "approval", value: "browser:\n  policy:\n    approval: ask\n", want: "browser.policy.approval"},
		{name: "cancel policy", value: "browser:\n  policy:\n    cancel_on_interrupt: ask\n", want: "browser.policy.cancel_on_interrupt"},
		{name: "duration", value: "browser:\n  limits:\n    invocation_timeout: soon\n", want: "invocation_timeout"},
		{name: "duration numeric", value: "browser:\n  limits:\n    invocation_timeout: 30\n", want: "positive Go duration"},
		{name: "negative size", value: "browser:\n  limits:\n    max_input_bytes: -1\n", want: "max_input_bytes"},
		{name: "fractional size", value: "browser:\n  limits:\n    max_result_bytes: 1.5\n", want: "non-negative decimal integer"},
		{name: "origin list", value: "browser:\n  policy:\n    allowed_origins: https://example.test\n", want: "allowed_origins"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ConfigFileName)
			if err := os.WriteFile(path, []byte(tt.value), 0600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := NewConfigStorage(path).Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadBrowserConfig_RejectsInvalidEnvironmentValues(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
		want  string
	}{
		{name: "backend", env: "AGENT_BROWSER__TOOLS__BACKEND", value: "chrome", want: "TOOLS__BACKEND"},
		{name: "boolean", env: "AGENT_BROWSER__TOOLS__ENABLED", value: "TRUE", want: "strict boolean"},
		{name: "duration", env: "AGENT_BROWSER__LIMITS__INVOCATION_TIMEOUT", value: "30", want: "Go duration"},
		{name: "size", env: "AGENT_BROWSER__LIMITS__MAX_INPUT_BYTES", value: "0x10", want: "decimal integer"},
		{name: "origins", env: "AGENT_BROWSER__POLICY__ALLOWED_ORIGINS", value: "https://example.test", want: "JSON array"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.env, tt.value)
			_, err := NewConfigStorage(filepath.Join(t.TempDir(), ConfigFileName)).Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadBrowserConfig_LegacyWebMCPInputsDoNotActivateBrowser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("webmcp:\n  tools:\n    enabled: true\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("AGENT_WEBMCP__TOOLS__ENABLED", "true")

	cfg, err := NewConfigStorage(path).Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Browser.Tools.Enabled {
		t.Fatal("legacy webmcp configuration activated browser tools")
	}
	if cfg.Browser.Tools.Backend != BrowserToolsBackendWebMCP {
		t.Fatalf("legacy inputs changed backend to %q", cfg.Browser.Tools.Backend)
	}
}
