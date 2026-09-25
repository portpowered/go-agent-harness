package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/spf13/cobra"
)

func TestWebMCPProductionCLIEnumeratesSupportedDiscoveryShapes(t *testing.T) {
	tests := []struct {
		name             string
		config           string
		args             []string
		wantID           string
		wantSource       webmcp.DiscoverySource
		wantProduct      string
		wantHTTPHost     string
		wantActiveCalls  int
		wantProcessCalls int
	}{
		{
			name: "explicit command flag overrides configured endpoint",
			config: `
browser:
  connection:
    cdp_url: http://127.0.0.1:9222
`,
			args:             []string{"browsers", "--cdp-url", "http://127.0.0.1:9223", "--json"},
			wantID:           "browser-flag",
			wantSource:       webmcp.DiscoverySourceExplicit,
			wantProduct:      "Chrome/Flag",
			wantHTTPHost:     "127.0.0.1:9223",
			wantProcessCalls: 0,
		},
		{
			name: "configured endpoint",
			config: `
browser:
  connection:
    cdp_url: http://127.0.0.1:9222
`,
			args:             []string{"browsers", "--json"},
			wantID:           "browser-configured",
			wantSource:       webmcp.DiscoverySourceExplicit,
			wantProduct:      "Chrome/Configured",
			wantHTTPHost:     "127.0.0.1:9222",
			wantProcessCalls: 0,
		},
		{
			name: "active port profile without explicit endpoint",
			config: `
browser:
  connection:
    user_data_dir: /hermetic/profile
`,
			args:             []string{"browsers", "--json"},
			wantID:           "browser-active",
			wantSource:       webmcp.DiscoverySourceActivePort,
			wantProduct:      "Chrome/Active",
			wantHTTPHost:     "127.0.0.1:9224",
			wantActiveCalls:  1,
			wantProcessCalls: 0,
		},
		{
			name: "allowed process discovery",
			config: `
browser:
  connection:
    allow_process_scan: true
`,
			args:             []string{"browsers", "--json"},
			wantID:           "browser-process",
			wantSource:       webmcp.DiscoverySourceProcess,
			wantProduct:      "Chrome/Process",
			wantHTTPHost:     "127.0.0.1:9225",
			wantProcessCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runDiscoveryShapeCase(t, tc.config, tc.args, discoveredBrowserWant{
				id: tc.wantID, source: tc.wantSource, product: tc.wantProduct, httpHost: tc.wantHTTPHost,
				activeCalls: tc.wantActiveCalls, processCalls: tc.wantProcessCalls,
			})
		})
	}
}

type discoveredBrowserWant struct {
	id                        string
	source                    webmcp.DiscoverySource
	product, httpHost         string
	activeCalls, processCalls int
}

// runDiscoveryShapeCase runs `webmcp browsers` through the shipped CLI over
// the hermetic discovery fixture and requires exactly the wanted browser.
func runDiscoveryShapeCase(t *testing.T, doctorConfig string, args []string, want discoveredBrowserWant) {
	t.Helper()
	fixture := newCLIProductionDiscoveryFixture()
	runtime := &productionFakeRuntime{}
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(fixture.http),
		WithWebMCPProductionActivePortReader(fixture.activePort),
		WithWebMCPProductionProcessEnumerator(fixture.process),
		WithWebMCPProductionIDMapper(cliProductionIDMapper{}),
		WithWebMCPProductionClock(cliProductionClock{now: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)}),
	)
	result := executeShippedWebMCPCommand(t, writeDoctorConfig(t, doctorConfig), factory, args...)
	if result.err != nil {
		t.Fatalf("CLI exit status = 1, want 0: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
	}
	if result.stderr != "" {
		t.Fatalf("CLI stderr = %q, want empty", result.stderr)
	}
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectBrowsersData
	decodeDirectData(t, envelope.Data, &data)
	if len(data.Browsers) != 1 {
		t.Fatalf("browser count = %d, want one: %+v", len(data.Browsers), data)
	}
	browser := data.Browsers[0]
	if browser.ID != want.id || browser.Source != string(want.source) || browser.Product != want.product {
		t.Fatalf("browser = %+v, want id=%q source=%q product=%q", browser, want.id, want.source, want.product)
	}
	if browser.Scope != "loopback" || browser.Endpoint != "http://127.0.0.1:"+strings.TrimPrefix(want.httpHost, "127.0.0.1:")+"/json/version" {
		t.Fatalf("browser endpoint/scope = %q/%q, want redacted loopback endpoint for %s", browser.Endpoint, browser.Scope, want.httpHost)
	}
	fixture.assertDiscoveryCalls(t, want.httpHost, want.activeCalls, want.processCalls)
	if got := runtime.count("open"); got != 0 {
		t.Fatalf("test assertion runtime unexpectedly opened %d handles", got)
	}
}

