package doctor

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
)

const (
	requiredRemoteFlag = "browser-allow-remote-cdp"
	keyRequiredFlag    = "required_flag"
	messageRemoteCDP   = "remote browser endpoints require explicit permission"
)

// Request is one doctor run. LoadConfig resolves the browser configuration
// at the caller's boundary (files, environment, flags); Factory constructs
// the diagnostic runtime only after configuration and endpoint policy pass.
type Request struct {
	// CommandTimeout bounds the whole run. Zero selects
	// direct.DefaultCommandTimeout; a negative value is invalid input.
	CommandTimeout time.Duration
	LoadConfig     func() (*config.Config, error)
	Factory        direct.Factory
}

// Diagnose runs every doctor check and returns the report. A failed check
// yields an *Error that carries the report and the classified cause; a
// negative CommandTimeout returns the invalid-input cause directly.
func Diagnose(ctx context.Context, request Request) (Report, error) { //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	if ctx == nil {
		ctx = context.Background()
	}
	report := NewReport()
	commandTimeout := request.CommandTimeout
	if commandTimeout == 0 {
		commandTimeout = direct.DefaultCommandTimeout
	}
	if commandTimeout < 0 {
		primary := direct.InvalidInputError("--command-timeout must not be negative", "/command_timeout")
		report.fail(StatusInvalidConfiguration, primary, webmcp.ErrorInvalidToolInput, nil)
		report.setCheck(checkConfiguration, CheckFail, "The WebMCP command timeout is invalid.", phaseDetails("command_timeout"))
		return report, primary
	}
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	runtime, primary := diagnoseConfigured(commandCtx, request, &report)
	if runtime.Owned() {
		primary = recordCleanup(&report, direct.CloseRuntimeBounded(runtime), primary)
	}
	if primary != nil {
		return report, &Error{Report: report, Cause: primary}
	}
	return report, nil
}

// diagnoseConfigured runs the configuration, endpoint, runtime construction,
// and runtime checks. It returns the constructed runtime, which the caller
// owns and closes, together with the primary failure.
func diagnoseConfigured(ctx context.Context, request Request, report *Report) (direct.Runtime, error) {
	browser, primary := loadConfiguration(request.LoadConfig, report)
	if primary != nil {
		return direct.Runtime{}, primary
	}
	if primary := checkEndpointPolicy(browser, report); primary != nil {
		return direct.Runtime{}, primary
	}
	runtime, factoryErr := direct.ConstructRuntime(ctx, request.Factory, browser)
	if factoryErr != nil {
		primary := direct.RuntimeFactoryFailure(factoryErr)
		report.fail(StatusUnavailable, primary, webmcp.ErrorBrowserProtocol, phaseDetails("runtime_factory"))
		report.setCheck(checkDiscovery, CheckUnavailable, "The diagnostic runtime could not be constructed.", phaseDetails("runtime_factory"))
		return runtime, primary
	}
	if runtime.Broker == nil {
		primary := direct.RuntimeUnavailableError("runtime_factory")
		report.fail(StatusUnavailable, primary, webmcp.ErrorBrowserProtocol, nil)
		report.setCheck(checkDiscovery, CheckUnavailable, "The diagnostic runtime returned no broker.", nil)
		return runtime, primary
	}
	return runtime, diagnoseRuntime(ctx, browser, runtime, report)
}

