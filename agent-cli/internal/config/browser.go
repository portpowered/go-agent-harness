package config

import (
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
// An explicit CDP or WebSocket endpoint always belongs to the caller; an
// empty endpoint requests an agent-managed browser.
func (c BrowserConfig) ConnectionMode() BrowserConnectionMode {
	if strings.TrimSpace(c.Connection.CDPURL) != "" || strings.TrimSpace(c.Connection.WSEndpoint) != "" {
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
