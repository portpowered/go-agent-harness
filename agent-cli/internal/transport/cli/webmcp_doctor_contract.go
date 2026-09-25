package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/doctor"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// The doctor diagnosis lives in internal/webmcp/doctor and the direct-command
// runtime seams in internal/webmcp/direct. These aliases keep the CLI's
// public names for embedders and command tests.

// WebMCPDoctorVersionFunc supplies the browser version/protocol check.
type WebMCPDoctorVersionFunc = direct.VersionFunc

// WebMCPDiscoveryService is the discovery service consumed by the production
// composition. Keeping this interface at the CLI boundary lets command tests
// inject a discovery fake without importing a browser protocol package or
// depending on a concrete service implementation.
type WebMCPDiscoveryService = production.DiscoveryService

// WebMCPDoctorRuntime is the request-scoped set of seams used by doctor and
// the direct commands.
type WebMCPDoctorRuntime = direct.Runtime

// WebMCPDoctorFactory constructs one diagnostic runtime for a resolved
// browser configuration.
type WebMCPDoctorFactory = direct.Factory

// WebMCPRuntimeFactory and WebMCPDoctorRuntimeFactory are descriptive aliases
// for callers that name the injected seam after the runtime rather than the
// command.
type (
	WebMCPRuntimeFactory       = WebMCPDoctorFactory
	WebMCPDoctorRuntimeFactory = WebMCPDoctorFactory
)

// Doctor report shapes; see internal/webmcp/doctor.
type (
	WebMCPDoctorReport    = doctor.Report
	WebMCPDoctorEndpoint  = doctor.Endpoint
	WebMCPDoctorBrowser   = doctor.Browser
	WebMCPDoctorTarget    = doctor.Target
	WebMCPDoctorCatalog   = doctor.Catalog
	WebMCPDoctorCheck     = doctor.Check
	WebMCPDoctorErrorData = doctor.ErrorData
	WebMCPDoctorError     = doctor.Error
)

// The forwarders below keep the session and probe callers, which are outside
// this slice, on their existing names. Each delegates to the single
// implementation in the feature package.

func closeWebMCPDoctorRuntime(runtime WebMCPDoctorRuntime) error {
	return direct.CloseRuntime(runtime)
}

func webmcpRuntimeUnavailableError(phase string) error {
	return direct.RuntimeUnavailableError(phase)
}

func splitCompositeTargetRef(value string) (string, string, bool) {
	return normalize.SplitCompositeTargetRef(value)
}

func normalizeDirectOpaqueID(value string) string {
	return direct.NormalizeOpaqueID(value)
}
