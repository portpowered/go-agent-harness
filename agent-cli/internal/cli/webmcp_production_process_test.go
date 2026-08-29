package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/spf13/cobra"
)

const (
	osProcessFixtureChildEnv      = "WEBMCP_DIRECT_CROSS_PROCESS_CHILD"
	osProcessFixtureEndpointEnv   = "WEBMCP_DIRECT_CROSS_PROCESS_ENDPOINT"
	osProcessFixtureConfigDirEnv  = "WEBMCP_DIRECT_CROSS_PROCESS_CONFIG_DIR"
	osProcessFixtureToolRefEnv    = "WEBMCP_DIRECT_CROSS_PROCESS_TOOL_REF"
	osProcessFixtureInvocationEnv = "WEBMCP_DIRECT_CROSS_PROCESS_INVOCATION_ID"
)

// TestProductionWebMCPDirectCommandsCancelAcrossOSProcessesAndRecover uses a
// loopback fixture server as the browser-side state owner. The invoke and
// cancel commands are separate test-binary processes: neither process shares
// a broker, runtime, session, or invocation registry with the other.
func TestProductionWebMCPDirectCommandsCancelAcrossOSProcessesAndRecover(t *testing.T) {
	if os.Getenv(osProcessFixtureChildEnv) != "" {
		runOSProcessWebMCPChild(t)
		return
	}

	fixture := newOSProcessWebMCPFixture(t)
	defer fixture.Close()
	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
  selection:
    persist: true
`, fixture.server.URL+"/json/version"))
	store := NewFileWebMCPSelectionStore(configDir)

	factory := osProcessFixtureFactory(fixture.server.URL)
	selected := executeDirectCommand(t, configDir, store, factory, "select", "--browser", fixture.browserID, "--tab", string(fixture.publicTargetID), "--json")
	requireDirectSuccess(t, selected)
	toolsResult := executeDirectCommand(t, configDir, store, factory, "tools", "--json")
	toolsEnvelope := requireDirectSuccess(t, toolsResult)
	var toolsData WebMCPDirectToolsData
	decodeDirectData(t, toolsEnvelope.Data, &toolsData)
	if len(toolsData.Tools) != 1 {
		t.Fatalf("fixture tools = %+v", toolsData.Tools)
	}
	toolRef := toolsData.Tools[0].Ref

	invoke := startOSProcessWebMCPChild(t, "invoke", fixture.server.URL, configDir, toolRef, "")
	invokeAlive := true
	defer func() {
		if invokeAlive {
			_ = invoke.command.Process.Kill()
			_, _ = invoke.command.Process.Wait()
		}
	}()

	var receipt WebMCPDirectInvocationReceipt
	select {
	case line := <-invoke.stderr.firstLine:
		if err := json.Unmarshal([]byte(line), &receipt); err != nil {
			t.Fatalf("decode cross-process dispatch receipt: %v; stderr=%q", err, line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("invoke child did not emit a dispatch receipt")
	}
	if receipt.InvocationID == "" || receipt.InvocationID != string(fixture.firstInvocationID()) || receipt.ToolRef != toolRef || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("cross-process receipt = %+v, want the exact first browser invocation", receipt)
	}

	cancel := startOSProcessWebMCPChild(t, "cancel", fixture.server.URL, configDir, "", receipt.InvocationID)
	if err := cancel.Wait(); err != nil {
		t.Fatalf("cancel child: %v\nstdout=%s\nstderr=%s", err, cancel.stdout.String(), cancel.stderr.String())
	}
	cancelEnvelope := decodeDirectEnvelope(t, cancel.stdout.String())
	if !cancelEnvelope.OK {
		t.Fatalf("cross-process cancel envelope = %+v", cancelEnvelope)
	}
	var cancelData WebMCPDirectCancelData
	decodeDirectData(t, cancelEnvelope.Data, &cancelData)
	if cancelData.InvocationID != receipt.InvocationID || cancelData.Status != "canceled" || cancelData.Phase != "terminal" || cancelData.Outcome != "confirmed_canceled" {
		t.Fatalf("cross-process cancel data = %+v", cancelData)
	}
	if cancel.stderr.String() != "" {
		t.Fatalf("cancel child wrote stderr = %q", cancel.stderr.String())
	}

	invokeErr := invoke.Wait()
	invokeAlive = false
	if invokeErr == nil {
		t.Fatal("invoke child exited successfully after cancellation")
	}
	invokeEnvelope := decodeDirectEnvelope(t, invoke.stdout.String())
	if invokeEnvelope.OK || invokeEnvelope.Error == nil || invokeEnvelope.Error.Code != string(webmcp.ErrorInvocationCanceled) {
		t.Fatalf("canceled invoke envelope = %+v", invokeEnvelope)
	}
	if invokeEnvelope.Error.Details["invocation_id"] != receipt.InvocationID {
		t.Fatalf("canceled invoke ID = %#v, want %q", invokeEnvelope.Error.Details["invocation_id"], receipt.InvocationID)
	}
	if strings.TrimSpace(strings.TrimPrefix(invoke.stderr.String(), receiptLine(receipt))) != "" {
		t.Fatalf("invoke child wrote unexpected stderr = %q", invoke.stderr.String())
	}

	recovered := startOSProcessWebMCPChild(t, "recover", fixture.server.URL, configDir, toolRef, "")
	if err := recovered.Wait(); err != nil {
		t.Fatalf("recovery child: %v\nstdout=%s\nstderr=%s", err, recovered.stdout.String(), recovered.stderr.String())
	}
	recoveredEnvelope := decodeDirectEnvelope(t, recovered.stdout.String())
	if !recoveredEnvelope.OK {
		t.Fatalf("recovery envelope = %+v", recoveredEnvelope)
	}
	var recoveredData WebMCPDirectInvocation
	decodeDirectData(t, recoveredEnvelope.Data, &recoveredData)
	if recoveredData.Status != string(webmcp.InvocationCompleted) || string(recoveredData.Output) != `{"recovered":true}` {
		t.Fatalf("recovery data = %+v", recoveredData)
	}
	var recoveredReceipt WebMCPDirectInvocationReceipt
	if err := json.Unmarshal([]byte(recovered.stderr.String()), &recoveredReceipt); err != nil {
		t.Fatalf("decode recovery receipt: %v; stderr=%q", err, recovered.stderr.String())
	}
	if recoveredReceipt.InvocationID != "browser-os-2" || recoveredReceipt.ToolRef != toolRef || recoveredReceipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("recovery receipt = %+v", recoveredReceipt)
	}

	for _, output := range []string{selected.stdout, toolsResult.stdout, cancel.stdout.String(), invoke.stdout.String(), invoke.stderr.String(), recovered.stdout.String(), recovered.stderr.String()} {
		for _, secret := range []string{"secret", "#fragment", "query", "127.0.0.1"} {
			if strings.Contains(output, secret) {
				t.Fatalf("cross-process output exposed %q: %s", secret, output)
			}
		}
	}
}

func receiptLine(receipt WebMCPDirectInvocationReceipt) string {
	encoded, _ := json.Marshal(receipt)
	return string(append(encoded, '\n'))
}

type osProcessWebMCPChild struct {
	command *exec.Cmd
	stdout  *childProcessOutputBuffer
	stderr  *childProcessStderrBuffer
}

func startOSProcessWebMCPChild(t *testing.T, mode, endpoint, configDir, toolRef, invocationID string) *osProcessWebMCPChild {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestProductionWebMCPDirectCommandsCancelAcrossOSProcessesAndRecover$", "-test.v=false")
	command.Env = append(os.Environ(),
		osProcessFixtureChildEnv+"="+mode,
		osProcessFixtureEndpointEnv+"="+endpoint,
		osProcessFixtureConfigDirEnv+"="+configDir,
		osProcessFixtureToolRefEnv+"="+toolRef,
		osProcessFixtureInvocationEnv+"="+invocationID,
	)
	stdout := &childProcessOutputBuffer{}
	stderr := newChildProcessStderrBuffer()
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start %s child: %v", mode, err)
	}
	return &osProcessWebMCPChild{command: command, stdout: stdout, stderr: stderr}
}

func (p *osProcessWebMCPChild) Wait() error {
	return p.command.Wait()
}

func runOSProcessWebMCPChild(t *testing.T) {
	mode := os.Getenv(osProcessFixtureChildEnv)
	endpoint := os.Getenv(osProcessFixtureEndpointEnv)
	configDir := os.Getenv(osProcessFixtureConfigDirEnv)
	toolRef := os.Getenv(osProcessFixtureToolRefEnv)
	invocationID := os.Getenv(osProcessFixtureInvocationEnv)
	store := NewFileWebMCPSelectionStore(configDir)
	factory := osProcessFixtureFactory(endpoint)
	var err error
	switch mode {
	case "invoke":
		err = runOSProcessDirectCommand(t, configDir, store, factory, "invoke", "--tool-ref", toolRef, "--input-json", `{"value":7}`, "--timeout", "5s", "--json")
		if err == nil {
			os.Exit(41)
		}
		os.Exit(42)
	case "cancel":
		err = runOSProcessDirectCommand(t, configDir, store, factory, "cancel", "--invocation", invocationID, "--json")
		if err != nil {
			os.Exit(43)
		}
		os.Exit(0)
	case "recover":
		err = runOSProcessDirectCommand(t, configDir, store, factory, "invoke", "--tool-ref", toolRef, "--input-json", `{"value":8}`, "--timeout", "5s", "--json")
		if err != nil {
			os.Exit(44)
		}
		os.Exit(0)
	default:
		os.Exit(45)
	}
}

func runOSProcessDirectCommand(t *testing.T, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) error {
	t.Helper()
	globalFlags := newDirectGlobalFlags(configDir)
	operations := NewWebMCPOperationsCommand(globalFlags, factory)
	operations.SelectionStore = store
	root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
	operations.AddCommands(root)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.SetArgs(args)
	return root.Execute()
}

func newDirectGlobalFlags(configDir string) *flags.GlobalFlags {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	return globalFlags
}

func osProcessFixtureFactory(endpoint string) WebMCPDoctorFactory {
	return NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(newOSProcessWebMCPRuntime(endpoint)),
	)
}

type osProcessWebMCPFixture struct {
	server         *httptest.Server
	mu             sync.Mutex
	browserID      string
	publicTargetID webmcp.TargetID
	target         webmcp.Target
	tool           webmcp.ToolDescriptor
	next           int
	invocations    map[webmcp.InvocationID]*osProcessFixtureInvocation
}

type osProcessFixtureInvocation struct {
	status string
	output json.RawMessage
}

func newOSProcessWebMCPFixture(t *testing.T) *osProcessWebMCPFixture {
	t.Helper()
	fixture := &osProcessWebMCPFixture{
		target: webmcp.Target{
			ID:               "raw-tab",
			Type:             "page",
			Title:            "Cross-process fixture",
			URL:              "https://fixture.test/cancel?secret=query#fragment",
			Origin:           "https://fixture.test",
			ContinuityMarker: "document-cross-process",
			Generation:       1,
			Eligible:         true,
		},
		tool: webmcp.ToolDescriptor{
			Name:        "set_state",
			Description: "Mutate fixture state",
			FrameID:     "frame-1",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"required":["value"],"additionalProperties":false}`),
			Origin:      "https://fixture.test",
			Generation:  1,
		},
		invocations: make(map[webmcp.InvocationID]*osProcessFixtureInvocation),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	browserWS := "ws" + strings.TrimPrefix(fixture.server.URL, "http") + "/devtools/browser/cross-process"
	parsed, err := url.Parse(browserWS)
	if err != nil {
		t.Fatalf("parse fixture browser websocket: %v", err)
	}
	fixture.browserID = string(discovery.HashIDMapper{}.BrowserID(discovery.BrowserIdentity{
		Scheme: parsed.Scheme,
		Host:   parsed.Hostname(),
		Port:   parsed.Port(),
		Path:   parsed.EscapedPath(),
	}))
	fixture.target.BrowserID = webmcp.BrowserID(fixture.browserID)
	fixture.publicTargetID = webmcp.TargetID(discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{
		BrowserID: fixture.browserID,
		RawID:     string(fixture.target.ID),
	}))
	return fixture
}

