package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const BrowserDefaultManagedOpen = "about:blank"

// BrowserConnectionMode identifies which owner supplies the browser
// endpoint after configuration precedence has been resolved.
type BrowserConnectionMode string

const (
	BrowserConnectionModeManaged  BrowserConnectionMode = "managed"
	BrowserConnectionModeExternal BrowserConnectionMode = "external"
)

// DefaultBrowserConfig returns a fresh copy of the complete C0 browser
// configuration with browser tools disabled.
func DefaultBrowserConfig() BrowserConfig {
	return BrowserConfig{
		Tools: BrowserToolsConfig{
			Enabled: false,
			Backend: BrowserToolsBackendWebMCP,
			WebCast: false,
		},
		Connection: BrowserConnectionConfig{
			CDPURL:           "",
			WSEndpoint:       "",
			UserDataDir:      "",
			AllowProcessScan: false,
			AllowRemoteCDP:   false,
		},
		Managed: BrowserManagedConfig{
			Headless:    false,
			Open:        BrowserDefaultManagedOpen,
			CloseOnExit: false,
		},
		Selection: BrowserSelectionConfig{
			Browser:     "",
			Tab:         "",
			Origin:      "",
			AutoSelect:  BrowserAutoSelectOff,
			ActivateTab: false,
			Persist:     true,
		},
		Policy: BrowserPolicyConfig{
			AllowedOrigins:    []string{},
			DeniedOrigins:     []string{},
			Approval:          BrowserApprovalWrites,
			CancelOnInterrupt: BrowserCancelOnInterruptReadOnly,
		},
		Limits: BrowserLimitsConfig{
			InvocationTimeout:  30 * time.Second,
			MaxInputBytes:      262144,
			MaxResultBytes:     262144,
			SerializePerTarget: true,
		},
		Recording: BrowserRecordingConfig{
			Enabled:           false,
			IncludeArguments:  true,
			IncludeResults:    true,
			RedactURLQuery:    true,
			RedactURLFragment: true,
		},
		Replay: BrowserReplayConfig{
			Path:   "",
			Strict: true,
		},
	}
}

// Validate checks the values in the browser configuration after layered
// loading. Raw YAML and environment values are checked before unmarshal too,
// so this method also protects callers that construct Config values directly.
func (c BrowserConfig) Validate() error {
	if c.Tools.Backend != BrowserToolsBackendWebMCP {
		return fmt.Errorf("browser.tools.backend %q is unsupported (only %q is available)", c.Tools.Backend, BrowserToolsBackendWebMCP)
	}
	if c.Tools.WebCast && !c.Tools.Enabled {
		return fmt.Errorf("browser.tools.web_cast requires browser.tools.enabled")
	}
	if !containsString([]string{BrowserAutoSelectOff, BrowserAutoSelectSingle, BrowserAutoSelectPersisted}, c.Selection.AutoSelect) {
		return fmt.Errorf("browser.selection.auto_select %q is invalid (want off, single, or persisted)", c.Selection.AutoSelect)
	}
	if !containsString([]string{BrowserApprovalAlways, BrowserApprovalWrites, BrowserApprovalNever}, c.Policy.Approval) {
		return fmt.Errorf("browser.policy.approval %q is invalid (want always, writes, or never)", c.Policy.Approval)
	}
	if !containsString([]string{BrowserCancelOnInterruptNever, BrowserCancelOnInterruptReadOnly, BrowserCancelOnInterruptAlways}, c.Policy.CancelOnInterrupt) {
		return fmt.Errorf("browser.policy.cancel_on_interrupt %q is invalid (want never, read-only, or always)", c.Policy.CancelOnInterrupt)
	}
	if c.Limits.InvocationTimeout <= 0 {
		return fmt.Errorf("browser.limits.invocation_timeout must be positive")
	}
	if c.Limits.MaxInputBytes < 0 {
		return fmt.Errorf("browser.limits.max_input_bytes must be non-negative")
	}
	if c.Limits.MaxResultBytes < 0 {
		return fmt.Errorf("browser.limits.max_result_bytes must be non-negative")
	}
	if err := validateManagedOpenURL(c.Managed.Open); err != nil {
		return err
	}
	return nil
}

// ValidateBrowser returns browser configuration validation errors without
// changing the existing provider validation contract.
func (c Config) ValidateBrowser() error {
	return c.Browser.Validate()
}

// BrowserBackendEnabled reports whether the loaded configuration has selected
// the supported WebMCP backend and enabled its capability.
func (c BrowserConfig) BrowserBackendEnabled() bool {
	return c.Tools.Enabled && c.Tools.Backend == BrowserToolsBackendWebMCP
}

