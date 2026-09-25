package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func TestManagedProductionDiscoveryInjectsEndpointAndDetachKeepsBrowserWarm(t *testing.T) {
	configDir := t.TempDir()
	control := &managedCompositionTestControl{}
	var starts atomic.Int32
	manager := newManagedCompositionTestManager(configDir, control, &starts)
	browserConfig := config.DefaultBrowserConfig()
	browserConfig.Tools.Enabled = true
	browserConfig.Managed.Open = "about:blank"

	discoveryFake := &managedCompositionDiscoveryFake{}
	composition := &productionWebMCPComposition{
		browser:        browserConfig,
		configDir:      configDir,
		inputs:         productionDiscoveryInputs(browserConfig),
		discovery:      discoveryFake,
		managedManager: manager,
		httpClient:     &http.Client{Transport: managedCompositionVersionTransport{}},
		coreCandidates: make(map[string]webmcp.BrowserCandidate),
		laneCandidates: make(map[string]discovery.BrowserCandidate),
		endpoints:      make(map[string]discovery.Endpoint),
	}
	wrapped := &managedWebMCPDiscoveryService{owner: composition, delegate: discoveryFake}

	candidates, err := wrapped.DiscoverAll(context.Background(), discovery.ConnectionInputs{
		UserDataDir:      "/customer/profile",
		AllowProcessScan: true,
	})
	if err != nil {
		t.Fatalf("managed DiscoverAll(): %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != "managed-browser" {
		t.Fatalf("managed candidates = %+v", candidates)
	}
	if starts.Load() != 1 {
		t.Fatalf("managed launch count = %d, want one", starts.Load())
	}
	inputs := discoveryFake.lastInputs()
	if !strings.HasPrefix(inputs.CDPURL, "http://127.0.0.1:") || inputs.UserDataDir != "" || inputs.AllowProcessScan {
		t.Fatalf("managed discovery inputs = %+v, want only manager loopback endpoint", inputs)
	}
	statePath := chrome.ManagedBrowserStatePath(configDir)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("managed state after discovery: %v", err)
	}

	if err := composition.Close(); err != nil {
		t.Fatalf("composition Close(): %v", err)
	}
	if discoveryFake.closeCalls.Load() != 1 {
		t.Fatalf("discovery close calls = %d, want one", discoveryFake.closeCalls.Load())
	}
	if control.terminate.Load() != 0 {
		t.Fatalf("normal composition close terminated managed process: %d", control.terminate.Load())
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("managed state after normal detach: %v", err)
	}

	composition.mu.Lock()
	managedBrowser := composition.managedBrowser
	composition.mu.Unlock()
	if managedBrowser == nil {
		t.Fatal("composition did not retain managed browser")
	}
	if err := managedBrowser.Close(); err != nil {
		t.Fatalf("explicit managed Close(): %v", err)
	}
	if control.terminate.Load() != 1 {
		t.Fatalf("explicit managed close terminate calls = %d, want one", control.terminate.Load())
	}
}

func TestExternalProductionCompositionDoesNotAcquireManagedBrowser(t *testing.T) {
	browserConfig := config.DefaultBrowserConfig()
	browserConfig.Connection.CDPURL = "http://127.0.0.1:9222/json/version"
	composition := &productionWebMCPComposition{browser: browserConfig}
	browser, err := composition.ensureManagedBrowser(context.Background())
	if err != nil {
		t.Fatalf("external ensureManagedBrowser(): %v", err)
	}
	if browser != nil {
		t.Fatalf("external composition returned managed browser %#v", browser)
	}
}

