package cli

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/spf13/cobra"
	"io"
)

const (
	doctorResultVersion = "webmcp.doctor.v1"

	doctorStatusReady                = "ready"
	doctorStatusNotReady             = "not_ready"
	doctorStatusUnavailable          = "unavailable"
	doctorStatusInvalidConfiguration = "invalid_configuration"
	doctorStatusCleanupError         = "cleanup_error"

	doctorCheckPass        = "pass"
	doctorCheckWarn        = "warning"
	doctorCheckFail        = "fail"
	doctorCheckUnavailable = "unavailable"
	doctorCheckSkipped     = "skipped"

	doctorErrorInvalidConfiguration = "invalid_configuration"
	doctorErrorCleanupFailed        = "cleanup_failed"

	doctorTestedChromeRow   = "Stable Chrome for Testing 152.0.7977.64 (mac-arm64, revision 1669021)"
	doctorTestedChromeFlags = "--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport"
)

// WebMCPDoctorVersionFunc supplies the browser version/protocol check. It is
// separate from the broker because the broker's stable interface deliberately
// contains only browser operations needed by model-facing tools.
type WebMCPDoctorCommand struct {
	globalFlags    *flags.GlobalFlags
	factory        WebMCPDoctorFactory
	json           bool
	browserFlags   *flags.BrowserFlags
	commandTimeout time.Duration
}

// NewWebMCPDoctorCommand constructs doctor with an optional injected runtime
// factory. The default uses the production discovery and browser runtime.
func NewWebMCPDoctorCommand(globalFlags *flags.GlobalFlags, factories ...WebMCPDoctorFactory) *WebMCPDoctorCommand {
	factory := defaultWebMCPDoctorFactory(globalFlags)
	if len(factories) > 0 && factories[0] != nil {
		factory = factories[0]
	}
	return &WebMCPDoctorCommand{
		globalFlags:  globalFlags,
		factory:      factory,
		browserFlags: flags.NewBrowserFlags(),
	}
}

