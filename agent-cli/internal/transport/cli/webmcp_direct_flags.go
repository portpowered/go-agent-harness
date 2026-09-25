package cli

import (
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/spf13/cobra"
)

// DefaultWebMCPDirectCommandTimeout is the end-to-end safety deadline for one
// direct WebMCP command; it is direct.DefaultCommandTimeout. A caller may
// choose a shorter value with --command-timeout; zero uses this safe default.
const DefaultWebMCPDirectCommandTimeout = direct.DefaultCommandTimeout

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

func directCommandTimeout(values *webmcpDirectFlags) time.Duration {
	if values == nil || values.commandTimeout == 0 {
		return DefaultWebMCPDirectCommandTimeout
	}
	return values.commandTimeout
}

func directBrowserFlagChanged(cmd *cobra.Command) bool {
	return directFlagChanged(cmd, "browser", "browser-browser")
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