func TestManagedProductionCloseOnExitClearsStateAndStopsExactBrowser(t *testing.T) {
	configDir := t.TempDir()
	control := &managedCompositionTestControl{}
	var starts atomic.Int32
	manager := newManagedCompositionTestManager(configDir, control, &starts)
	browserConfig := config.DefaultBrowserConfig()
	browserConfig.Tools.Enabled = true
	browserConfig.Managed.CloseOnExit = true
	discoveryFake := &managedCompositionDiscoveryFake{}
	composition := &productionWebMCPComposition{
		browser:        browserConfig,
		configDir:      configDir,
		inputs:         productionDiscoveryInputs(browserConfig),
		discovery:      discoveryFake,
		managedManager: manager,
		httpClient:     &http.Client{Transport: managedCompositionVersionTransport{}},
		coreCandidates: make(map[string]webmcp.BrowserCandidate),
		laneCandidates: make(map[string]discovery.BrowserCandidate),
		endpoints:      make(map[string]discovery.Endpoint),
	}
	wrapped := &managedWebMCPDiscoveryService{owner: composition, delegate: discoveryFake}
	if _, err := wrapped.DiscoverAll(context.Background(), discovery.ConnectionInputs{}); err != nil {
		t.Fatalf("managed DiscoverAll(): %v", err)
	}
	statePath := chrome.ManagedBrowserStatePath(configDir)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state before close-on-exit: %v", err)
	}
	if err := composition.Close(); err != nil {
		t.Fatalf("close-on-exit composition Close(): %v", err)
	}
	if control.terminate.Load() != 1 || control.kill.Load() != 0 {
		t.Fatalf("close-on-exit terminate/kill = %d/%d, want 1/0", control.terminate.Load(), control.kill.Load())
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state after close-on-exit = %v, want removed", err)
	}
	deadline := time.Now().Add(time.Second)
	lockPath := filepath.Join(filepath.Dir(statePath), ".managed-browser.lock")
	for {
		_, err := os.Stat(lockPath)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("close-on-exit lifecycle lock remains: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func newManagedCompositionTestManager(configDir string, control *managedCompositionTestControl, starts *atomic.Int32) *chrome.ManagedBrowserManager {
	return chrome.NewManagedBrowserManager(chrome.ManagedBrowserManagerOptions{
		ConfigDir: configDir,
		LaunchOptions: chrome.ManagedBrowserLaunchOptions{
			ConfigDir:        configDir,
			DisplayAvailable: func() bool { return true },
			Acquirer: chrome.ManagedChromeExecutableAcquirerFunc(func(context.Context) (chrome.ChromeExecutable, error) {
				return chrome.ChromeExecutable{Path: "/qualified/test-chrome", Major: 152, Source: chrome.ExecutableSourceStock}, nil
			}),
			HTTPClient: &http.Client{Transport: managedCompositionVersionTransport{}},
			ProcessStarter: func(string, []string) (chrome.ManagedBrowserProcess, error) {
				starts.Add(1)
				return control.newProcess(7002), nil
			},
			StartupTimeout:  500 * time.Millisecond,
			PollInterval:    time.Millisecond,
			ShutdownTimeout: 50 * time.Millisecond,
		},
		ProcessInspector: chrome.ManagedBrowserProcessInspectorFunc(func(_ context.Context, state chrome.ManagedBrowserState) (chrome.ManagedBrowserProcessInfo, error) {
			return chrome.ManagedBrowserProcessInfo{PID: state.PID, Identity: "composition-incarnation", ProfileDir: state.ProfileDir}, nil
		}),
		ProcessReattacher: func(context.Context, chrome.ManagedBrowserState) (chrome.ManagedBrowserProcess, error) {
			return control.newProcess(7002), nil
		},
		LockTimeout: 2 * time.Second,
		LockPoll:    time.Millisecond,
	})
}

type managedCompositionVersionTransport struct{}

func (managedCompositionVersionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Path != "/json/version" {
		return nil, errors.New("unexpected readiness request")
	}
	body := fmt.Sprintf(`{"Browser":"Google Chrome 152.0.1.2","Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://127.0.0.1:%s/devtools/browser/composition"}`, request.URL.Port())
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

type managedCompositionTestControl struct {
	done      chan struct{}
	initOnce  sync.Once
	exitOnce  sync.Once
	terminate atomic.Int32
	kill      atomic.Int32
}

func (c *managedCompositionTestControl) newProcess(pid int) *managedCompositionTestProcess {
	c.initOnce.Do(func() { c.done = make(chan struct{}) })
	return &managedCompositionTestProcess{control: c, pid: pid}
}

func (c *managedCompositionTestControl) exit() {
	c.initOnce.Do(func() { c.done = make(chan struct{}) })
	c.exitOnce.Do(func() { close(c.done) })
}

type managedCompositionTestProcess struct {
	control *managedCompositionTestControl
	pid     int
}

func (p *managedCompositionTestProcess) Wait() error {
	if p == nil || p.control == nil {
		return errors.New("test process unavailable")
	}
	<-p.control.done
	return nil
}

func (p *managedCompositionTestProcess) Terminate() error {
	p.control.terminate.Add(1)
	p.control.exit()
	return nil
}

func (p *managedCompositionTestProcess) Kill() error {
	p.control.kill.Add(1)
	p.control.exit()
	return nil
}

func (p *managedCompositionTestProcess) PID() int { return p.pid }

type managedCompositionDiscoveryFake struct {
	mu         sync.Mutex
	inputs     discovery.ConnectionInputs
	closeCalls atomic.Int32
}

func (d *managedCompositionDiscoveryFake) DiscoverAll(_ context.Context, inputs discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	d.mu.Lock()
	d.inputs = inputs
	d.mu.Unlock()
	return []discovery.BrowserCandidate{{ID: "managed-browser", Source: discovery.SourceExplicitCDPHTTP, Loopback: true}}, nil
}

func (d *managedCompositionDiscoveryFake) lastInputs() discovery.ConnectionInputs {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inputs
}

func (d *managedCompositionDiscoveryFake) ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	return discovery.TargetSnapshot{}, nil
}

func (d *managedCompositionDiscoveryFake) Select(context.Context, discovery.TargetSelectionRequest) (discovery.Selection, error) {
	return discovery.Selection{}, nil
}

func (d *managedCompositionDiscoveryFake) Selected() (discovery.Selection, bool) {
	return discovery.Selection{}, false
}

func (d *managedCompositionDiscoveryFake) RefreshSelection(context.Context) (discovery.Selection, error) {
	return discovery.Selection{}, nil
}

func (d *managedCompositionDiscoveryFake) Close() error {
	d.closeCalls.Add(1)
	return nil
}