// ConnectionMode returns the ownership mode selected by endpoint precedence.
// An explicit CDP or WebSocket endpoint always belongs to the caller. A
// configured profile or process scan is also an external discovery request;
// only a completely endpoint-free connection selects the agent-managed
// browser.
func (c BrowserConfig) ConnectionMode() BrowserConnectionMode {
	if strings.TrimSpace(c.Connection.CDPURL) != "" || strings.TrimSpace(c.Connection.WSEndpoint) != "" ||
		strings.TrimSpace(c.Connection.UserDataDir) != "" || c.Connection.AllowProcessScan {
		return BrowserConnectionModeExternal
	}
	return BrowserConnectionModeManaged
}

// UsesManagedBrowser reports whether this configuration requests an
// agent-managed endpoint rather than an explicitly supplied browser.
func (c BrowserConfig) UsesManagedBrowser() bool {
	return c.ConnectionMode() == BrowserConnectionModeManaged
}

// UsesExternalBrowser reports whether an explicit endpoint owns the browser
// connection. Managed lifecycle controls must never close this path.
func (c BrowserConfig) UsesExternalBrowser() bool {
	return c.ConnectionMode() == BrowserConnectionModeExternal
}

// ManagedStartupURL returns the effective single page for a managed launch.
func (c BrowserConfig) ManagedStartupURL() string {
	if open := strings.TrimSpace(c.Managed.Open); open != "" {
		return open
	}
	return BrowserDefaultManagedOpen
}

func validateManagedOpenURL(raw string) error {
	open := strings.TrimSpace(raw)
	if open == "" {
		return nil
	}
	parsed, err := url.Parse(open)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("browser.managed.open %q is not a valid startup URL (use an absolute URL such as https://example.test or about:blank)", raw)
	}
	if parsed.User != nil {
		return fmt.Errorf("browser.managed.open must not contain URL credentials")
	}
	if (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && parsed.Hostname() == "" {
		return fmt.Errorf("browser.managed.open %q is not a valid startup URL (HTTP URLs require a host)", raw)
	}
	return nil
}

// validateBrowserRawBool accepts native YAML booleans from files and only the
// strict strings "true" or "false" from environment variables.
func validateBrowserRawBool(value interface{}, source string, fromEnvironment bool) error {
	switch typed := value.(type) {
	case bool:
		if !fromEnvironment {
			return nil
		}
	case string:
		if fromEnvironment && (typed == "true" || typed == "false") {
			return nil
		}
	}
	return fmt.Errorf("%s: expected strict boolean true or false", source)
}

func validateBrowserRawEnum(allowed []string, value interface{}, source string) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s: expected one of %s", source, strings.Join(allowed, ", "))
	}
	if !containsString(allowed, text) {
		return fmt.Errorf("%s: invalid value %q (want one of %s)", source, text, strings.Join(allowed, ", "))
	}
	return nil
}

func validateBrowserRawDuration(value interface{}, source string) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s: expected a positive Go duration such as 30s", source)
	}
	duration, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("%s: invalid Go duration %q: %w", source, text, err)
	}
	if duration <= 0 {
		return fmt.Errorf("%s: duration must be positive", source)
	}
	return nil
}

// validateBrowserRawSize accepts decimal strings only from environment
// variables and non-negative YAML integers from files.
func validateBrowserRawSize(value interface{}, source string, fromEnvironment bool) error {
	if text, ok := value.(string); ok {
		if !fromEnvironment {
			return fmt.Errorf("%s: expected a non-negative decimal integer", source)
		}
		if _, err := parseNonNegativeDecimalSize(text); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		return nil
	}
	if err := validateYAMLInteger(value); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	return nil
}

// validateBrowserRawStringList accepts a JSON array string only from
// environment variables and a YAML sequence from files.
func validateBrowserRawStringList(value interface{}, source string, fromEnvironment bool) error {
	if text, ok := value.(string); ok {
		if !fromEnvironment {
			return fmt.Errorf("%s: expected a YAML list of strings", source)
		}
		var values []interface{}
		if err := json.Unmarshal([]byte(text), &values); err != nil {
			return fmt.Errorf("%s: expected a JSON array of strings: %w", source, err)
		}
		if values == nil {
			return fmt.Errorf("%s: expected a JSON array of strings", source)
		}
		return validateBrowserRawStringItems(values, source)
	}
	values, ok := value.([]interface{})
	if !ok {
		return fmt.Errorf("%s: expected a YAML list of strings", source)
	}
	return validateBrowserRawStringItems(values, source)
}

func validateBrowserRawStringItems(values []interface{}, source string) error {
	for index, item := range values {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("%s: item %d must be a string", source, index)
		}
	}
	return nil
}
