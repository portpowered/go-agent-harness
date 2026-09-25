package cli

import (
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
	"github.com/spf13/cobra"
)

// ProbeScenarioV2BrowserExecutorMode selects the browser composition used by
// a probe.scenario.v2 run; see scenariov2.BrowserExecutorMode.
type ProbeScenarioV2BrowserExecutorMode = scenariov2.BrowserExecutorMode

const (
	ProbeScenarioV2BrowserExecutorHermetic = scenariov2.BrowserExecutorHermetic
	ProbeScenarioV2BrowserExecutorReal     = scenariov2.BrowserExecutorReal
)

// probeScenarioV2BrowserExecutorModeValue is the typed --browser-executor flag.
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
	mode, err := scenariov2.ParseBrowserExecutorMode(raw)
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

// probeScenarioV2BrowserExecutorOptions resolves the executor composition
// from the command's flags. Real mode resolves the browser configuration; a
// configuration failure is carried into each scenario result.
func (c *ProbeRunCommand) probeScenarioV2BrowserExecutorOptions(cmd *cobra.Command) (scenariov2.BrowserExecutorOptions, error) {
	mode, err := scenariov2.ParseBrowserExecutorMode(string(c.BrowserExecutorMode))
	if err != nil {
		return scenariov2.BrowserExecutorOptions{}, err
	}
	options := scenariov2.BrowserExecutorOptions{Mode: mode, Factory: probeScenarioV2RealRuntimeFactory(c.browserFactory)}
	if mode != ProbeScenarioV2BrowserExecutorReal {
		return options, nil
	}
	globalFlags := c.globalFlags
	if globalFlags == nil && c.ConfigDir != "" {
		globalFlags = &flags.GlobalFlags{ConfigDirPath: c.ConfigDir}
	}
	resolved, configErr := resolveSessionBrowserConfig(globalFlags, cmd, c.browserFlags)
	options.ConfigError = configErr
	if configErr == nil {
		options.Browser = resolved.Browser
	}
	return options, nil
}

// probeScenarioV2RealRuntimeFactory adapts the WebMCP doctor composition to
// the v2 executor's real runtime seam. The adapted Close owns both the
// broker and the runtime's own resources.
func probeScenarioV2RealRuntimeFactory(factory WebMCPDoctorFactory) scenariov2.RealRuntimeFactory {
	if factory == nil {
		return nil
	}
	return func(browser config.BrowserConfig) (scenariov2.RealRuntime, error) {
		runtime, err := factory(browser)
		return scenariov2.RealRuntime{
			Broker:    runtime.Broker,
			Close:     func() error { return closeWebMCPDoctorRuntime(runtime) },
			Navigate:  runtime.Navigate,
			PageState: runtime.PageState,
		}, err
	}
}
