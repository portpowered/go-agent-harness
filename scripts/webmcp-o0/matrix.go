package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"
)

const (
	nativeProbeToolName  = "webmcp_o0_probe_tool"
	missingProbeToolName = "webmcp_o0_missing_tool"
)

//go:embed fixture.html
var nativeFixtureHTML []byte

type pageMethodReport struct {
	Present bool   `json:"present"`
	Type    string `json:"type"`
	Length  *int   `json:"length,omitempty"`
}

type pageObjectReport struct {
	Present     bool                        `json:"present"`
	Type        string                      `json:"type"`
	AccessError string                      `json:"accessError,omitempty"`
	Methods     map[string]pageMethodReport `json:"methods"`
}

type pageDescriptorReport struct {
	Present      bool `json:"present"`
	HasGetter    bool `json:"hasGetter"`
	Enumerable   bool `json:"enumerable"`
	Configurable bool `json:"configurable"`
}

type pageToolSummary struct {
	Name          string `json:"name"`
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	InputSchema   any    `json:"inputSchema,omitempty"`
	Origin        string `json:"origin,omitempty"`
	WindowPresent bool   `json:"windowPresent,omitempty"`
}

type pageOperationReport struct {
	Attempted bool              `json:"attempted"`
	Outcome   string            `json:"outcome"`
	Error     string            `json:"error,omitempty"`
	Tools     []pageToolSummary `json:"tools,omitempty"`
	Requested string            `json:"requested,omitempty"`
	Returned  any               `json:"returned,omitempty"`
}

type fixtureRegistrationReport struct {
	Attempted bool   `json:"attempted"`
	Outcome   string `json:"outcome"`
	Error     string `json:"error,omitempty"`
}

type fixtureInvocationReport struct {
	Value string `json:"value"`
}

type fixtureStateReport struct {
	Ready        bool                      `json:"ready"`
	ToolName     string                    `json:"toolName"`
	ContextKind  string                    `json:"contextKind"`
	Registration fixtureRegistrationReport `json:"registration"`
	Invocations  []fixtureInvocationReport `json:"invocations,omitempty"`
}

type pageProbeReport struct {
	URL                       string                          `json:"url"`
	Origin                    string                          `json:"origin"`
	IsSecureContext           bool                            `json:"isSecureContext"`
	OriginAgentCluster        *bool                           `json:"originAgentCluster,omitempty"`
	PermissionsPolicyTools    *bool                           `json:"permissionsPolicyTools,omitempty"`
	DocumentModelContext      pageObjectReport                `json:"documentModelContext"`
	NavigatorModelContext     pageObjectReport                `json:"navigatorModelContext"`
	NavigatorModelContextTest pageObjectReport                `json:"navigatorModelContextTesting"`
	Descriptors               map[string]pageDescriptorReport `json:"descriptors"`
	Fixture                   fixtureStateReport              `json:"fixture"`
	ProducerDiscovery         pageOperationReport             `json:"producerDiscovery"`
	ProducerInvocation        pageOperationReport             `json:"producerInvocation"`
	TestingDiscovery          pageOperationReport             `json:"testingDiscovery"`
	TestingInvocation         pageOperationReport             `json:"testingInvocation"`
}

type advertisedProtocolReport struct {
	Available bool     `json:"available"`
	Domain    string   `json:"domain,omitempty"`
	Methods   []string `json:"methods,omitempty"`
	Events    []string `json:"events,omitempty"`
}

type typedCoverageReport struct {
	Commands        []string `json:"typedCommands"`
	Events          []string `json:"typedEvents"`
	MissingCommands []string `json:"missingCommands,omitempty"`
	MissingEvents   []string `json:"missingEvents,omitempty"`
	Verdict         string   `json:"verdict"`
}

type cdpAttemptReport struct {
	Attempted bool   `json:"attempted"`
	Outcome   string `json:"outcome"`
	Error     string `json:"error,omitempty"`
}

type cdpInvocationReport struct {
	Attempted    bool   `json:"attempted"`
	Outcome      string `json:"outcome"`
	ToolName     string `json:"toolName,omitempty"`
	InvocationID string `json:"invocationId,omitempty"`
	Status       string `json:"status,omitempty"`
	ErrorText    string `json:"errorText,omitempty"`
	Output       string `json:"output,omitempty"`
	Error        string `json:"error,omitempty"`
}