func TestWebMCPProductionCLIClassifiesDiscoveryFailures(t *testing.T) {
	tests := []struct {
		name          string
		config        string
		responses     map[string]cliHTTPResponse
		wantCode      webmcp.ErrorCode
		wantDetail    string
		forbiddenText []string
	}{
		{
			name: "endpoint not found",
			config: `
browser:
  connection:
    cdp_url: http://127.0.0.1:9230
`,
			responses: map[string]cliHTTPResponse{
				"127.0.0.1:9230": {status: http.StatusNotFound},
			},
			wantCode: webmcp.ErrorEndpointNotFound,
		},
		{
			name: "endpoint unreachable",
			config: `
browser:
  connection:
    cdp_url: http://127.0.0.1:9231
`,
			responses: map[string]cliHTTPResponse{
				"127.0.0.1:9231": {err: errors.New("dial failed for endpoint-secret")},
			},
			wantCode:      webmcp.ErrorEndpointUnreachable,
			wantDetail:    "version",
			forbiddenText: []string{"endpoint-secret", "127.0.0.1:9231"},
		},
		{
			name: "remote endpoint denied",
			config: `
browser:
  connection:
    cdp_url: http://192.0.2.1:9232/json/version?token=secret
`,
			wantCode:      webmcp.ErrorRemoteEndpointDenied,
			forbiddenText: []string{"192.0.2.1", "token=secret"},
		},
		{
			name: "invalid browser protocol",
			config: `
browser:
  connection:
    cdp_url: http://127.0.0.1:9233
`,
			responses: map[string]cliHTTPResponse{
				"127.0.0.1:9233": {body: `{"Browser":"Chrome/Invalid","Protocol-Version":"2.0","webSocketDebuggerUrl":"ws://127.0.0.1:9233/devtools/browser/invalid"}`},
			},
			wantCode:      webmcp.ErrorBrowserProtocol,
			wantDetail:    "version",
			forbiddenText: []string{"127.0.0.1:9233", "devtools/browser/invalid"},
		},
		{
			name: "no endpoint",
			config: `
browser:
  connection: {}
`,
			wantCode: webmcp.ErrorEndpointNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCLIProductionDiscoveryFixture()
			fixture.http.responses = tc.responses
			factory := NewProductionWebMCPDoctorFactory(
				WithWebMCPProductionRuntime(&productionFakeRuntime{}),
				WithWebMCPProductionHTTPClient(fixture.http),
				WithWebMCPProductionActivePortReader(fixture.activePort),
				WithWebMCPProductionProcessEnumerator(fixture.process),
				WithWebMCPProductionIDMapper(cliProductionIDMapper{}),
			)
			result := executeShippedWebMCPCommand(t, writeDoctorConfig(t, tc.config), factory, "browsers", "--json")
			assertClassifiedDiscoveryFailure(t, result, tc.wantCode, tc.wantDetail, tc.forbiddenText)
		})
	}
}

// assertClassifiedDiscoveryFailure requires a failed command whose JSON error
// envelope carries the classified code and never exposes endpoint secrets or
// internal lane names.
func assertClassifiedDiscoveryFailure(t *testing.T, result directCommandResult, wantCode webmcp.ErrorCode, wantDetail string, forbiddenText []string) {
	t.Helper()
	if result.err == nil {
		t.Fatalf("CLI exit status = 0, want 1; stdout=%s", result.stdout)
	}
	if result.stderr != "" {
		t.Fatalf("CLI stderr = %q, want empty", result.stderr)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(wantCode) {
		t.Fatalf("error envelope = %+v, want code %q", envelope, wantCode)
	}
	if wantDetail != "" && envelope.Error.Details["phase"] != wantDetail {
		t.Fatalf("error details = %+v, want phase %q", envelope.Error.Details, wantDetail)
	}
	for _, forbidden := range forbiddenText {
		if strings.Contains(result.stdout, forbidden) {
			t.Fatalf("error output exposed %q: %s", forbidden, result.stdout)
		}
	}
	if strings.Contains(result.stdout, "Lane B") || strings.Contains(result.stdout, "Lane D") {
		t.Fatalf("error output exposed an internal lane name: %s", result.stdout)
	}
}

func executeShippedWebMCPCommand(t *testing.T, configDir string, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	root := &cobra.Command{Use: "agent", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().StringVarP(&globalFlags.ConfigDirPath, "config-dir", "C", "", "Directory for agent CLI config")
	root.AddCommand(NewWebMCPCommand(globalFlags, factory).Generate())
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"--config-dir", configDir, "webmcp"}, args...))
	err := root.ExecuteContext(context.Background())
	return directCommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

