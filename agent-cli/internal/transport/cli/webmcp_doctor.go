package cli

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/doctor"
	"github.com/spf13/cobra"
)

// WebMCPDoctorCommand implements `yui webmcp doctor`.
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

// diagnose resolves the browser configuration from this command's flags and
// runs the doctor with the command's runtime factory.
func (c *WebMCPDoctorCommand) diagnose(ctx context.Context, cmd *cobra.Command) (WebMCPDoctorReport, error) {
	factory := c.factory
	if factory == nil {
		factory = defaultWebMCPDoctorFactory(c.globalFlags)
	}
	return doctor.Diagnose(ctx, doctor.Request{
		CommandTimeout: c.commandTimeout,
		LoadConfig: func() (*config.Config, error) {
			return resolveSessionBrowserConfig(c.globalFlags, cmd, c.browserFlags)
		},
		Factory: factory,
	})
}