type cdpEventReport struct {
	ToolsAdded    []string `json:"toolsAdded,omitempty"`
	ToolsRemoved  []string `json:"toolsRemoved,omitempty"`
	ToolInvoked   []string `json:"toolInvoked,omitempty"`
	ToolResponded []string `json:"toolResponded,omitempty"`
}

type cdpProbeReport struct {
	Advertised advertisedProtocolReport `json:"advertised"`
	Typed      typedCoverageReport      `json:"typedCoverage"`
	Enable     cdpAttemptReport         `json:"enable"`
	Invocation cdpInvocationReport      `json:"invocation"`
	Events     cdpEventReport           `json:"events"`
}

type webmcpMatrixReport struct {
	ObservedAt    string          `json:"observedAt"`
	FixtureOrigin string          `json:"fixtureOrigin"`
	FixtureURL    string          `json:"fixtureURL"`
	Page          pageProbeReport `json:"page"`
	CDP           cdpProbeReport  `json:"cdp"`
	Verdict       string          `json:"verdict"`
}

type protocolDomain struct {
	Domain   string           `json:"domain"`
	Commands []protocolMethod `json:"commands"`
	Events   []protocolMethod `json:"events"`
}

type protocolMethod struct {
	Name string `json:"name"`
}

type protocolDocument struct {
	Domains []protocolDomain `json:"domains"`
}

type webmcpEventLog struct {
	mu        sync.Mutex
	added     []string
	removed   []string
	invoked   []string
	responded []string
	responses chan webmcpResponse
}

type webmcpResponse struct {
	InvocationID string
	Status       string
	ErrorText    string
	Output       string
}

func (l *webmcpEventLog) observe(event any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	switch event := event.(type) {
	case *webmcp.EventToolsAdded:
		for _, tool := range event.Tools {
			if tool != nil {
				l.added = append(l.added, tool.Name)
			}
		}
	case *webmcp.EventToolsRemoved:
		for _, tool := range event.Tools {
			if tool != nil {
				l.removed = append(l.removed, tool.Name)
			}
		}
	case *webmcp.EventToolInvoked:
		l.invoked = append(l.invoked, event.ToolName)
	case *webmcp.EventToolResponded:
		response := webmcpResponse{
			InvocationID: event.InvocationID,
			Status:       event.Status.String(),
			ErrorText:    event.ErrorText,
			Output:       string(event.Output),
		}
		l.responded = append(l.responded, event.InvocationID)
		select {
		case l.responses <- response:
		default:
		}
	}
}

func (l *webmcpEventLog) snapshot() cdpEventReport {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cdpEventReport{
		ToolsAdded:    append([]string(nil), l.added...),
		ToolsRemoved:  append([]string(nil), l.removed...),
		ToolInvoked:   append([]string(nil), l.invoked...),
		ToolResponded: append([]string(nil), l.responded...),
	}
}

func (l *webmcpEventLog) waitForResponse(ctx context.Context, invocationID string) (webmcpResponse, error) {
	for {
		select {
		case response := <-l.responses:
			if response.InvocationID == invocationID {
				return response, nil
			}
		case <-ctx.Done():
			return webmcpResponse{}, ctx.Err()
		}
	}
}

func startNativeFixture() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Origin-Agent-Cluster", "?1")
		writer.Header().Set("Permissions-Policy", "tools=(self)")
		writeFixtureBody(writer, nativeFixtureHTML)
	}))
}

func awaitPromise(parameter *runtime.EvaluateParams) *runtime.EvaluateParams {
	return parameter.WithAwaitPromise(true)
}

// pageProbeScript is evaluated in the fixture page to report WebMCP API
// shape, registration, discovery, and invocation results.
//
//go:embed page_probe.js
var pageProbeScript string

func pageProbeExpression() string {
	return pageProbeScript
}

