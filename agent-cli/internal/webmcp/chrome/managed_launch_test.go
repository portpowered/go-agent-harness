package chrome

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedBrowserLauncherUsesPrivateProfileAndOneHeadfulStartupPage(t *testing.T) {
	configDir := t.TempDir()
	process := &managedLaunchTestProcess{}
	var executable string
	var args []string
	launcher := newManagedLaunchTestLauncher(t, process, ManagedChromeExecutableAcquirerFunc(func(_ context.Context) (ChromeExecutable, error) {
		return ChromeExecutable{Path: "/qualified/chrome", Version: "Google Chrome 152.0.1.2", Major: 152, Source: ExecutableSourceStock}, nil
	}), ManagedBrowserProcessStarter(func(path string, received []string) (ManagedBrowserProcess, error) {
		executable = path
		args = append([]string(nil), received...)
		return process, nil
	}))
	launcher.options.ConfigDir = configDir
	launcher.options.StartupURL = "https://example.test/start?token=redact#section"
	launcher.options.DisplayAvailable = func() bool { return true }
	launcher.options.StartupTimeout = time.Second

	browser, err := launcher.Launch(context.Background())
	if err != nil {
		t.Fatalf("Launch(): %v", err)
	}
	if executable != "/qualified/chrome" {
		t.Fatalf("started executable = %q", executable)
	}
	profileDir := filepath.Join(configDir, ManagedBrowserProfileDirName)
	if browser.ProfileDir() != profileDir {
		t.Fatalf("profile = %q, want %q", browser.ProfileDir(), profileDir)
	}
	info, err := os.Stat(profileDir)
	if err != nil {
		t.Fatalf("stat managed profile: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("managed profile mode/type = %s/%v, want private directory", info.Mode().Perm(), info.IsDir())
	}
	if browser.Headless() {
		t.Fatal("headful display was resolved as headless")
	}
	if browser.StartupURL() != launcher.options.StartupURL {
		t.Fatalf("startup URL = %q, want %q", browser.StartupURL(), launcher.options.StartupURL)
	}
	if process.terminateCalls.Load() != 0 {
		t.Fatal("successful launch terminated its process")
	}

	assertManagedLaunchArguments(t, args, profileDir, false, launcher.options.StartupURL)
	port := managedLaunchPortFromArgs(t, args)
	if browser.Endpoint().CDPURL != "http://127.0.0.1:"+strconv.Itoa(port)+"/json/version" {
		t.Fatalf("CDP endpoint = %q", browser.Endpoint().CDPURL)
	}
	if strings.Contains(browser.Endpoint().BrowserWSEndpoint, "redact") {
		t.Fatalf("endpoint retained websocket credentials/query: %q", browser.Endpoint().BrowserWSEndpoint)
	}

	if err := browser.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := browser.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if process.terminateCalls.Load() != 1 || process.killCalls.Load() != 0 {
		t.Fatalf("cleanup calls terminate/kill = %d/%d", process.terminateCalls.Load(), process.killCalls.Load())
	}
}

func TestManagedBrowserLauncherFallsBackToHeadlessWithoutDisplay(t *testing.T) {
	process := &managedLaunchTestProcess{}
	launcher := newManagedLaunchTestLauncher(t, process, nil, nil)
	launcher.options.DisplayAvailable = func() bool { return false }

	browser, err := launcher.Launch(context.Background())
	if err != nil {
		t.Fatalf("Launch(): %v", err)
	}
	defer func() { _ = browser.Close() }()
	if !browser.Headless() {
		t.Fatal("display-free launch was not resolved as headless")
	}
	assertManagedLaunchArguments(t, process.argsSnapshot(), browser.ProfileDir(), true, "about:blank")
}

func TestManagedBrowserLauncherExplicitHeadlessWinsWhenDisplayExists(t *testing.T) {
	process := &managedLaunchTestProcess{}
	launcher := newManagedLaunchTestLauncher(t, process, nil, nil)
	launcher.options.Headless = true
	launcher.options.DisplayAvailable = func() bool { return true }

	browser, err := launcher.Launch(context.Background())
	if err != nil {
		t.Fatalf("Launch(): %v", err)
	}
	defer func() { _ = browser.Close() }()
	if !browser.Headless() {
		t.Fatal("explicit headless request was ignored")
	}
	assertManagedLaunchArguments(t, process.argsSnapshot(), browser.ProfileDir(), true, "about:blank")
}

func TestManagedBrowserLauncherCancellationCleansOnlyFailedProcess(t *testing.T) {
	process := &managedLaunchTestProcess{}
	transport := managedLaunchVersionTransport{waitForRequest: true}
	launcher := newManagedLaunchTestLauncherWithTransport(t, process, transport, nil, nil)
	launcher.options.StartupTimeout = time.Second
	launcher.options.ShutdownTimeout = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	launcher.options.ProcessStarter = func(path string, args []string) (ManagedBrowserProcess, error) {
		close(started)
		process.setArgs(path, args)
		return process, nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := launcher.Launch(ctx)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("launcher did not start process")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil || !errors.Is(err, ErrManagedBrowserLaunch) {
			t.Fatalf("canceled Launch() error = %v, want managed launch error", err)
		}
		if !strings.Contains(err.Error(), "managed WebMCP browser launch failed") || strings.Contains(err.Error(), launcher.options.ConfigDir) {
			t.Fatalf("unsafe or incomplete cancellation error = %q", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled launch did not finish within its bound")
	}
	if process.terminateCalls.Load() != 1 {
		t.Fatalf("failed process terminate calls = %d, want one", process.terminateCalls.Load())
	}
}

func TestManagedBrowserLauncherEarlyExitReturnsSafeBoundedError(t *testing.T) {
	process := &managedLaunchTestProcess{}
	process.exit(nil)
	launcher := newManagedLaunchTestLauncherWithTransport(t, process, managedLaunchVersionTransport{alwaysError: errors.New("connection refused")}, nil, nil)
	launcher.options.StartupTimeout = time.Second

	started := time.Now()
	_, err := launcher.Launch(context.Background())
	if err == nil || !errors.Is(err, ErrManagedBrowserLaunch) {
		t.Fatalf("early-exit Launch() error = %v, want managed launch error", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("early-exit launch took %s, want bounded process-exit detection", elapsed)
	}
	if strings.Contains(err.Error(), "/qualified/chrome") || strings.Contains(err.Error(), launcher.options.ConfigDir) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("early-exit error leaked unsafe detail: %q", err)
	}
}

func TestManagedBrowserLauncherReadinessTimeoutTerminatesProcess(t *testing.T) {
	process := &managedLaunchTestProcess{}
	transport := managedLaunchVersionTransport{alwaysError: errors.New("connection refused")}
	launcher := newManagedLaunchTestLauncherWithTransport(t, process, transport, nil, nil)
	launcher.options.StartupTimeout = 40 * time.Millisecond
	launcher.options.PollInterval = 5 * time.Millisecond
	launcher.options.ShutdownTimeout = 20 * time.Millisecond

	started := time.Now()
	_, err := launcher.Launch(context.Background())
	if err == nil || !errors.Is(err, ErrManagedBrowserLaunch) {
		t.Fatalf("readiness-timeout Launch() error = %v, want managed launch error", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("readiness timeout took %s, want bounded completion", elapsed)
	}
	if process.terminateCalls.Load() != 1 {
		t.Fatalf("readiness failure terminate calls = %d, want one", process.terminateCalls.Load())
	}
	if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), launcher.options.ConfigDir) {
		t.Fatalf("readiness error leaked nested detail: %q", err)
	}
}

func TestManagedBrowserLauncherPortCollisionFailsTheAttempt(t *testing.T) {
	process := &managedLaunchTestProcess{}
	launcher := newManagedLaunchTestLauncherWithTransport(t, process, managedLaunchVersionTransport{alwaysError: errors.New("connection refused")}, nil, nil)
	launcher.options.StartupTimeout = 200 * time.Millisecond
	launcher.options.ShutdownTimeout = 20 * time.Millisecond
	var collision net.Listener
	launcher.options.ProcessStarter = func(path string, args []string) (ManagedBrowserProcess, error) {
		process.setArgs(path, args)
		port := managedLaunchPortFromArgs(t, args)
		var err error
		collision, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			return nil, err
		}
		process.exit(errors.New("Chrome could not bind its debugging port"))
		return process, nil
	}
	defer func() {
		if collision != nil {
			_ = collision.Close()
		}
	}()

	_, err := launcher.Launch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "managed WebMCP browser launch failed") {
		t.Fatalf("port collision Launch() error = %v, want safe launch error", err)
	}
	if !strings.Contains(err.Error(), "during readiness") && !strings.Contains(err.Error(), "during startup") {
		t.Fatalf("port collision phase = %q, want readiness/startup", err)
	}
	if process.terminateCalls.Load() != 0 {
		t.Fatalf("already-exited collision process terminate calls = %d, want zero", process.terminateCalls.Load())
	}
}