// Generate returns the Cobra command for `yui webmcp doctor`.
func (c *WebMCPDoctorCommand) Generate() *cobra.Command {
	if c.browserFlags == nil {
		c.browserFlags = flags.NewBrowserFlags()
	}
	cmd := &cobra.Command{
		Use:          "doctor",
		Short:        "Diagnose WebMCP browser readiness",
		Long:         "Diagnose WebMCP browser readiness. Check browser configuration, endpoint reachability, target selection, WebMCP support, catalog readiness, and cleanup without starting a model session.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := c.diagnose(cmd.Context(), cmd)
			if writeErr := writeWebMCPDoctorReport(cmd.OutOrStdout(), report, c.json); writeErr != nil {
				return writeErr
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&c.json, "json", false, "Write one machine-readable JSON diagnostic object")
	registerWebMCPCommandTimeoutFlag(cmd, &c.commandTimeout)
	registerSessionBrowserFlags(cmd, c.browserFlags)
	return cmd
}

// Run executes doctor without command-line overrides. It is useful to
// embedding callers that already supplied the config directory on GlobalFlags.
func (c *WebMCPDoctorCommand) Run(ctx context.Context, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	report, err := c.diagnose(ctx, nil)
	if writeErr := writeWebMCPDoctorReport(out, report, c.json); writeErr != nil {
		return writeErr
	}
	return err
}

func (c *WebMCPDoctorCommand) diagnose(ctx context.Context, cmd *cobra.Command) (report WebMCPDoctorReport, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report = newWebMCPDoctorReport()

	commandTimeout := c.commandTimeout
	if commandTimeout == 0 {
		commandTimeout = DefaultWebMCPDirectCommandTimeout
	}

	var (
		primary        error
		factoryRuntime WebMCPDoctorRuntime
		factoryErr     error
		runtimeOwned   bool
	)
	if commandTimeout < 0 {
		primary = directInvalidInputError("--command-timeout must not be negative", "/command_timeout")
		report.Status = doctorStatusInvalidConfiguration
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorInvalidToolInput, nil)
		report.setCheck("configuration", doctorCheckFail, "The WebMCP command timeout is invalid.", map[string]any{"phase": "command_timeout"})
		return report, primary
	}
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	ctx = commandCtx

	defer func() {
		if runtimeOwned {
			closeErr := closeWebMCPDoctorRuntimeBounded(factoryRuntime)
			if closeErr != nil {
				report.setCheck("cleanup", doctorCheckFail, "Diagnostic cleanup failed.", map[string]any{"phase": "cleanup"})
				if primary == nil {
					primary = closeErr
					report.Status = doctorStatusCleanupError
					report.Error = &WebMCPDoctorErrorData{
						Code:      doctorErrorCleanupFailed,
						Message:   "WebMCP doctor cleanup failed.",
						Retryable: false,
						Details:   map[string]any{"phase": "cleanup"},
					}
				} else {
					primary = errors.Join(primary, closeErr)
					if report.Error == nil {
						report.Error = &WebMCPDoctorErrorData{
							Code:      doctorErrorCleanupFailed,
							Message:   "WebMCP doctor cleanup failed.",
							Retryable: false,
							Details:   map[string]any{"phase": "cleanup"},
						}
					} else if report.Error.Details == nil {
						report.Error.Details = map[string]any{}
					}
					if report.Error != nil {
						report.Error.Details["cleanup_error"] = true
					}
				}
			} else {
				report.setCheck("cleanup", doctorCheckPass, "Diagnostic runtime cleanup completed.", nil)
			}
		}
		if primary != nil {
			err = &WebMCPDoctorError{Report: report, Cause: primary}
		}
	}()

	loaded, loadErr := resolveSessionBrowserConfig(c.globalFlags, cmd, c.browserFlags)
	if loadErr != nil {
		primary = loadErr
		report.Status = doctorStatusInvalidConfiguration
		report.Error = &WebMCPDoctorErrorData{
			Code:      doctorErrorInvalidConfiguration,
			Message:   "Browser configuration is invalid; fix the browser YAML, AGENT_BROWSER__... value, or --browser-* flag.",
			Retryable: false,
			Details:   map[string]any{"phase": "configuration"},
		}
		report.setCheck("configuration", doctorCheckFail, "Browser configuration is invalid.", map[string]any{"phase": "configuration"})
		return report, nil
	}
	if loaded == nil {
		primary = errors.New("browser configuration loader returned nil config")
		report.Status = doctorStatusInvalidConfiguration
		report.Error = &WebMCPDoctorErrorData{
			Code:      doctorErrorInvalidConfiguration,
			Message:   "Browser configuration could not be loaded.",
			Retryable: false,
			Details:   map[string]any{"phase": "configuration"},
		}
		report.setCheck("configuration", doctorCheckFail, "Browser configuration could not be loaded.", nil)
		return report, nil
	}
	report.Endpoint = doctorEndpointFor(loaded.Browser)
	report.setCheck("configuration", doctorCheckPass, "Browser configuration parsed and validated.", nil)
	if loaded.Browser.BrowserBackendEnabled() {
		report.setCheck("activation", doctorCheckPass, "WebMCP is enabled for model sessions.", map[string]any{"backend": loaded.Browser.Tools.Backend})
	} else {
		report.setCheck("activation", doctorCheckWarn, "WebMCP is disabled for model sessions; direct doctor checks remain active.", map[string]any{"backend": loaded.Browser.Tools.Backend, "enabled": false})
		report.addWarning("WebMCP is not enabled for model sessions; use --browser-tools=webmcp with agent session to admit the capability.")
	}

	if endpointErr := validateDoctorEndpoints(loaded.Browser); endpointErr != nil {
		primary = endpointErr
		report.Status = doctorStatusInvalidConfiguration
		report.Error = doctorErrorDataFor(endpointErr, webmcp.ErrorBrowserProtocol, map[string]any{"phase": "endpoint"})
		report.setCheck("endpoint", doctorCheckFail, "The configured browser endpoint is invalid.", map[string]any{"phase": "endpoint"})
		return report, nil
	}
	if report.Endpoint.Scope == "non_loopback" && !loaded.Browser.Connection.AllowRemoteCDP {
		primary = webmcp.NewClassifiedError(webmcp.ErrorRemoteEndpointDenied, "remote browser endpoints require explicit permission", map[string]any{
			"endpoint_kind": endpointKindFor(loaded.Browser),
			"network_class": "non_loopback",
			"required_flag": "browser-allow-remote-cdp",
		})
		report.Status = doctorStatusNotReady
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorRemoteEndpointDenied, nil)
		report.setCheck("endpoint", doctorCheckFail, "The endpoint is non-loopback and remote CDP permission is disabled.", map[string]any{"required_flag": "browser-allow-remote-cdp"})
		return report, nil
	}
	report.setCheck("endpoint", doctorCheckPass, "Endpoint policy permits the configured discovery scope.", map[string]any{"scope": report.Endpoint.Scope})

	factory := c.factory
	if factory == nil {
		factory = defaultWebMCPDoctorFactory(c.globalFlags)
	}
	factoryRuntime, factoryErr = constructWebMCPDoctorRuntime(ctx, factory, loaded.Browser)
	runtimeOwned = factoryRuntime.Close != nil || factoryRuntime.Broker != nil
	if factoryErr != nil {
		primary = directRuntimeFactoryFailure(factoryErr)
		report.Status = doctorStatusUnavailable
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorBrowserProtocol, map[string]any{"phase": "runtime_factory"})
		report.setCheck("discovery", doctorCheckUnavailable, "The diagnostic runtime could not be constructed.", map[string]any{"phase": "runtime_factory"})
		return report, nil
	}
	if factoryRuntime.Broker == nil {
		primary = webmcpRuntimeUnavailableError("runtime_factory")
		report.Status = doctorStatusUnavailable
		report.Error = doctorErrorDataFor(primary, webmcp.ErrorBrowserProtocol, nil)
		report.setCheck("discovery", doctorCheckUnavailable, "The diagnostic runtime returned no broker.", nil)
		return report, nil
	}

	primary = diagnoseWebMCPDoctorRuntime(ctx, loaded.Browser, factoryRuntime, &report)
	return report, nil
}