func fetchProtocol(endpoint string) (report advertisedProtocolReport, err error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return advertisedProtocolReport{}, fmt.Errorf("parse browser endpoint: %w", err)
	}
	switch parsed.Scheme {
	case "ws":
		parsed.Scheme = schemeHTTP
	case "wss":
		parsed.Scheme = "https"
	case schemeHTTP, "https":
	default:
		return advertisedProtocolReport{}, fmt.Errorf("unsupported browser endpoint scheme %q", parsed.Scheme)
	}
	parsed.Path = "/json/protocol"
	parsed.RawQuery = ""
	parsed.Fragment = ""

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(parsed.String())
	if err != nil {
		return advertisedProtocolReport{}, fmt.Errorf("fetch %s: %w", parsed, err)
	}
	defer closeInto(&err, response.Body, "protocol response body")
	if response.StatusCode != http.StatusOK {
		return advertisedProtocolReport{}, fmt.Errorf("fetch %s: HTTP %s", parsed, response.Status)
	}
	var document protocolDocument
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&document); err != nil {
		return advertisedProtocolReport{}, fmt.Errorf("decode %s: %w", parsed, err)
	}
	for _, domain := range document.Domains {
		if domain.Domain != "WebMCP" {
			continue
		}
		report := advertisedProtocolReport{Available: true, Domain: domain.Domain}
		for _, method := range domain.Commands {
			report.Methods = append(report.Methods, method.Name)
		}
		for _, event := range domain.Events {
			report.Events = append(report.Events, event.Name)
		}
		sort.Strings(report.Methods)
		sort.Strings(report.Events)
		return report, nil
	}
	return advertisedProtocolReport{}, nil
}

func typedCoverage(advertised advertisedProtocolReport) typedCoverageReport {
	typedCommands := []string{
		webmcp.CommandEnable,
		webmcp.CommandDisable,
		webmcp.CommandInvokeTool,
		webmcp.CommandCancelInvocation,
	}
	typedEvents := []string{
		"toolsAdded",
		"toolsRemoved",
		"toolInvoked",
		"toolResponded",
	}
	commandSet := make(map[string]bool, len(advertised.Methods))
	for _, name := range advertised.Methods {
		commandSet[name] = true
	}
	eventSet := make(map[string]bool, len(advertised.Events))
	for _, name := range advertised.Events {
		eventSet[name] = true
	}
	coverage := typedCoverageReport{
		Commands: append([]string(nil), typedCommands...),
		Events:   append([]string(nil), typedEvents...),
	}
	for _, command := range typedCommands {
		shortName := strings.TrimPrefix(command, "WebMCP.")
		if advertised.Available && !commandSet[shortName] {
			coverage.MissingCommands = append(coverage.MissingCommands, command)
		}
	}
	for _, event := range typedEvents {
		if advertised.Available && !eventSet[event] {
			coverage.MissingEvents = append(coverage.MissingEvents, event)
		}
	}
	switch {
	case !advertised.Available:
		coverage.Verdict = "generated bindings present; WebMCP domain not advertised"
	case len(coverage.MissingCommands) > 0 || len(coverage.MissingEvents) > 0:
		coverage.Verdict = "partial typed coverage"
	default:
		coverage.Verdict = verdictCompleteTypedCoverage
	}
	return coverage
}