// loadConfiguration resolves the browser configuration and records the
// configuration, endpoint description, and activation checks.
func loadConfiguration(load func() (*config.Config, error), report *Report) (config.BrowserConfig, error) {
	if load == nil {
		load = func() (*config.Config, error) { return nil, nil }
	}
	loaded, loadErr := load()
	if loadErr != nil {
		report.Status = StatusInvalidConfiguration
		report.Error = invalidConfigurationData("Browser configuration is invalid; fix the browser YAML, AGENT_BROWSER__... value, or --browser-* flag.")
		report.setCheck(checkConfiguration, CheckFail, "Browser configuration is invalid.", phaseDetails(checkConfiguration))
		return config.BrowserConfig{}, loadErr
	}
	if loaded == nil {
		report.Status = StatusInvalidConfiguration
		report.Error = invalidConfigurationData("Browser configuration could not be loaded.")
		report.setCheck(checkConfiguration, CheckFail, "Browser configuration could not be loaded.", nil)
		return config.BrowserConfig{}, errors.New("browser configuration loader returned nil config")
	}
	browser := loaded.Browser
	report.Endpoint = EndpointFor(browser)
	report.setCheck(checkConfiguration, CheckPass, "Browser configuration parsed and validated.", nil)
	if browser.BrowserBackendEnabled() {
		report.setCheck(checkActivation, CheckPass, "WebMCP is enabled for model sessions.", map[string]any{"backend": browser.Tools.Backend})
	} else {
		report.setCheck(checkActivation, CheckWarn, "WebMCP is disabled for model sessions; direct doctor checks remain active.", map[string]any{"backend": browser.Tools.Backend, "enabled": false})
		report.addWarning("WebMCP is not enabled for model sessions; use --browser-tools=webmcp with agent session to admit the capability.")
	}
	return browser, nil
}

func invalidConfigurationData(message string) *ErrorData {
	return &ErrorData{
		Code:      ErrorInvalidConfiguration,
		Message:   message,
		Retryable: false,
		Details:   phaseDetails(checkConfiguration),
	}
}

// checkEndpointPolicy validates the configured endpoints and the remote-CDP
// permission for a non-loopback endpoint.
func checkEndpointPolicy(browser config.BrowserConfig, report *Report) error {
	if endpointErr := ValidateEndpoints(browser); endpointErr != nil {
		report.fail(StatusInvalidConfiguration, endpointErr, webmcp.ErrorBrowserProtocol, phaseDetails(checkEndpoint))
		report.setCheck(checkEndpoint, CheckFail, "The configured browser endpoint is invalid.", phaseDetails(checkEndpoint))
		return endpointErr
	}
	if report.Endpoint.Scope == scopeNonLoopback && !browser.Connection.AllowRemoteCDP {
		primary := remoteEndpointDeniedError(browser)
		report.notReady(primary, webmcp.ErrorRemoteEndpointDenied, nil)
		report.setCheck(checkEndpoint, CheckFail, "The endpoint is non-loopback and remote CDP permission is disabled.", map[string]any{keyRequiredFlag: requiredRemoteFlag})
		return primary
	}
	report.setCheck(checkEndpoint, CheckPass, "Endpoint policy permits the configured discovery scope.", map[string]any{"scope": report.Endpoint.Scope})
	return nil
}

func remoteEndpointDeniedError(browser config.BrowserConfig) error {
	return webmcp.NewClassifiedError(webmcp.ErrorRemoteEndpointDenied, messageRemoteCDP, map[string]any{
		"endpoint_kind": direct.EndpointKind(browser),
		"network_class": scopeNonLoopback,
		keyRequiredFlag: requiredRemoteFlag,
	})
}

// recordCleanup records the runtime cleanup outcome and folds a cleanup
// failure into the primary error.
func recordCleanup(report *Report, closeErr, primary error) error {
	if closeErr == nil {
		report.setCheck(checkCleanup, CheckPass, "Diagnostic runtime cleanup completed.", nil)
		return primary
	}
	report.setCheck(checkCleanup, CheckFail, "Diagnostic cleanup failed.", phaseDetails(checkCleanup))
	if primary == nil {
		report.Status = StatusCleanupError
		report.Error = cleanupFailedData()
		return closeErr
	}
	if report.Error == nil {
		report.Error = cleanupFailedData()
	} else if report.Error.Details == nil {
		report.Error.Details = map[string]any{}
	}
	report.Error.Details["cleanup_error"] = true
	return errors.Join(primary, closeErr)
}

func cleanupFailedData() *ErrorData {
	return &ErrorData{
		Code:      ErrorCleanupFailed,
		Message:   "WebMCP doctor cleanup failed.",
		Retryable: false,
		Details:   phaseDetails(checkCleanup),
	}
}
