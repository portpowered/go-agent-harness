package cli

// Managed-browser lifecycle fixtures shared by CLI session tests. The
// composition-level managed tests live in internal/webmcp/production.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
)

// testDevToolsVersionPath is the DevTools HTTP version endpoint served by the
// CLI's browser fixtures.
const testDevToolsVersionPath = "/json/version"

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
	if request == nil || request.URL == nil || request.URL.Path != testDevToolsVersionPath {
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
