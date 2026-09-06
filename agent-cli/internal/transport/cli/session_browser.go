package cli

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	hostServices "github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ErrInvalidBrowserToolsBackend identifies a session --browser-tools value
// that is not part of the frozen capability contract.
var ErrInvalidBrowserToolsBackend = errors.New("invalid browser tools backend")

// BrowserToolsBackendError reports an unsupported --browser-tools backend
// before provider or browser setup begins.
type BrowserToolsBackendError struct {
	Value string
}

func (e *BrowserToolsBackendError) Error() string {
	if e == nil {
		return ErrInvalidBrowserToolsBackend.Error()
	}
	return fmt.Sprintf("--browser-tools supports backend %q; got %q", config.BrowserToolsBackendWebMCP, e.Value)
}

func (e *BrowserToolsBackendError) Unwrap() error { return ErrInvalidBrowserToolsBackend }

// sessionBrowserFlagNames is the complete session browser flag set. Keeping
// the list next to registration makes presence-aware config loading auditable
// and prevents a new browser flag from being omitted from config resolution.
var sessionBrowserFlagNames = []string{
	"browser-tools",
	"web-cast",
	"browser-cdp-url",
	"browser-ws-endpoint",
	"browser-user-data-dir",
	"browser-headless",
	"browser-open",
	"browser-close-on-exit",
	"browser-allow-process-scan",
	"browser-allow-remote-cdp",
	"browser-browser",
	"browser-tab",
	"browser-origin",
	"browser-auto-select",
	"browser-activate-tab",
	"browser-persist-selection",
	"browser-allowed-origin",
	"browser-denied-origin",
	"browser-approval",
	"browser-cancel-on-interrupt",
	"browser-invocation-timeout",
	"browser-max-input-bytes",
	"browser-max-result-bytes",
	"browser-serialize-per-target",
	"browser-record",
	"browser-record-arguments",
	"browser-record-results",
	"browser-redact-url-query",
	"browser-redact-url-fragment",
	"browser-replay",
	"browser-replay-strict",
}

