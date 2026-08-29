package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

type WebMCPDoctorVersionFunc func(context.Context, webmcp.BrowserCandidate) (webmcp.BrowserVersion, error)

// WebMCPDiscoveryService is the discovery service consumed by the production
// composition. Keeping this interface at the CLI boundary lets
// command tests inject a discovery fake without importing a browser protocol
// package or depending on a concrete service implementation.
type WebMCPDiscoveryService interface {
	DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error)
	ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error)
	Select(context.Context, discovery.TargetSelectionRequest) (discovery.Selection, error)
	Selected() (discovery.Selection, bool)
	RefreshSelection(context.Context) (discovery.Selection, error)
}

// WebMCPDoctorRuntime is the request-scoped set of seams used by doctor.
// Broker is the real stateful broker in production-capable compositions;
// VersionFunc or Catalog supplies the endpoint/version check. Close is
// optional and owns any runtime resources not owned by Broker.
type WebMCPDoctorRuntime struct {
	Broker      webmcp.Broker
	Discovery   WebMCPDiscoveryService
	VersionFunc WebMCPDoctorVersionFunc
	Catalog     webmcp.DevToolsCatalog
	Close       func() error
	// Navigate and PageState are optional run-scoped browser observations used
	// by probe.scenario.v2. Keeping them outside BrowserRuntime preserves the
	// neutral broker contract for callers that do not need probe evidence.
	Navigate  func(context.Context, string) error
	PageState func(context.Context) (json.RawMessage, error)
}

// WebMCPDoctorFactory constructs one diagnostic runtime for a resolved
// browser configuration. Construction is lazy and is never called when
// configuration or endpoint policy validation has already failed.
type WebMCPDoctorFactory func(config.BrowserConfig) (WebMCPDoctorRuntime, error)

// WebMCPRuntimeFactory and WebMCPDoctorRuntimeFactory are descriptive aliases
// for callers that name the injected seam after the runtime rather than the
// command.
type WebMCPRuntimeFactory = WebMCPDoctorFactory
type WebMCPDoctorRuntimeFactory = WebMCPDoctorFactory

// WebMCPDoctorReport is the stable, machine-readable diagnostic result. All
// URL-bearing fields are reduced to a redacted endpoint or origin-only value;
// websocket paths, credentials, query strings, fragments, and page URLs are
// never included.
type WebMCPDoctorReport struct {
	Version       string                 `json:"version"`
	Status        string                 `json:"status"`
	Endpoint      WebMCPDoctorEndpoint   `json:"endpoint"`
	Browsers      []WebMCPDoctorBrowser  `json:"browsers"`
	Targets       []WebMCPDoctorTarget   `json:"targets"`
	PageTargets   int                    `json:"page_targets"`
	EligiblePages int                    `json:"eligible_pages"`
	SelectedPage  *WebMCPDoctorTarget    `json:"selected_page"`
	WebMCP        string                 `json:"webmcp"`
	WebMCPDomain  string                 `json:"webmcp_domain"`
	PageTools     string                 `json:"page_tools"`
	Catalog       WebMCPDoctorCatalog    `json:"catalog"`
	Checks        []WebMCPDoctorCheck    `json:"checks"`
	Warnings      []string               `json:"warnings"`
	Error         *WebMCPDoctorErrorData `json:"error"`
}

// WebMCPDoctorEndpoint describes the configured or discovered endpoint
// without retaining any credential-bearing or websocket-secret material.
type WebMCPDoctorEndpoint struct {
	Source  string `json:"source"`
	Address string `json:"address"`
	Scope   string `json:"scope"`
}

type WebMCPDoctorBrowser struct {
	ID       string `json:"id"`
	Product  string `json:"product"`
	Protocol string `json:"protocol"`
	Scope    string `json:"scope"`
}

type WebMCPDoctorTarget struct {
	BrowserID             string `json:"browser_id"`
	TargetID              string `json:"target_id"`
	Type                  string `json:"type"`
	Title                 string `json:"title"`
	Origin                string `json:"origin"`
	Eligible              bool   `json:"eligible"`
	EligibilityReason     string `json:"eligibility_reason,omitempty"`
	Attached              bool   `json:"attached"`
	Selected              bool   `json:"selected"`
	WebMCPDomainSupported bool   `json:"webmcp_domain_supported"`
	PageToolsReady        bool   `json:"page_tools_ready"`
	PageToolsKnown        bool   `json:"page_tools_known"`
	PageToolsEvidence     string `json:"page_tools_evidence,omitempty"`
}

type WebMCPDoctorCatalog struct {
	Ready          bool   `json:"ready"`
	Generation     uint64 `json:"generation"`
	ToolCount      int    `json:"tool_count"`
	ToolCountKnown bool   `json:"tool_count_known"`
	Evidence       string `json:"evidence,omitempty"`
}

type WebMCPDoctorCheck struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// WebMCPDoctorErrorData follows the classified error shape used by the
// broker while allowing doctor-only availability and cleanup classifications.
type WebMCPDoctorErrorData struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

// WebMCPDoctorError is returned after a report has been rendered. Its report
// is also available to programmatic callers, while Unwrap preserves the
// original classified cause for errors.Is/errors.As checks.
type WebMCPDoctorError struct {
	Report WebMCPDoctorReport
	Cause  error
}

func (e *WebMCPDoctorError) Error() string {
	if e == nil {
		return "webmcp doctor failed"
	}
	if e.Report.Error != nil {
		return fmt.Sprintf("webmcp doctor: %s: %s", e.Report.Error.Code, e.Report.Error.Message)
	}
	return "webmcp doctor failed"
}

func (e *WebMCPDoctorError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// WebMCPDoctorCommand implements `agent webmcp doctor`.
