package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/spf13/cobra"
)

// ProbeScenarioV2BrowserExecutorMode selects the browser composition used by
// a probe.scenario.v2 run. Hermetic is deliberately the zero value so an
// existing probe invocation cannot acquire a browser connection implicitly.
type ProbeScenarioV2BrowserExecutorMode string

const (
	ProbeScenarioV2BrowserExecutorHermetic ProbeScenarioV2BrowserExecutorMode = "hermetic"
	ProbeScenarioV2BrowserExecutorReal     ProbeScenarioV2BrowserExecutorMode = "real"
)

// ErrInvalidProbeScenarioV2BrowserExecutorMode identifies an unsupported
// browser executor selection before any scenario or browser work begins.
var ErrInvalidProbeScenarioV2BrowserExecutorMode = errors.New("invalid probe.scenario.v2 browser executor mode")

// ErrProbeScenarioV2RealAdapterUnavailable identifies a real-mode prerequisite
// failure. It is intentionally separate from the transport-specific cause so
// callers can classify the failure without depending on Chrome or CDP.
var ErrProbeScenarioV2RealAdapterUnavailable = errors.New("probe.scenario.v2 real browser adapter unavailable")

// ParseProbeScenarioV2BrowserExecutorMode validates the explicit probe mode.
func ParseProbeScenarioV2BrowserExecutorMode(raw string) (ProbeScenarioV2BrowserExecutorMode, error) {
	mode := ProbeScenarioV2BrowserExecutorMode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case ProbeScenarioV2BrowserExecutorHermetic, ProbeScenarioV2BrowserExecutorReal:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: got %q; want %q or %q", ErrInvalidProbeScenarioV2BrowserExecutorMode, raw, ProbeScenarioV2BrowserExecutorHermetic, ProbeScenarioV2BrowserExecutorReal)
	}
}

// ProbeScenarioV2BrowserExecutorOptions is the typed composition boundary for
// the browser executor. Factory is used only for real mode; hermetic mode
// always constructs the transport-free testkit runtime in this package.
type ProbeScenarioV2BrowserExecutorOptions struct {
	Mode        ProbeScenarioV2BrowserExecutorMode
	Factory     WebMCPDoctorFactory
	Browser     config.BrowserConfig
	ConfigError error
}

// ProbeScenarioV2BrowserExecutorOption customizes one v2 browser executor.
type ProbeScenarioV2BrowserExecutorOption func(*ProbeScenarioV2BrowserExecutorOptions)

type probeScenarioV2BrowserExecutorModeValue struct {
	target *ProbeScenarioV2BrowserExecutorMode
}

func (v *probeScenarioV2BrowserExecutorModeValue) String() string {
	if v == nil || v.target == nil || *v.target == "" {
		return string(ProbeScenarioV2BrowserExecutorHermetic)
	}
	return string(*v.target)
}

func (v *probeScenarioV2BrowserExecutorModeValue) Set(raw string) error {
	mode, err := ParseProbeScenarioV2BrowserExecutorMode(raw)
	if err != nil {
		return err
	}
	if v == nil || v.target == nil {
		return errors.New("probe.scenario.v2 browser executor mode target is nil")
	}
	*v.target = mode
	return nil
}

func (*probeScenarioV2BrowserExecutorModeValue) Type() string { return "hermetic|real" }

// WithProbeScenarioV2BrowserExecutorMode selects hermetic or real execution.
func WithProbeScenarioV2BrowserExecutorMode(mode ProbeScenarioV2BrowserExecutorMode) ProbeScenarioV2BrowserExecutorOption {
	return func(options *ProbeScenarioV2BrowserExecutorOptions) {
		options.Mode = mode
	}
}

// WithProbeScenarioV2BrowserExecutorFactory supplies the real browser
// composition. The factory is never consulted by hermetic execution.
func WithProbeScenarioV2BrowserExecutorFactory(factory WebMCPDoctorFactory) ProbeScenarioV2BrowserExecutorOption {
	return func(options *ProbeScenarioV2BrowserExecutorOptions) {
		options.Factory = factory
	}
}

// WithProbeScenarioV2BrowserExecutorConfig supplies the resolved browser
// configuration for a real run.
func WithProbeScenarioV2BrowserExecutorConfig(browser config.BrowserConfig) ProbeScenarioV2BrowserExecutorOption {
	return func(options *ProbeScenarioV2BrowserExecutorOptions) {
		options.Browser = browser
	}
}

// WithProbeScenarioV2BrowserExecutorConfigError preserves configuration load
// failures for the per-scenario result without attempting a fallback runtime.
func WithProbeScenarioV2BrowserExecutorConfigError(err error) ProbeScenarioV2BrowserExecutorOption {
	return func(options *ProbeScenarioV2BrowserExecutorOptions) {
		options.ConfigError = err
	}
}

