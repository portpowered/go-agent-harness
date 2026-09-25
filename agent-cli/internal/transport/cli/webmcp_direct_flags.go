package cli

import (
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/spf13/cobra"
)

// DefaultWebMCPDirectCommandTimeout is the end-to-end safety deadline for one
// direct WebMCP command; it is direct.DefaultCommandTimeout. A caller may
// choose a shorter value with --command-timeout; zero uses this safe default.
const DefaultWebMCPDirectCommandTimeout = direct.DefaultCommandTimeout

// browserFlagPrefix is the legacy spelling prefix still honored when a
// direct command reports which browser flags were changed.
const browserFlagPrefix = "browser-"

func registerWebMCPDirectCommandTimeoutFlag(cmd *cobra.Command, values *webmcpDirectFlags) {
	if cmd == nil || values == nil {
		return
	}
	registerWebMCPCommandTimeoutFlag(cmd, &values.commandTimeout)
}

func registerWebMCPCommandTimeoutFlag(cmd *cobra.Command, target *time.Duration) {
	if cmd == nil || target == nil {
		return
	}
	cmd.Flags().DurationVar(target, "command-timeout", DefaultWebMCPDirectCommandTimeout, "End-to-end WebMCP command bound (Go duration; zero uses the safe default)")
}

func directBrowserFlagChanged(cmd *cobra.Command) bool {
	return directFlagChanged(cmd, "browser", browserFlagPrefix+"browser")
}

func directFlagChanged(cmd *cobra.Command, names ...string) bool {
	if cmd == nil {
		return false
	}
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func registerWebMCPDirectBrowserFlags(cmd *cobra.Command, values *flags.BrowserFlags) {
	if cmd == nil || values == nil {
		return
	}
	stringFlag := func(target *string, name, usage string) {
		cmd.Flags().StringVar(target, name, "", usage)
	}
	boolFlag := func(target *bool, name, usage string) {
		bindStrictBrowserBool(cmd.Flags(), target, name, usage)
	}
	stringFlag(&values.CDPURL, "cdp-url", "Browser DevTools HTTP endpoint")
	stringFlag(&values.WSEndpoint, "ws-endpoint", "Browser DevTools WebSocket endpoint")
	stringFlag(&values.UserDataDir, "user-data-dir", "Browser profile directory used for DevTools discovery")
	boolFlag(&values.AllowProcessScan, "allow-process-scan", "Allow process-based browser endpoint discovery")
	boolFlag(&values.AllowRemoteCDP, "allow-remote-cdp", "Allow non-loopback DevTools endpoints")
	stringFlag(&values.Browser, "browser", "Exact normalized browser ID")
	stringFlag(&values.Tab, "tab", "Exact browser target ID")
	stringFlag(&values.Origin, "origin", "Exact browser page origin filter")
	stringFlag(&values.AutoSelect, "auto-select", "Browser target auto-selection: off, single, or persisted")
	boolFlag(&values.ActivateTab, "activate-tab", "Activate the selected browser tab")
	boolFlag(&values.PersistSelection, "persist-selection", "Persist the selected browser ID and target metadata")
	cmd.Flags().StringArrayVar(&values.AllowedOrigins, "allowed-origin", nil, "Allow an exact browser page origin (repeatable)")
	cmd.Flags().StringArrayVar(&values.DeniedOrigins, "denied-origin", nil, "Deny an exact browser page origin (repeatable)")
	stringFlag(&values.Approval, "approval", "Browser page approval policy: always, writes, or never")
	stringFlag(&values.CancelOnInterrupt, "cancel-on-interrupt", "Browser invocation cancellation policy: never, read-only, or always")
	cmd.Flags().DurationVar(&values.InvocationTimeout, "invocation-timeout", 0, "Maximum browser invocation duration (Go duration)")
	cmd.Flags().Var(&strictBrowserIntValue{target: &values.MaxInputBytes, name: "max-input-bytes"}, "max-input-bytes", "Maximum browser input_json bytes (decimal integer)")
	cmd.Flags().Var(&strictBrowserIntValue{target: &values.MaxResultBytes, name: "max-result-bytes"}, "max-result-bytes", "Maximum browser result bytes (decimal integer)")
	boolFlag(&values.SerializePerTarget, "serialize-per-target", "Serialize browser page calls per target")
}

// directOverride maps one changed browser flag onto its configuration
// override.
type directOverride struct {
	name  string
	apply func(*config.BrowserOverrides, *flags.BrowserFlags)
}

func directOverrides() []directOverride {
	return []directOverride{
		{"cdp-url", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.CDPURL = &v.CDPURL }},
		{"ws-endpoint", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.WSEndpoint = &v.WSEndpoint }},
		{"user-data-dir", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.UserDataDir = &v.UserDataDir }},
		{"allow-process-scan", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.AllowProcessScan = &v.AllowProcessScan }},
		{"allow-remote-cdp", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.AllowRemoteCDP = &v.AllowRemoteCDP }},
		{"browser", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.Browser = &v.Browser }},
		{"tab", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.Tab = &v.Tab }},
		{"origin", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.Origin = &v.Origin }},
		{"auto-select", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.AutoSelect = &v.AutoSelect }},
		{"activate-tab", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.ActivateTab = &v.ActivateTab }},
		{"persist-selection", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.PersistSelection = &v.PersistSelection }},
		{"allowed-origin", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.AllowedOrigins = &v.AllowedOrigins }},
		{"denied-origin", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.DeniedOrigins = &v.DeniedOrigins }},
		{"approval", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.Approval = &v.Approval }},
		{"cancel-on-interrupt", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.CancelOnInterrupt = &v.CancelOnInterrupt }},
		{"invocation-timeout", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.InvocationTimeout = &v.InvocationTimeout }},
		{"max-input-bytes", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.MaxInputBytes = &v.MaxInputBytes }},
		{"max-result-bytes", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.MaxResultBytes = &v.MaxResultBytes }},
		{"serialize-per-target", func(o *config.BrowserOverrides, v *flags.BrowserFlags) { o.SerializePerTarget = &v.SerializePerTarget }},
	}
}

// directBrowserOverrides applies only the browser flags the command line
// changed, under either spelling.
func directBrowserOverrides(cmd *cobra.Command, values *flags.BrowserFlags) config.BrowserOverrides {
	var overrides config.BrowserOverrides
	if cmd == nil || values == nil {
		return overrides
	}
	for _, override := range directOverrides() {
		if directFlagChanged(cmd, override.name, browserFlagPrefix+override.name) {
			override.apply(&overrides, values)
		}
	}
	return overrides
}