func waitForToolEvent(log *webmcpEventLog, name string, timeout time.Duration) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		events := log.snapshot()
		for _, added := range events.ToolsAdded {
			if added == name {
				return
			}
		}
		select {
		case <-deadline.C:
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func runCDPInvocation(ctx context.Context, targetContext context.Context, eventLog *webmcpEventLog, pageReport pageProbeReport, report *cdpProbeReport) {
	report.Invocation = cdpInvocationReport{Attempted: true, ToolName: missingProbeToolName}
	client := chromedp.FromContext(targetContext)
	if client == nil || client.Target == nil {
		report.Invocation.Outcome = outcomeError
		report.Invocation.Error = "target context has no attached target"
		return
	}
	frameTree, err := page.GetFrameTree().Do(cdp.WithExecutor(targetContext, client.Target))
	if err != nil || frameTree == nil || frameTree.Frame == nil {
		report.Invocation.Outcome = outcomeError
		report.Invocation.Error = fmt.Sprintf("get main frame: %v", err)
		return
	}
	if pageReport.Fixture.Registration.Outcome == "registered" {
		report.Invocation.ToolName = nativeProbeToolName
	}
	input := jsontext.Value([]byte(`{"value":"cdp"}`))
	invocationID, err := webmcp.InvokeTool(frameTree.Frame.ID, report.Invocation.ToolName, input).Do(cdp.WithExecutor(targetContext, client.Target))
	if err != nil {
		report.Invocation.Outcome = outcomeError
		report.Invocation.Error = err.Error()
		return
	}
	report.Invocation.InvocationID = invocationID
	responseContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := eventLog.waitForResponse(responseContext, invocationID)
	if err != nil {
		report.Invocation.Outcome = outcomeError
		report.Invocation.Error = err.Error()
		return
	}
	report.Invocation.Outcome = "response"
	report.Invocation.Status = response.Status
	report.Invocation.ErrorText = response.ErrorText
	report.Invocation.Output = response.Output
}

func nativeVerdict(pageReport pageProbeReport, cdpReport cdpProbeReport) string {
	nativeProducer := (pageReport.DocumentModelContext.Present || pageReport.NavigatorModelContext.Present) &&
		pageReport.Fixture.Registration.Outcome == "registered" &&
		pageReport.ProducerDiscovery.Outcome == outcomeSuccess &&
		pageReport.ProducerInvocation.Outcome == outcomeSuccess
	cdpUsable := cdpReport.Advertised.Available &&
		cdpReport.Typed.Verdict == verdictCompleteTypedCoverage &&
		cdpReport.Enable.Outcome == outcomeSuccess &&
		cdpReport.Invocation.Outcome == "response" &&
		cdpReport.Invocation.Status == webmcp.InvocationStatusCompleted.String()
	testingSurface := pageReport.NavigatorModelContextTest.Present &&
		pageReport.TestingDiscovery.Attempted
	switch {
	case nativeProducer && cdpUsable:
		return verdictPass
	case nativeProducer || cdpUsable || testingSurface:
		return "PARTIAL"
	default:
		return "FAIL"
	}
}

func runWebMCPMatrix(endpoint string) (webmcpMatrixReport, error) {
	if endpoint == "" {
		return webmcpMatrixReport{}, fmt.Errorf("browser websocket endpoint is empty")
	}
	fixture := startNativeFixture()
	defer fixture.Close()

	rootContext, cancelRoot := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext)
	defer func() {
		// The remote allocator never owns Chrome. Cancel only the temporary
		// client target; the launcher owns the browser process.
		cancelProbeTarget(targetContext)
		cancelTarget()
	}()

	eventLog := &webmcpEventLog{responses: make(chan webmcpResponse, 16)}
	chromedp.ListenTarget(targetContext, eventLog.observe)
	if err := chromedp.Run(targetContext, chromedp.Navigate(fixture.URL), chromedp.WaitReady("body")); err != nil {
		return webmcpMatrixReport{}, fmt.Errorf("navigate to loopback fixture %s: %w", fixture.URL, err)
	}
	var pageReport pageProbeReport
	if err := chromedp.Run(targetContext, chromedp.Evaluate(pageProbeExpression(), &pageReport, awaitPromise)); err != nil {
		return webmcpMatrixReport{}, fmt.Errorf("evaluate WebMCP page probe: %w", err)
	}
	advertised, err := fetchProtocol(endpoint)
	if err != nil {
		return webmcpMatrixReport{}, err
	}
	cdpReport := cdpProbeReport{
		Advertised: advertised,
		Typed:      typedCoverage(advertised),
		Enable:     cdpAttemptReport{Attempted: true},
	}
	client := chromedp.FromContext(targetContext)
	if client == nil || client.Target == nil {
		cdpReport.Enable.Outcome = outcomeError
		cdpReport.Enable.Error = "target context has no attached target"
	} else if err := webmcp.Enable().Do(cdp.WithExecutor(targetContext, client.Target)); err != nil {
		cdpReport.Enable.Outcome = outcomeError
		cdpReport.Enable.Error = err.Error()
	} else {
		cdpReport.Enable.Outcome = outcomeSuccess
		waitForToolEvent(eventLog, pageReport.Fixture.ToolName, 750*time.Millisecond)
		runCDPInvocation(rootContext, targetContext, eventLog, pageReport, &cdpReport)
	}
	cdpReport.Events = eventLog.snapshot()

	return webmcpMatrixReport{
		ObservedAt:    time.Now().UTC().Format(time.RFC3339),
		FixtureOrigin: fixture.URL,
		FixtureURL:    fixture.URL,
		Page:          pageReport,
		CDP:           cdpReport,
		Verdict:       nativeVerdict(pageReport, cdpReport),
	}, nil
}