func resolveProbeScenarioV2BrowserExecutorOptions(options ...ProbeScenarioV2BrowserExecutorOption) (ProbeScenarioV2BrowserExecutorOptions, error) {
	resolved := ProbeScenarioV2BrowserExecutorOptions{Mode: ProbeScenarioV2BrowserExecutorHermetic}
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}
	if resolved.Mode == "" {
		resolved.Mode = ProbeScenarioV2BrowserExecutorHermetic
	}
	if _, err := ParseProbeScenarioV2BrowserExecutorMode(string(resolved.Mode)); err != nil {
		return ProbeScenarioV2BrowserExecutorOptions{}, err
	}
	return resolved, nil
}

// ProbeScenarioV2BrowserExecutorError is a safe, actionable classification for
// real-adapter construction and operation failures. It deliberately excludes
// the underlying error text because browser errors can contain endpoint or
// transport details that do not belong in probe output.
type ProbeScenarioV2BrowserExecutorError struct {
	Mode  ProbeScenarioV2BrowserExecutorMode
	Code  webmcp.ErrorCode
	Phase string
	Cause error
}

func (e *ProbeScenarioV2BrowserExecutorError) Error() string {
	if e == nil {
		return ErrProbeScenarioV2RealAdapterUnavailable.Error()
	}
	return fmt.Sprintf("probe.scenario.v2 browser executor %q failed: code=%s phase=%s; %s", e.Mode, e.Code, e.Phase, probeScenarioV2BrowserAction(e.Code))
}

func (e *ProbeScenarioV2BrowserExecutorError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause == nil {
		return ErrProbeScenarioV2RealAdapterUnavailable
	}
	return errors.Join(ErrProbeScenarioV2RealAdapterUnavailable, e.Cause)
}

func newProbeScenarioV2BrowserExecutorError(mode ProbeScenarioV2BrowserExecutorMode, phase string, fallback webmcp.ErrorCode, cause error) error {
	code := fallback
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil && webmcp.IsKnownErrorCode(classified.Code) {
		code = classified.Code
	}
	if !webmcp.IsKnownErrorCode(code) {
		code = webmcp.ErrorBrowserProtocol
	}
	if phase == "" {
		phase = "browser"
	}
	return &ProbeScenarioV2BrowserExecutorError{Mode: mode, Code: code, Phase: phase, Cause: cause}
}

func probeScenarioV2BrowserOperationError(mode ProbeScenarioV2BrowserExecutorMode, phase string, cause error) error {
	if cause == nil {
		return nil
	}
	var executorErr *ProbeScenarioV2BrowserExecutorError
	if errors.As(cause, &executorErr) {
		return cause
	}
	fallback := webmcp.ErrorBrowserProtocol
	if errors.Is(cause, webmcp.ErrClosed) || errors.Is(cause, webmcp.ErrBrowserNotFound) {
		fallback = webmcp.ErrorBrowserDisconnected
	}
	if errors.Is(cause, context.Canceled) {
		fallback = webmcp.ErrorInvocationCanceled
	}
	return newProbeScenarioV2BrowserExecutorError(mode, phase, fallback, cause)
}

func probeScenarioV2BrowserAction(code webmcp.ErrorCode) string {
	switch code {
	case webmcp.ErrorEndpointNotFound:
		return "configure an explicit browser endpoint or start the browser"
	case webmcp.ErrorEndpointUnreachable:
		return "verify the configured browser endpoint is running and reachable"
	case webmcp.ErrorRemoteEndpointDenied:
		return "use a permitted loopback endpoint or explicitly allow the remote endpoint"
	case webmcp.ErrorUnsupportedWebMCP:
		return "use a browser adapter that provides the stateful WebMCP probe seam"
	case webmcp.ErrorBrowserDisconnected:
		return "reconnect the browser and rerun the probe"
	case webmcp.ErrorBrowserProtocol:
		return "check the browser protocol and probe adapter composition"
	case webmcp.ErrorInvocationCanceled:
		return "rerun the probe without cancellation"
	default:
		return "inspect the browser prerequisite and rerun the probe"
	}
}

func (c *ProbeRunCommand) probeScenarioV2BrowserExecutorOptions(cmd *cobra.Command) (ProbeScenarioV2BrowserExecutorOptions, error) {
	mode, err := ParseProbeScenarioV2BrowserExecutorMode(string(c.BrowserExecutorMode))
	if err != nil {
		return ProbeScenarioV2BrowserExecutorOptions{}, err
	}
	options := ProbeScenarioV2BrowserExecutorOptions{Mode: mode, Factory: c.browserFactory}
	if mode != ProbeScenarioV2BrowserExecutorReal {
		return options, nil
	}
	globalFlags := c.globalFlags
	if globalFlags == nil && c.ConfigDir != "" {
		globalFlags = &flags.GlobalFlags{ConfigDirPath: c.ConfigDir}
	}
	resolved, configErr := resolveSessionBrowserConfig(globalFlags, cmd, c.browserFlags)
	if configErr != nil {
		options.ConfigError = configErr
		return options, nil
	}
	options.Browser = resolved.Browser
	return options, nil
}