func TestManagedBrowserLauncherRejectsSymlinkedProfileAndPortOutsideLoopback(t *testing.T) {
	t.Run("symlinked profile", func(t *testing.T) {
		configDir := t.TempDir()
		profileTarget := t.TempDir()
		profile := filepath.Join(configDir, ManagedBrowserProfileDirName)
		if err := os.Symlink(profileTarget, profile); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		process := &managedLaunchTestProcess{}
		launcher := newManagedLaunchTestLauncher(t, process, nil, nil)
		launcher.options.ConfigDir = configDir
		_, err := launcher.Launch(context.Background())
		if err == nil || !strings.Contains(err.Error(), "during profile") {
			t.Fatalf("symlinked profile error = %v, want profile phase", err)
		}
		if process.startCalls.Load() != 0 {
			t.Fatal("symlinked profile started Chrome")
		}
	})

	t.Run("non-loopback reservation", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve test port: %v", err)
		}
		defer listener.Close()
		process := &managedLaunchTestProcess{}
		launcher := newManagedLaunchTestLauncher(t, process, nil, nil)
		launcher.options.PortAllocator = func() (net.Listener, error) {
			return &managedLaunchTestListener{Listener: listener, address: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: listener.Addr().(*net.TCPAddr).Port}}, nil
		}
		_, err = launcher.Launch(context.Background())
		if err == nil || !strings.Contains(err.Error(), "during port") {
			t.Fatalf("non-loopback reservation error = %v, want port phase", err)
		}
		if process.startCalls.Load() != 0 {
			t.Fatal("invalid port reservation started Chrome")
		}
	})
}

