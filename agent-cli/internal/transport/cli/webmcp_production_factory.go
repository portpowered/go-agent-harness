package cli

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/doctor"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production"
)

// The production WebMCP composition lives in internal/webmcp/production.
// This file only adapts it to the CLI's doctor/direct-command seams.

// WebMCPProductionOptions holds the injectable production dependencies.
type WebMCPProductionOptions = production.Options

// WithWebMCPProductionConfigDir keeps managed-browser state and selection
// persistence on the same resolved config directory.
func WithWebMCPProductionConfigDir(configDir string) production.Option {
	return production.WithConfigDir(configDir)
}

// WithWebMCPProductionWorkingDir injects the host working directory for
// managed Chrome for Testing lock discovery.
func WithWebMCPProductionWorkingDir(workingDir string) production.Option {
	return production.WithWorkingDir(workingDir)
}

// WithWebMCPProductionSelectionStore injects the selection store.
func WithWebMCPProductionSelectionStore(store any) production.Option {
	return production.WithSelectionStore(store)
}

// WithWebMCPProductionSelectionStoreFactory defers selection-store creation
// until command execution. This keeps a parsed --config-dir override aligned
// with the store used by the direct command.
func WithWebMCPProductionSelectionStoreFactory(factory func() any) production.Option {
	return production.WithSelectionStoreFactory(factory)
}

// NewProductionWebMCPDoctorFactory validates the resolved browser
// configuration at the command boundary and then delegates composition to
// the production package. Construction remains lazy: no browser endpoint is
// opened until a command invokes the returned factory runtime.
func NewProductionWebMCPDoctorFactory(options ...production.Option) WebMCPDoctorFactory {
	build := production.NewFactory(options...)
	return func(browser config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		if err := browser.Validate(); err != nil {
			return WebMCPDoctorRuntime{}, fmt.Errorf("resolve browser config: %w", err)
		}
		if err := doctor.ValidateEndpoints(browser); err != nil {
			return WebMCPDoctorRuntime{}, err
		}
		runtime, err := build(browser)
		if err != nil {
			return WebMCPDoctorRuntime{}, err
		}
		return WebMCPDoctorRuntime{
			Broker:    runtime.Broker,
			Discovery: runtime.Discovery,
			Catalog:   runtime.Catalog,
			Close:     runtime.Close,
		}, nil
	}
}

// configDirForGlobalFlags is kept at the composition boundary so the route
// can give the production factory the same selection path as direct commands.
func configDirForGlobalFlags(globalFlags *flags.GlobalFlags) string {
	if globalFlags == nil {
		return ""
	}
	return globalFlags.ConfigDir()
}

// defaultWebMCPDoctorFactory is the single default composition used by
// direct commands and router fallbacks. The selection store is created only
// after flags have been parsed, so --config-dir applies consistently without
// making command construction touch the filesystem.
func defaultWebMCPDoctorFactory(globalFlags *flags.GlobalFlags) WebMCPDoctorFactory {
	// Without a host working directory the lock search starts from the
	// executable and source directories only.
	workingDir := globalFlags.HostWorkDirOrEmpty()
	return NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionConfigDir(configDirForGlobalFlags(globalFlags)),
		WithWebMCPProductionWorkingDir(workingDir),
		WithWebMCPProductionSelectionStoreFactory(func() any {
			return NewFileWebMCPSelectionStore(configDirForGlobalFlags(globalFlags))
		}),
	)
}