func hasSessionBrowserFlag(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	for _, name := range sessionBrowserFlagNames {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func validateBrowserToolsBackend(value string, provided bool) error {
	if !provided {
		return nil
	}
	if value != config.BrowserToolsBackendWebMCP {
		return &BrowserToolsBackendError{Value: value}
	}
	return nil
}

func browserOverridesFromFlags(cmd *cobra.Command, values *flags.BrowserFlags) config.BrowserOverrides {
	var overrides config.BrowserOverrides
	if cmd == nil || values == nil {
		return overrides
	}
	changed := func(name string) bool { return cmd.Flags().Changed(name) }
	if changed("browser-tools") {
		overrides.ToolsBackend = &values.Tools
	}
	if changed("web-cast") {
		overrides.WebCast = &values.WebCast
	}
	if changed("browser-cdp-url") {
		overrides.CDPURL = &values.CDPURL
	}
	if changed("browser-ws-endpoint") {
		overrides.WSEndpoint = &values.WSEndpoint
	}
	if changed("browser-user-data-dir") {
		overrides.UserDataDir = &values.UserDataDir
	}
	if changed("browser-headless") {
		overrides.ManagedHeadless = &values.Headless
	}
	if changed("browser-open") {
		overrides.ManagedOpen = &values.Open
	}
	if changed("browser-close-on-exit") {
		overrides.ManagedCloseOnExit = &values.CloseOnExit
	}
	if changed("browser-allow-process-scan") {
		overrides.AllowProcessScan = &values.AllowProcessScan
	}
	if changed("browser-allow-remote-cdp") {
		overrides.AllowRemoteCDP = &values.AllowRemoteCDP
	}
	if changed("browser-browser") {
		overrides.Browser = &values.Browser
	}
	if changed("browser-tab") {
		overrides.Tab = &values.Tab
	}
	if changed("browser-origin") {
		overrides.Origin = &values.Origin
	}
	if changed("browser-auto-select") {
		overrides.AutoSelect = &values.AutoSelect
	}
	if changed("browser-activate-tab") {
		overrides.ActivateTab = &values.ActivateTab
	}
	if changed("browser-persist-selection") {
		overrides.PersistSelection = &values.PersistSelection
	}
	if changed("browser-allowed-origin") {
		overrides.AllowedOrigins = &values.AllowedOrigins
	}
	if changed("browser-denied-origin") {
		overrides.DeniedOrigins = &values.DeniedOrigins
	}
	if changed("browser-approval") {
		overrides.Approval = &values.Approval
	}
	if changed("browser-cancel-on-interrupt") {
		overrides.CancelOnInterrupt = &values.CancelOnInterrupt
	}
	if changed("browser-invocation-timeout") {
		overrides.InvocationTimeout = &values.InvocationTimeout
	}
	if changed("browser-max-input-bytes") {
		overrides.MaxInputBytes = &values.MaxInputBytes
	}
	if changed("browser-max-result-bytes") {
		overrides.MaxResultBytes = &values.MaxResultBytes
	}
	if changed("browser-serialize-per-target") {
		overrides.SerializePerTarget = &values.SerializePerTarget
	}
	if changed("browser-record") {
		overrides.Record = &values.Record
	}
	if changed("browser-record-arguments") {
		overrides.RecordArguments = &values.RecordArguments
	}
	if changed("browser-record-results") {
		overrides.RecordResults = &values.RecordResults
	}
	if changed("browser-redact-url-query") {
		overrides.RedactURLQuery = &values.RedactURLQuery
	}
	if changed("browser-redact-url-fragment") {
		overrides.RedactURLFragment = &values.RedactURLFragment
	}
	if changed("browser-replay") {
		overrides.Replay = &values.Replay
	}
	if changed("browser-replay-strict") {
		overrides.ReplayStrict = &values.ReplayStrict
	}
	return overrides
}

// resolveSessionBrowserConfig loads the normal defaults/YAML/environment
// snapshot, then applies only flags present on cmd. It returns a copied Config
// so a command-specific selection cannot mutate ConfigStorage's cache.
func resolveSessionBrowserConfig(globalFlags *flags.GlobalFlags, cmd *cobra.Command, browserFlags *flags.BrowserFlags) (*config.Config, error) {
	configDir := ""
	if globalFlags != nil {
		configDir = globalFlags.ConfigDir()
	}
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("load session config: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load session config: %w", err)
	}
	resolvedBrowser, err := loaded.Browser.ApplyBrowserOverrides(browserOverridesFromFlags(cmd, browserFlags))
	if err != nil {
		return nil, fmt.Errorf("resolve browser flags: %w", err)
	}
	resolved := *loaded
	resolved.Browser = resolvedBrowser
	resolved.FilesystemWorkDir, err = hostServices.ResolveCLIWorkDir(globalFlags)
	if err != nil {
		return nil, err
	}
	// Keep the command's config-directory override attached to this resolved
	// snapshot so request-scoped browser persistence follows the same -C path.
	resolved.ConfigDir = configDir
	return &resolved, nil
}

// strictBrowserBoolValue prevents pflag's permissive bool spellings (1, t,
// and their case variants) from widening the C0 true/false contract.
type strictBrowserBoolValue struct {
	target *bool
	name   string
}

func (v *strictBrowserBoolValue) String() string {
	if v == nil || v.target == nil {
		return "false"
	}
	return strconv.FormatBool(*v.target)
}

func (v *strictBrowserBoolValue) Set(raw string) error {
	switch raw {
	case "true":
		*v.target = true
	case "false":
		*v.target = false
	default:
		return fmt.Errorf("--%s must be true or false; got %q", v.name, raw)
	}
	return nil
}

func (*strictBrowserBoolValue) Type() string { return "bool" }

func bindStrictBrowserBool(flagSet *pflag.FlagSet, target *bool, name, usage string) {
	flagSet.Var(&strictBrowserBoolValue{target: target, name: name}, name, usage)
	flagSet.Lookup(name).NoOptDefVal = "true"
}

// singleBrowserOpenValue prevents a repeatable command source from silently
// replacing the requested startup page with its last value.
type singleBrowserOpenValue struct {
	target *string
	name   string
	seen   bool
}

func (v *singleBrowserOpenValue) String() string {
	if v == nil || v.target == nil {
		return ""
	}
	return *v.target
}

func (v *singleBrowserOpenValue) Set(raw string) error {
	if v.seen {
		return fmt.Errorf("--%s accepts at most one startup URL", v.name)
	}
	v.seen = true
	*v.target = raw
	return nil
}

func (*singleBrowserOpenValue) Type() string { return "url" }

// strictBrowserIntValue accepts only non-negative decimal integers. This is
// stricter than pflag's base-0 integer parser, which would also accept hex.
type strictBrowserIntValue struct {
	target *int
	name   string
}

func (v *strictBrowserIntValue) String() string {
	if v == nil || v.target == nil {
		return "0"
	}
	return strconv.Itoa(*v.target)
}

func (v *strictBrowserIntValue) Set(raw string) error {
	if raw == "" {
		return fmt.Errorf("--%s must be a non-negative decimal integer; got empty value", v.name)
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return fmt.Errorf("--%s must be a non-negative decimal integer; got %q", v.name, raw)
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("--%s must be a non-negative decimal integer: %w", v.name, err)
	}
	*v.target = value
	return nil
}

func (*strictBrowserIntValue) Type() string { return "int" }

func registerSessionBrowserFlags(cmd *cobra.Command, values *flags.BrowserFlags) {
	cmd.Flags().StringVar(&values.Tools, "browser-tools", "", "Enable WebMCP browser tools; without an endpoint, the agent manages a local Chrome")
	bindStrictBrowserBool(cmd.Flags(), &values.WebCast, "web-cast", "Enable native-media and selected-tab Google Cast controls; requires --browser-tools webmcp")
	cmd.Flags().StringVar(&values.CDPURL, "browser-cdp-url", "", "Browser DevTools HTTP endpoint")
	cmd.Flags().StringVar(&values.WSEndpoint, "browser-ws-endpoint", "", "Browser DevTools WebSocket endpoint")
	cmd.Flags().StringVar(&values.UserDataDir, "browser-user-data-dir", "", "Browser profile directory used for DevTools discovery")
	bindStrictBrowserBool(cmd.Flags(), &values.Headless, "browser-headless", "Use headless mode for an agent-managed browser")
	cmd.Flags().Var(&singleBrowserOpenValue{target: &values.Open, name: "browser-open"}, "browser-open", "Open exactly one startup URL in an agent-managed browser (default: about:blank)")
	bindStrictBrowserBool(cmd.Flags(), &values.CloseOnExit, "browser-close-on-exit", "Close the exact agent-managed browser when the session exits; external endpoints are never closed")
	bindStrictBrowserBool(cmd.Flags(), &values.AllowProcessScan, "browser-allow-process-scan", "Allow process-based browser endpoint discovery")
	bindStrictBrowserBool(cmd.Flags(), &values.AllowRemoteCDP, "browser-allow-remote-cdp", "Allow non-loopback DevTools endpoints")
	cmd.Flags().StringVar(&values.Browser, "browser-browser", "", "Exact normalized browser ID")
	cmd.Flags().StringVar(&values.Tab, "browser-tab", "", "Exact browser target ID")
	cmd.Flags().StringVar(&values.Origin, "browser-origin", "", "Exact browser page origin filter")
	cmd.Flags().StringVar(&values.AutoSelect, "browser-auto-select", "", "Browser target auto-selection: off, single, or persisted")
	bindStrictBrowserBool(cmd.Flags(), &values.ActivateTab, "browser-activate-tab", "Activate the selected browser tab")
	bindStrictBrowserBool(cmd.Flags(), &values.PersistSelection, "browser-persist-selection", "Persist the selected browser ID and target metadata")
	cmd.Flags().StringArrayVar(&values.AllowedOrigins, "browser-allowed-origin", nil, "Allow an exact browser page origin (repeatable)")
	cmd.Flags().StringArrayVar(&values.DeniedOrigins, "browser-denied-origin", nil, "Deny an exact browser page origin (repeatable)")
	cmd.Flags().StringVar(&values.Approval, "browser-approval", "", "Browser page approval policy: always, writes, or never")
	cmd.Flags().StringVar(&values.CancelOnInterrupt, "browser-cancel-on-interrupt", "", "Browser invocation cancellation policy: never, read-only, or always")
	cmd.Flags().DurationVar(&values.InvocationTimeout, "browser-invocation-timeout", 0, "Maximum browser invocation duration (Go duration)")
	cmd.Flags().Var(&strictBrowserIntValue{target: &values.MaxInputBytes, name: "browser-max-input-bytes"}, "browser-max-input-bytes", "Maximum browser input_json bytes (decimal integer)")
	cmd.Flags().Var(&strictBrowserIntValue{target: &values.MaxResultBytes, name: "browser-max-result-bytes"}, "browser-max-result-bytes", "Maximum browser result bytes (decimal integer)")
	bindStrictBrowserBool(cmd.Flags(), &values.SerializePerTarget, "browser-serialize-per-target", "Serialize browser page calls per target")
	bindStrictBrowserBool(cmd.Flags(), &values.Record, "browser-record", "Record semantic browser events with the session capture")
	bindStrictBrowserBool(cmd.Flags(), &values.RecordArguments, "browser-record-arguments", "Include redacted browser tool arguments in recording")
	bindStrictBrowserBool(cmd.Flags(), &values.RecordResults, "browser-record-results", "Include redacted browser tool results in recording")
	bindStrictBrowserBool(cmd.Flags(), &values.RedactURLQuery, "browser-redact-url-query", "Redact query data from browser URLs")
	bindStrictBrowserBool(cmd.Flags(), &values.RedactURLFragment, "browser-redact-url-fragment", "Redact fragment data from browser URLs")
	cmd.Flags().StringVar(&values.Replay, "browser-replay", "", "Browser semantic replay fixture path")
	bindStrictBrowserBool(cmd.Flags(), &values.ReplayStrict, "browser-replay-strict", "Reject divergent browser replay events")
}

// browserToolsAdmission reports only the explicit command-line activation
// trigger. Config activation enables capabilities after another session mode
// admits the command, but it does not turn a bare session invocation into a
// live run.
func browserToolsAdmission(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Flags().Changed("browser-tools")
}

func browserConfigEnablesTools(cfg *config.Config) bool {
	return cfg != nil && cfg.Browser.BrowserBackendEnabled()
}

// NewSessionBrowserBroker creates the production browser broker used by
// browser-enabled sessions. The runtime is request-scoped so session cleanup
// can retire both broker state and discovery resources through one idempotent
// close hook.
func NewSessionBrowserBroker(browser config.BrowserConfig) (webmcp.Broker, error) {
	return newSessionBrowserBrokerWithConfigDir(browser, "")
}

// NewSessionBrowserBrokerWithConfigDir constructs one request-scoped browser
// runtime with the config directory used for persisted selection state.
func NewSessionBrowserBrokerWithConfigDir(browser config.BrowserConfig, configDir string) (webmcp.Broker, error) {
	return newSessionBrowserBrokerWithConfigDir(browser, configDir)
}

func newSessionBrowserBrokerWithConfigDir(browser config.BrowserConfig, configDir string) (webmcp.Broker, error) {
	return newSessionBrowserBrokerWithDoctorFactory(browser, NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionConfigDir(configDir),
		WithWebMCPProductionSelectionStoreFactory(func() any {
			return NewFileWebMCPSelectionStore(configDir)
		}),
	))
}