func (f *osProcessWebMCPFixture) Close() {
	if f != nil && f.server != nil {
		f.server.Close()
	}
}

func (f *osProcessWebMCPFixture) firstInvocationID() webmcp.InvocationID {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next == 0 {
		return ""
	}
	return webmcp.InvocationID(fmt.Sprintf("browser-os-%d", 1))
}

func (f *osProcessWebMCPFixture) handle(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/json/version":
		browserWS := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/devtools/browser/cross-process"
		_, _ = fmt.Fprintf(writer, `{"Browser":"Chrome/Fixture","Protocol-Version":"1.3","webSocketDebuggerUrl":%q}`, browserWS)
	case "/fixture/targets":
		f.mu.Lock()
		target := f.target
		f.mu.Unlock()
		_ = json.NewEncoder(writer).Encode([]osProcessFixtureTarget{{
			ID:               string(target.ID),
			Type:             target.Type,
			Title:            target.Title,
			URL:              target.URL,
			Origin:           target.Origin,
			ContinuityMarker: target.ContinuityMarker,
		}})
	case "/fixture/invoke":
		var requestBody osProcessFixtureInvokeRequest
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&requestBody); err != nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.next++
		id := webmcp.InvocationID(fmt.Sprintf("browser-os-%d", f.next))
		status := "pending"
		var output json.RawMessage
		if f.next > 1 {
			status = "completed"
			output = json.RawMessage(`{"recovered":true}`)
		}
		f.invocations[id] = &osProcessFixtureInvocation{status: status, output: output}
		f.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(osProcessFixtureInvokeResponse{InvocationID: id, Status: status, Output: output})
	case "/fixture/cancel":
		var requestBody osProcessFixtureCancelRequest
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&requestBody); err != nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		invocation := f.invocations[webmcp.InvocationID(requestBody.InvocationID)]
		if invocation == nil || invocation.status != "pending" {
			f.mu.Unlock()
			http.Error(writer, "invocation is not pending", http.StatusConflict)
			return
		}
		invocation.status = "canceled"
		f.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(map[string]string{"status": "cancel_requested"})
	case "/fixture/status":
		invocationID := webmcp.InvocationID(request.URL.Query().Get("invocation_id"))
		f.mu.Lock()
		invocation := f.invocations[invocationID]
		if invocation == nil {
			f.mu.Unlock()
			http.Error(writer, "invocation not found", http.StatusNotFound)
			return
		}
		response := osProcessFixtureInvokeResponse{InvocationID: invocationID, Status: invocation.status, Output: append(json.RawMessage(nil), invocation.output...)}
		f.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(response)
	default:
		http.NotFound(writer, request)
	}
}

