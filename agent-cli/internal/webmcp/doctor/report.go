// Package doctor diagnoses WebMCP browser readiness without starting a model
// session: configuration, endpoint policy, discovery, protocol version,
// targets, origin policy, exact target selection, WebMCP support, catalog
// readiness, and cleanup. It produces a redacted, machine-readable Report;
// rendering and command wiring stay in the CLI transport.
package doctor

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// Report, status, check, and error vocabulary.
const (
	ResultVersion = "webmcp.doctor.v1"

	StatusReady                = "ready"
	StatusNotReady             = "not_ready"
	StatusUnavailable          = "unavailable"
	StatusInvalidConfiguration = "invalid_configuration"
	StatusCleanupError         = "cleanup_error"

	CheckPass        = "pass"
	CheckWarn        = "warning"
	CheckFail        = "fail"
	CheckUnavailable = "unavailable"
	CheckSkipped     = "skipped"

	ErrorInvalidConfiguration = "invalid_configuration"
	ErrorCleanupFailed        = "cleanup_failed"

	messageDoctorFailed = "webmcp doctor failed"

	TestedChromeRow   = "Stable Chrome for Testing 152.0.7977.64 (mac-arm64, revision 1669021)"
	TestedChromeFlags = "--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport"
)

// Readiness values reported for the WebMCP domain, page tools, and catalog.
const (
	ValueNotChecked  = "not_checked"
	ValueUnverified  = "unverified"
	ValueSupported   = "supported"
	ValueUnsupported = "unsupported"
)

// Check names, in report order.
const (
	checkConfiguration = "configuration"
	checkActivation    = "activation"
	checkEndpoint      = "endpoint"
	checkDiscovery     = "discovery"
	checkVersion       = "version"
	checkTargets       = "targets"
	checkSelection     = "selection"
	checkPolicy        = "policy"
	checkWebMCP        = "webmcp"
	checkCatalog       = "catalog"
	checkCleanup       = "cleanup"
)

// Detail keys and values shared across checks.
const (
	keyPhase          = "phase"
	keyBrowserID      = "browser_id"
	keyTargetID       = "target_id"
	keyPageTools      = "page_tools"
	keyCandidateCount = "candidate_count"
	keyReason         = "reason"
	keySelectedGen    = "selected_generation"

	maxWarnings       = 8
	maxWarningLength  = 240
	maxTitleLength    = 160
	maxProductLength  = 160
	maxProtocolLength = 80
	maxTypeLength     = 40
	maxReasonLength   = 160
)

// Report is the stable, machine-readable diagnostic result. All URL-bearing
// fields are reduced to a redacted endpoint or origin-only value; websocket
// paths, credentials, query strings, fragments, and page URLs are never
// included.
type Report struct {
	Version       string     `json:"version"`
	Status        string     `json:"status"`
	Endpoint      Endpoint   `json:"endpoint"`
	Browsers      []Browser  `json:"browsers"`
	Targets       []Target   `json:"targets"`
	PageTargets   int        `json:"page_targets"`
	EligiblePages int        `json:"eligible_pages"`
	SelectedPage  *Target    `json:"selected_page"`
	WebMCP        string     `json:"webmcp"`
	WebMCPDomain  string     `json:"webmcp_domain"`
	PageTools     string     `json:"page_tools"`
	Catalog       Catalog    `json:"catalog"`
	Checks        []Check    `json:"checks"`
	Warnings      []string   `json:"warnings"`
	Error         *ErrorData `json:"error"`
}

// Endpoint describes the configured or discovered endpoint without retaining
// any credential-bearing or websocket-secret material.
type Endpoint struct {
	Source  string `json:"source"`
	Address string `json:"address"`
	Scope   string `json:"scope"`
}

// Browser is one discovered browser.
type Browser struct {
	ID       string `json:"id"`
	Product  string `json:"product"`
	Protocol string `json:"protocol"`
	Scope    string `json:"scope"`
}

// Target is one redacted browser target.
type Target struct {
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

// Catalog is the selected page's WebMCP catalog readiness.
type Catalog struct {
	Ready          bool   `json:"ready"`
	Generation     uint64 `json:"generation"`
	ToolCount      int    `json:"tool_count"`
	ToolCountKnown bool   `json:"tool_count_known"`
	Evidence       string `json:"evidence,omitempty"`
}

// Check is one named diagnostic step.
type Check struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrorData follows the classified error shape used by the broker while
// allowing doctor-only availability and cleanup classifications.
type ErrorData struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

// Error is returned after a report has been produced. Its report is also
// available to programmatic callers, while Unwrap preserves the original
// classified cause for errors.Is/errors.As checks.
type Error struct {
	Report Report
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return messageDoctorFailed
	}
	if e.Report.Error != nil {
		return fmt.Sprintf("webmcp doctor: %s: %s", e.Report.Error.Code, e.Report.Error.Message)
	}
	return messageDoctorFailed
}

// Unwrap returns the classified cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewReport returns a not-ready report with every check skipped.
func NewReport() Report {
	checkNames := []string{checkConfiguration, checkActivation, checkEndpoint, checkDiscovery, checkVersion, checkTargets, checkSelection, checkPolicy, checkWebMCP, checkCatalog, checkCleanup}
	report := Report{
		Version:      ResultVersion,
		Status:       StatusNotReady,
		Warnings:     []string{},
		Checks:       make([]Check, 0, len(checkNames)),
		Browsers:     []Browser{},
		Targets:      []Target{},
		WebMCP:       ValueNotChecked,
		WebMCPDomain: ValueNotChecked,
		PageTools:    ValueNotChecked,
	}
	for _, name := range checkNames {
		report.Checks = append(report.Checks, Check{Name: name, Status: CheckSkipped})
	}
	return report
}

// ErrorDataFor converts err into report error data, preferring a nested
// browser_disconnected cause.
func ErrorDataFor(err error, fallback webmcp.ErrorCode, details map[string]any) *ErrorData {
	err = direct.PreferBrowserDisconnected(err)
	result := webmcp.ResultErrorFor(err, fallback, details)
	return &ErrorData{Code: result.Code, Message: result.Message, Retryable: result.Retryable, Details: result.Details}
}

func (r *Report) setCheck(name, status, message string, details map[string]any) {
	if r == nil {
		return
	}
	for index := range r.Checks {
		if r.Checks[index].Name != name {
			continue
		}
		r.Checks[index] = Check{Name: name, Status: status, Message: message, Details: details}
		return
	}
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Message: message, Details: details})
}

// fail marks the report with status and the classified error data for err.
func (r *Report) fail(status string, err error, fallback webmcp.ErrorCode, details map[string]any) {
	r.Status = status
	r.Error = ErrorDataFor(err, fallback, details)
}

// notReady is fail with the not_ready status used by every runtime stage.
func (r *Report) notReady(err error, fallback webmcp.ErrorCode, details map[string]any) {
	r.fail(StatusNotReady, err, fallback, details)
}

func (r *Report) addWarning(warning string) {
	if r == nil || warning == "" || len(r.Warnings) >= maxWarnings {
		return
	}
	for _, existing := range r.Warnings {
		if existing == warning {
			return
		}
	}
	r.Warnings = append(r.Warnings, normalize.BoundedText(warning, maxWarningLength))
}

func phaseDetails(phase string) map[string]any {
	return map[string]any{keyPhase: phase}
}