func newManagedLaunchTestLauncher(t *testing.T, process *managedLaunchTestProcess, acquirer ManagedChromeExecutableAcquirer, starter ManagedBrowserProcessStarter) *ManagedBrowserLauncher {
	t.Helper()
	return newManagedLaunchTestLauncherWithTransport(t, process, managedLaunchVersionTransport{}, acquirer, starter)
}

func newManagedLaunchTestLauncherWithTransport(t *testing.T, process *managedLaunchTestProcess, transport managedLaunchVersionTransport, acquirer ManagedChromeExecutableAcquirer, starter ManagedBrowserProcessStarter) *ManagedBrowserLauncher {
	t.Helper()
	if acquirer == nil {
		acquirer = ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
			return ChromeExecutable{Path: "/qualified/chrome", Version: "Google Chrome 152.0.1.2", Major: 152, Source: ExecutableSourceChromeForTesting}, nil
		})
	}
	if starter == nil {
		starter = func(path string, args []string) (ManagedBrowserProcess, error) {
			process.setArgs(path, args)
			return process, nil
		}
	}
	return NewManagedBrowserLauncher(ManagedBrowserLaunchOptions{
		ConfigDir:      t.TempDir(),
		Acquirer:       acquirer,
		HTTPClient:     &http.Client{Transport: transport},
		ProcessStarter: starter,
		StartupTimeout: time.Second,
		PollInterval:   time.Millisecond,
		DisplayAvailable: func() bool {
			return true
		},
	})
}