type osProcessFixtureTarget struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	URL              string `json:"url"`
	Origin           string `json:"origin"`
	ContinuityMarker string `json:"continuity_marker"`
}

type osProcessFixtureInvokeRequest struct {
	TargetID string          `json:"target_id"`
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
}

type osProcessFixtureInvokeResponse struct {
	InvocationID webmcp.InvocationID `json:"invocation_id"`
	Status       string              `json:"status"`
	Output       json.RawMessage     `json:"output,omitempty"`
}

type osProcessFixtureCancelRequest struct {
	TargetID     string `json:"target_id"`
	InvocationID string `json:"invocation_id"`
}

type osProcessWebMCPRuntime struct {
	endpoint  string
	client    *http.Client
	browserID webmcp.BrowserID
	target    webmcp.Target
	tool      webmcp.ToolDescriptor
}

func newOSProcessWebMCPRuntime(endpoint string) *osProcessWebMCPRuntime {
	return &osProcessWebMCPRuntime{
		endpoint: endpoint,
		client:   http.DefaultClient,
		target: webmcp.Target{
			ID:               "raw-tab",
			Type:             "page",
			Title:            "Cross-process fixture",
			URL:              "https://fixture.test/cancel?secret=query#fragment",
			Origin:           "https://fixture.test",
			ContinuityMarker: "document-cross-process",
			Generation:       1,
			WebSocketURL:     "ws" + strings.TrimPrefix(endpoint, "http") + "/devtools/page/raw-tab",
			Eligible:         true,
		},
		tool: webmcp.ToolDescriptor{
			Name:        "set_state",
			Description: "Mutate fixture state",
			FrameID:     "frame-1",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"required":["value"],"additionalProperties":false}`),
			Origin:      "https://fixture.test",
			Generation:  1,
		},
	}
}

func (r *osProcessWebMCPRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.browserID = candidate.ID
	r.target.BrowserID = candidate.ID
	return &osProcessWebMCPHandle{runtime: r, candidate: candidate}, nil
}

type osProcessWebMCPHandle struct {
	runtime   *osProcessWebMCPRuntime
	candidate webmcp.BrowserCandidate
	mu        sync.Mutex
	closed    bool
}

func (h *osProcessWebMCPHandle) Candidate() webmcp.BrowserCandidate { return h.candidate }

func (h *osProcessWebMCPHandle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, webmcp.ErrClosed
	}
	var targets []osProcessFixtureTarget
	if err := h.runtime.doJSON(ctx, http.MethodGet, "/fixture/targets", nil, &targets); err != nil {
		return nil, err
	}
	result := make([]webmcp.Target, 0, len(targets))
	for _, target := range targets {
		result = append(result, webmcp.Target{
			BrowserID:        h.candidate.ID,
			ID:               webmcp.TargetID(target.ID),
			Type:             target.Type,
			Title:            target.Title,
			URL:              target.URL,
			Origin:           target.Origin,
			ContinuityMarker: target.ContinuityMarker,
			Generation:       1,
			WebSocketURL:     h.runtime.target.WebSocketURL,
			Eligible:         true,
		})
	}
	return result, nil
}

func (h *osProcessWebMCPHandle) Activate(ctx context.Context, _ webmcp.TargetID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (h *osProcessWebMCPHandle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if targetID != h.runtime.target.ID {
		return nil, webmcp.ErrTargetNotFound
	}
	page := webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: h.candidate.ID, TargetID: targetID},
		Title:      h.runtime.target.Title,
		URL:        h.runtime.target.URL,
		Origin:     h.runtime.target.Origin,
		Generation: 1,
		Connected:  true,
	}
	return newOSProcessWebMCPFixtureSession(h.runtime, page, ownership), nil
}

func (h *osProcessWebMCPHandle) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	return nil
}

type osProcessWebMCPFixtureSession struct {
	runtime   *osProcessWebMCPRuntime
	page      webmcp.PageContext
	ownership webmcp.TargetOwnership
	events    chan webmcp.BrowserEvent
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool
	ready     bool
}

func newOSProcessWebMCPFixtureSession(runtime *osProcessWebMCPRuntime, page webmcp.PageContext, ownership webmcp.TargetOwnership) *osProcessWebMCPFixtureSession {
	return &osProcessWebMCPFixtureSession{
		runtime:   runtime,
		page:      page,
		ownership: ownership,
		events:    make(chan webmcp.BrowserEvent, 16),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (s *osProcessWebMCPFixtureSession) Context() webmcp.PageContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := s.page
	page.Ready = s.ready
	return page
}

func (s *osProcessWebMCPFixtureSession) Ownership() webmcp.TargetOwnership { return s.ownership }

func (s *osProcessWebMCPFixtureSession) EnableWebMCP(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	s.ready = true
	s.mu.Unlock()
	tool := s.runtime.tool
	tool.BrowserID = s.page.Key.BrowserID
	tool.TargetID = s.page.Key.TargetID
	tool.Generation = s.page.Generation
	s.send(webmcp.BrowserEvent{Type: webmcp.EventToolsAdded, Tools: []webmcp.ToolDescriptor{tool}, Generation: s.page.Generation})
	return nil
}

func (s *osProcessWebMCPFixtureSession) Events() <-chan webmcp.BrowserEvent { return s.events }

func (s *osProcessWebMCPFixtureSession) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var response osProcessFixtureInvokeResponse
	if err := s.runtime.doJSON(ctx, http.MethodPost, "/fixture/invoke", osProcessFixtureInvokeRequest{
		TargetID: string(s.page.Key.TargetID),
		ToolName: toolName,
		Input:    append(json.RawMessage(nil), input...),
	}, &response); err != nil {
		return "", err
	}
	if response.InvocationID == "" {
		return "", errors.New("fixture returned an empty invocation ID")
	}
	s.send(webmcp.BrowserEvent{Type: webmcp.EventToolInvoked, FrameID: frameID, ToolName: toolName, Input: append(json.RawMessage(nil), input...), InvocationID: response.InvocationID})
	go s.watchInvocation(response.InvocationID)
	return response.InvocationID, nil
}

func (s *osProcessWebMCPFixtureSession) CancelWebMCP(ctx context.Context, invocationID webmcp.InvocationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var response map[string]string
	if err := s.runtime.doJSON(ctx, http.MethodPost, "/fixture/cancel", osProcessFixtureCancelRequest{
		TargetID:     string(s.page.Key.TargetID),
		InvocationID: string(invocationID),
	}, &response); err != nil {
		return err
	}
	go s.watchInvocation(invocationID)
	return nil
}

func (s *osProcessWebMCPFixtureSession) watchInvocation(invocationID webmcp.InvocationID) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		var response osProcessFixtureInvokeResponse
		if err := s.runtime.doJSON(context.Background(), http.MethodGet, "/fixture/status?invocation_id="+url.QueryEscape(string(invocationID)), nil, &response); err != nil {
			continue
		}
		if response.Status == "pending" {
			continue
		}
		status := "Completed"
		if response.Status == "canceled" {
			status = "Canceled"
		}
		s.send(webmcp.BrowserEvent{Type: webmcp.EventToolResponded, InvocationID: invocationID, Status: status, Output: append(json.RawMessage(nil), response.Output...)})
		return
	}
}

func (s *osProcessWebMCPFixtureSession) Done() <-chan struct{} { return s.done }

func (s *osProcessWebMCPFixtureSession) Err() error { return nil }

func (s *osProcessWebMCPFixtureSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.stop)
		close(s.events)
		close(s.done)
		s.mu.Unlock()
	})
	return nil
}

func (s *osProcessWebMCPFixtureSession) send(event webmcp.BrowserEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.events <- event
}

func (r *osProcessWebMCPRuntime) doJSON(ctx context.Context, method, path string, input any, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.endpoint, "/")+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("fixture HTTP status %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(output)
}

var (
	_ webmcp.BrowserRuntime = (*osProcessWebMCPRuntime)(nil)
	_ webmcp.BrowserHandle  = (*osProcessWebMCPHandle)(nil)
	_ webmcp.TargetSession  = (*osProcessWebMCPFixtureSession)(nil)
)