type cliProductionDiscoveryFixture struct {
	http       *cliProductionHTTPClient
	activePort *cliProductionActivePortReader
	process    *cliProductionProcessEnumerator
}

// assertDiscoveryCalls requires exactly one version request to wantHost and
// the expected active-port and process discovery call counts.
func (f *cliProductionDiscoveryFixture) assertDiscoveryCalls(t *testing.T, wantHost string, wantActiveCalls, wantProcessCalls int) {
	t.Helper()
	if got := f.http.requestHosts(); len(got) != 1 || got[0] != wantHost {
		t.Fatalf("version request hosts = %v, want [%s]", got, wantHost)
	}
	if len(f.activePort.calls) != wantActiveCalls {
		t.Fatalf("active-port calls = %d, want %d", len(f.activePort.calls), wantActiveCalls)
	}
	if len(f.process.calls) != wantProcessCalls {
		t.Fatalf("process discovery calls = %d, want %d", len(f.process.calls), wantProcessCalls)
	}
}

func newCLIProductionDiscoveryFixture() *cliProductionDiscoveryFixture {
	return &cliProductionDiscoveryFixture{
		http: &cliProductionHTTPClient{responses: map[string]cliHTTPResponse{
			"127.0.0.1:9222": {body: cliVersionBody("127.0.0.1:9222", "configured")},
			"127.0.0.1:9223": {body: cliVersionBody("127.0.0.1:9223", "flag")},
			"127.0.0.1:9224": {body: cliVersionBody("127.0.0.1:9224", "active")},
			"127.0.0.1:9225": {body: cliVersionBody("127.0.0.1:9225", "process")},
		}},
		activePort: &cliProductionActivePortReader{record: discovery.ActivePortRecord{Port: 9224, BrowserWebSocketPath: "/devtools/browser/active"}},
		process: &cliProductionProcessEnumerator{items: []discovery.ProcessInfo{{
			PID:              42,
			Name:             "Chrome",
			DebuggingEnabled: true,
			Endpoint:         discovery.Endpoint{CDPURL: "http://127.0.0.1:9225"},
		}}},
	}
}

func cliVersionBody(host, name string) string {
	label := name
	if label != "" {
		label = strings.ToUpper(label[:1]) + label[1:]
	}
	return fmt.Sprintf(`{"Browser":"Chrome/%s","Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://%s/devtools/browser/%s"}`, label, host, name)
}

type cliHTTPResponse struct {
	status int
	body   string
	err    error
}

type cliProductionHTTPClient struct {
	mu        sync.Mutex
	responses map[string]cliHTTPResponse
	calls     []*http.Request
}

func (c *cliProductionHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, request.Clone(request.Context()))
	response := c.responses[request.URL.Host]
	c.mu.Unlock()
	if response.err != nil {
		return nil, response.err
	}
	if response.status == 0 {
		response.status = http.StatusOK
	}
	return &http.Response{
		StatusCode: response.status,
		Body:       io.NopCloser(strings.NewReader(response.body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (c *cliProductionHTTPClient) requestHosts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	hosts := make([]string, 0, len(c.calls))
	for _, request := range c.calls {
		hosts = append(hosts, request.URL.Host)
	}
	return hosts
}

type cliProductionActivePortReader struct {
	calls  []string
	record discovery.ActivePortRecord
	err    error
}

func (r *cliProductionActivePortReader) Read(_ context.Context, userDataDir string) (discovery.ActivePortRecord, error) {
	r.calls = append(r.calls, userDataDir)
	return r.record, r.err
}

type cliProductionProcessEnumerator struct {
	calls []int
	items []discovery.ProcessInfo
	err   error
}

func (e *cliProductionProcessEnumerator) List(context.Context) ([]discovery.ProcessInfo, error) {
	e.calls = append(e.calls, len(e.calls)+1)
	return append([]discovery.ProcessInfo(nil), e.items...), e.err
}

type cliProductionIDMapper struct{}

func (cliProductionIDMapper) BrowserID(identity discovery.BrowserIdentity) string {
	name := strings.TrimPrefix(identity.Path, "/devtools/browser/")
	name = strings.Trim(name, "/")
	if name == "" {
		name = "unknown"
	}
	return "browser-" + name
}

type cliProductionClock struct{ now time.Time }

func (c cliProductionClock) Now() time.Time { return c.now }