func assertManagedLaunchArguments(t *testing.T, args []string, profileDir string, headless bool, startupURL string) {
	t.Helper()
	if len(args) == 0 || args[len(args)-1] != startupURL {
		t.Fatalf("launch args = %v, want one final startup URL %q", args, startupURL)
	}
	if !headless && hasManagedLaunchArg(args, "--headless=") || !headless && hasManagedLaunchArg(args, "--headless") {
		t.Fatalf("headful launch args contain headless mode: %v", args)
	}
	if headless && !hasManagedLaunchArg(args, "--headless=new") {
		t.Fatalf("headless launch args omit --headless=new: %v", args)
	}
	for _, required := range []string{
		"--remote-debugging-address=127.0.0.1",
		"--disable-features=DelayMediaSinkDiscovery",
		"--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
		"--enable-blink-features=DeclarativeWebmcp",
		"--enable-experimental-web-platform-features",
		"--user-data-dir=" + profileDir,
	} {
		if !hasManagedLaunchArg(args, required) {
			t.Fatalf("launch args omit %q: %v", required, args)
		}
	}
	positionals := 0
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positionals++
		}
	}
	if positionals != 1 {
		t.Fatalf("launch args have %d positional startup values, want one: %v", positionals, args)
	}
}

func hasManagedLaunchArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func managedLaunchPortFromArgs(t *testing.T, args []string) int {
	t.Helper()
	const prefix = "--remote-debugging-port="
	for _, arg := range args {
		if !strings.HasPrefix(arg, prefix) {
			continue
		}
		port, err := strconv.Atoi(strings.TrimPrefix(arg, prefix))
		if err != nil || port < 1 || port > 65535 {
			t.Fatalf("invalid launch port argument %q", arg)
		}
		return port
	}
	t.Fatalf("launch args omit remote debugging port: %v", args)
	return 0
}

type managedLaunchVersionTransport struct {
	waitForRequest bool
	alwaysError    error
}

func (t managedLaunchVersionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Path != "/json/version" {
		return nil, errors.New("unexpected managed browser readiness request")
	}
	if t.waitForRequest {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	if t.alwaysError != nil {
		return nil, t.alwaysError
	}
	port := request.URL.Port()
	body := `{"Browser":"Google Chrome 152.0.1.2","Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://127.0.0.1:` + port + `/devtools/browser/fake?secret=redact#fragment"}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

type managedLaunchTestProcess struct {
	done chan struct{}

	initOnce sync.Once
	exitOnce sync.Once
	argsMu   sync.RWMutex
	path     string
	args     []string

	startCalls     atomic.Int32
	terminateCalls atomic.Int32
	killCalls      atomic.Int32
	waitErr        error
}

func (p *managedLaunchTestProcess) initialize() {
	p.initOnce.Do(func() { p.done = make(chan struct{}) })
}

func (p *managedLaunchTestProcess) setArgs(path string, args []string) {
	p.initialize()
	p.argsMu.Lock()
	p.path = path
	p.args = append([]string(nil), args...)
	p.argsMu.Unlock()
	p.startCalls.Add(1)
}

func (p *managedLaunchTestProcess) argsSnapshot() []string {
	p.argsMu.RLock()
	defer p.argsMu.RUnlock()
	return append([]string(nil), p.args...)
}

func (p *managedLaunchTestProcess) Wait() error {
	p.initialize()
	<-p.done
	return p.waitErr
}

func (p *managedLaunchTestProcess) Terminate() error {
	p.initialize()
	p.terminateCalls.Add(1)
	p.exit(nil)
	return nil
}

func (p *managedLaunchTestProcess) Kill() error {
	p.initialize()
	p.killCalls.Add(1)
	p.exit(nil)
	return nil
}

func (p *managedLaunchTestProcess) exit(err error) {
	p.initialize()
	p.waitErr = err
	p.exitOnce.Do(func() { close(p.done) })
}

type managedLaunchTestListener struct {
	net.Listener
	address net.Addr
}

func (l *managedLaunchTestListener) Addr() net.Addr { return l.address }
