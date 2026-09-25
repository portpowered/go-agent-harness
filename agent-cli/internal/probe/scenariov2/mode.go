// Package scenariov2 executes browser-aware probe.scenario.v2 documents and
// finalizes their verified evidence bundles. It owns the step grammar, the
// hermetic and real browser compositions, expectation evaluation, browser
// evidence recording, and objective verification. Hosts (the CLI) supply
// only configuration, the real browser factory, and output writers.
package scenariov2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// BrowserExecutorMode selects the browser composition used by a
// probe.scenario.v2 run. Hermetic is deliberately the default so an existing
// probe invocation cannot acquire a browser connection implicitly.
type BrowserExecutorMode string

const (
	BrowserExecutorHermetic BrowserExecutorMode = "hermetic"
	BrowserExecutorReal     BrowserExecutorMode = "real"
)

// ErrInvalidBrowserExecutorMode identifies an unsupported browser executor
// selection before any scenario or browser work begins.
var ErrInvalidBrowserExecutorMode = errors.New("invalid probe.scenario.v2 browser executor mode")

// ErrRealAdapterUnavailable identifies a real-mode prerequisite failure. It
// is intentionally separate from the transport-specific cause so callers can
// classify the failure without depending on Chrome or CDP.
var ErrRealAdapterUnavailable = errors.New("probe.scenario.v2 real browser adapter unavailable")

// ParseBrowserExecutorMode validates the explicit probe mode.
func ParseBrowserExecutorMode(raw string) (BrowserExecutorMode, error) {
	mode := BrowserExecutorMode(strings.ToLower(strings.TrimSpace(raw)))
	if mode == BrowserExecutorHermetic || mode == BrowserExecutorReal {
		return mode, nil
	}
	return "", fmt.Errorf("%w: got %q; want %q or %q", ErrInvalidBrowserExecutorMode, raw, BrowserExecutorHermetic, BrowserExecutorReal)
}

// RealRuntime is the run-scoped real browser composition. Broker must also
// implement the stateful v2 seam. Close owns every runtime resource,
// including the broker. Navigate and PageState are optional observations.
type RealRuntime struct {
	Broker    webmcp.Broker
	Close     func() error
	Navigate  func(context.Context, string) error
	PageState func(context.Context) (json.RawMessage, error)
}

// RealRuntimeFactory constructs one real browser runtime for a resolved
// browser configuration. It is consulted only in real mode. On error the
// returned runtime is still closed so partial construction is released.
type RealRuntimeFactory func(config.BrowserConfig) (RealRuntime, error)

// BrowserExecutorOptions is the typed composition boundary for the browser
// executor. Factory is used only for real mode; hermetic mode always
// constructs the transport-free testkit runtime in this package.
type BrowserExecutorOptions struct {
	Mode        BrowserExecutorMode
	Factory     RealRuntimeFactory
	Browser     config.BrowserConfig
	ConfigError error
}

// BrowserExecutorOption customizes one v2 browser executor.
type BrowserExecutorOption func(*BrowserExecutorOptions)

// WithBrowserExecutorMode selects hermetic or real execution.
func WithBrowserExecutorMode(mode BrowserExecutorMode) BrowserExecutorOption {
	return func(options *BrowserExecutorOptions) {
		options.Mode = mode
	}
}

// WithBrowserExecutorFactory supplies the real browser composition. The
// factory is never consulted by hermetic execution.
func WithBrowserExecutorFactory(factory RealRuntimeFactory) BrowserExecutorOption {
	return func(options *BrowserExecutorOptions) {
		options.Factory = factory
	}
}

// WithBrowserExecutorConfig supplies the resolved browser configuration for
// a real run.
func WithBrowserExecutorConfig(browser config.BrowserConfig) BrowserExecutorOption {
	return func(options *BrowserExecutorOptions) {
		options.Browser = browser
	}
}

// WithBrowserExecutorConfigError preserves configuration load failures for
// the per-scenario result without attempting a fallback runtime.
func WithBrowserExecutorConfigError(err error) BrowserExecutorOption {
	return func(options *BrowserExecutorOptions) {
		options.ConfigError = err
	}
}

// Options returns options that reproduce o exactly.
func (o BrowserExecutorOptions) Options() []BrowserExecutorOption {
	return []BrowserExecutorOption{
		WithBrowserExecutorMode(o.Mode),
		WithBrowserExecutorFactory(o.Factory),
		WithBrowserExecutorConfig(o.Browser),
		WithBrowserExecutorConfigError(o.ConfigError),
	}
}

func resolveBrowserExecutorOptions(options ...BrowserExecutorOption) (BrowserExecutorOptions, error) {
	resolved := BrowserExecutorOptions{Mode: BrowserExecutorHermetic}
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}
	if resolved.Mode == "" {
		resolved.Mode = BrowserExecutorHermetic
	}
	if _, err := ParseBrowserExecutorMode(string(resolved.Mode)); err != nil {
		return BrowserExecutorOptions{}, err
	}
	return resolved, nil
}
