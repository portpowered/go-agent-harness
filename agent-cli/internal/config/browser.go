package config

import (
	"fmt"
	"time"
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
