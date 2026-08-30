package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// ManagedBrowserProfileDirName is the only profile directory used by the
	// managed browser. It is deliberately separate from any user Chrome
	// profile and is stable across sessions.
	ManagedBrowserProfileDirName = "browser-profile"

	defaultManagedBrowserStartupTimeout  = 30 * time.Second
	defaultManagedBrowserShutdownTimeout = 5 * time.Second
	defaultManagedBrowserPollInterval    = 100 * time.Millisecond
	managedBrowserVersionResponseLimit   = 64 << 10
	managedBrowserRequestTimeout         = 2 * time.Second
)

var (
	// ErrManagedBrowserLaunch is the stable classification for all failures
	// while preparing, starting, or waiting for an agent-managed browser.
	ErrManagedBrowserLaunch = errors.New("managed browser launch failed")

	managedBrowserURLPattern = map[string]struct{}{
		"http":  {},
		"https": {},
	}
)

// ManagedChromeExecutableAcquirer is the executable-selection seam used by
// the managed launcher. ManagedChromeAcquirer satisfies it, while tests can
// inject a deterministic qualified executable without downloading Chrome.
type ManagedChromeExecutableAcquirer interface {
	Acquire(context.Context) (ChromeExecutable, error)
}

// ManagedChromeExecutableAcquirerFunc adapts a function to the executable
// selection seam.
type ManagedChromeExecutableAcquirerFunc func(context.Context) (ChromeExecutable, error)

// Acquire implements ManagedChromeExecutableAcquirer.
func (f ManagedChromeExecutableAcquirerFunc) Acquire(ctx context.Context) (ChromeExecutable, error) {
	if f == nil {
		return ChromeExecutable{}, errors.New("managed Chrome executable acquirer is nil")
	}
	return f(ctx)
}

// ManagedBrowserPortAllocator reserves a loopback TCP port for one launch.
// The launcher closes the reservation immediately before starting Chrome;
// the reservation keeps port choice and process startup in one injectable
// boundary without requiring Chrome to inherit a file descriptor.
type ManagedBrowserPortAllocator func() (net.Listener, error)

// ManagedBrowserProcess is the small ownership contract required by the
// launcher. Wait is called exactly once by the launcher after Start returns.
type ManagedBrowserProcess interface {
	Wait() error
	Terminate() error
	Kill() error
}

// ManagedBrowserProcessStarter starts one executable with its already
// validated argument vector. It must not attach a context to the process:
// once launch succeeds, the browser intentionally outlives the session.
type ManagedBrowserProcessStarter func(string, []string) (ManagedBrowserProcess, error)

// ManagedBrowserLaunchOptions configures one managed browser acquisition and
// launch. Zero-valued optional functions select production behavior.
type ManagedBrowserLaunchOptions struct {
	// ConfigDir is the same directory used by the CLI config loader. The
	// managed profile is always ConfigDir/browser-profile.
	ConfigDir string
	// StartupURL is one validated customer-visible page. Empty means
	// about:blank.
	StartupURL string
	// Headless is an explicit request. When false, DisplayAvailable decides
	// whether the resolved process is headful or headless.
	Headless bool

	DisplayAvailable func() bool
	Acquirer         ManagedChromeExecutableAcquirer
	Acquisition      ManagedChromeAcquisitionOptions
	HTTPClient       *http.Client
	PortAllocator    ManagedBrowserPortAllocator
	ProcessStarter   ManagedBrowserProcessStarter

	StartupTimeout  time.Duration
	PollInterval    time.Duration
	ShutdownTimeout time.Duration
}

// ManagedBrowserEndpoint is the responsive loopback DevTools endpoint
// returned only after the launched process has published /json/version.
// Endpoint values are transport data for the existing discovery boundary;
// user-facing errors and diagnostics never render them.
type ManagedBrowserEndpoint struct {
	CDPURL            string
	BrowserWSEndpoint string
	Browser           string
	ProtocolVersion   string
}

// ManagedBrowserLauncher starts one agent-owned Chrome process.
type ManagedBrowserLauncher struct {
	options ManagedBrowserLaunchOptions
}

// NewManagedBrowserLauncher constructs a launcher with safe production
// defaults. Construction is side-effect free; acquisition and process start
// happen only in Launch.
func NewManagedBrowserLauncher(options ManagedBrowserLaunchOptions) *ManagedBrowserLauncher {
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = defaultManagedBrowserStartupTimeout
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultManagedBrowserPollInterval
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultManagedBrowserShutdownTimeout
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: managedBrowserRequestTimeout}
	}
	if options.DisplayAvailable == nil {
		options.DisplayAvailable = defaultManagedBrowserDisplayAvailable
	}
	if options.PortAllocator == nil {
		options.PortAllocator = reserveManagedLoopbackPort
	}
	if options.ProcessStarter == nil {
		options.ProcessStarter = startManagedBrowserProcess
	}
	if options.Acquirer == nil {
		options.Acquirer = NewManagedChromeAcquirer(options.Acquisition)
	}
	return &ManagedBrowserLauncher{options: options}
}

// LaunchManagedBrowser is the function-form entry point for callers that do
// not need to retain a launcher instance.
func LaunchManagedBrowser(ctx context.Context, options ManagedBrowserLaunchOptions) (*ManagedBrowser, error) {
	return NewManagedBrowserLauncher(options).Launch(ctx)
}

// Launch prepares the private profile, acquires a qualified executable,
// starts Chrome, and waits for a responsive loopback DevTools version
// response. A failed attempt terminates only the process it started.
func (l *ManagedBrowserLauncher) Launch(ctx context.Context) (*ManagedBrowser, error) {
	if l == nil {
		return nil, newManagedBrowserLaunchError("launcher", "unknown", nil, errors.New("launcher is nil"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, newManagedBrowserLaunchError("startup", "unknown", nil, err)
	}

	startupURL, err := normalizeManagedStartupURL(l.options.StartupURL)
	if err != nil {
		return nil, newManagedBrowserLaunchError("configuration", "unknown", nil, err)
	}
	profileDir, err := managedBrowserProfileDir(l.options.ConfigDir)
	if err != nil {
		return nil, newManagedBrowserLaunchError("profile", "unknown", nil, err)
	}
	if err := prepareManagedBrowserProfile(profileDir); err != nil {
		return nil, newManagedBrowserLaunchError("profile", "unknown", nil, err)
	}

	headless := l.options.Headless
	if !headless && !l.options.DisplayAvailable() {
		headless = true
	}
	mode := managedBrowserMode(headless)

	executable, err := l.options.Acquirer.Acquire(ctx)
	if err != nil {
		return nil, newManagedBrowserLaunchError("acquisition", mode, nil, err)
	}
	if strings.TrimSpace(executable.Path) == "" {
		return nil, newManagedBrowserLaunchError("acquisition", mode, nil, errors.New("qualified Chrome executable path is empty"))
	}

	listener, err := l.options.PortAllocator()
	if err != nil {
		return nil, newManagedBrowserLaunchError("port", mode, nil, err)
	}
	if listener == nil {
		return nil, newManagedBrowserLaunchError("port", mode, nil, errors.New("loopback port allocator returned no listener"))
	}
	port, err := managedLoopbackPort(listener)
	if err != nil {
		_ = listener.Close()
		return nil, newManagedBrowserLaunchError("port", mode, nil, err)
	}
	if err := listener.Close(); err != nil {
		return nil, newManagedBrowserLaunchError("port", mode, nil, err)
	}

	args := managedBrowserArgs(profileDir, port, startupURL, headless)
	process, err := l.options.ProcessStarter(executable.Path, args)
	if err != nil {
		return nil, newManagedBrowserLaunchError("start", mode, nil, err)
	}
	if process == nil {
		return nil, newManagedBrowserLaunchError("start", mode, nil, errors.New("process starter returned no process"))
	}

	state := newManagedBrowserProcessState(process)
	endpoint, readinessErr := waitForManagedBrowser(ctx, l.options.HTTPClient, port, state.done, l.options.StartupTimeout, l.options.PollInterval)
	if readinessErr != nil {
		cleanupErr := state.stop(l.options.ShutdownTimeout)
		if cleanupErr != nil {
			readinessErr = errors.Join(readinessErr, cleanupErr)
		}
		phase := "readiness"
		if errors.Is(readinessErr, context.Canceled) || errors.Is(readinessErr, context.DeadlineExceeded) {
			phase = "startup"
		}
		return nil, newManagedBrowserLaunchError(phase, mode, state.waitError(), readinessErr)
	}

	browser := &ManagedBrowser{
		endpoint:   endpoint,
		executable: executable,
		profileDir: profileDir,
		startupURL: startupURL,
		headless:   headless,
		process:    state,
		shutdown:   l.options.ShutdownTimeout,
	}
	if identity, ok := process.(interface{ PID() int }); ok {
		browser.pid = identity.PID()
	}
	return browser, nil
}

// ManagedBrowser is the successful result of Launch. Its process remains
// alive until Close is called or an external event terminates it; session
// detach must not call Close for the default keep-alive policy.
type ManagedBrowser struct {
	endpoint   ManagedBrowserEndpoint
	executable ChromeExecutable
	profileDir string
	startupURL string
	headless   bool
	pid        int
	process    *managedBrowserProcessState
	shutdown   time.Duration
	// closeHook is installed by ManagedBrowserManager. The launcher itself
	// owns only the process; the manager also owns the persisted identity
	// record and must serialize close against a later session's reuse.
	closeHook func() error

	closeOnce sync.Once
	closeErr  error
}

// Endpoint returns the responsive endpoint for the existing discovery seam.
func (b *ManagedBrowser) Endpoint() ManagedBrowserEndpoint {
	if b == nil {
		return ManagedBrowserEndpoint{}
	}
	return b.endpoint
}

// Executable returns the qualified executable metadata used for this launch.
func (b *ManagedBrowser) Executable() ChromeExecutable {
	if b == nil {
		return ChromeExecutable{}
	}
	return b.executable
}

// ProfileDir returns the agent-owned persistent profile path.
func (b *ManagedBrowser) ProfileDir() string {
	if b == nil {
		return ""
	}
	return b.profileDir
}

// StartupURL returns the one page supplied to Chrome.
func (b *ManagedBrowser) StartupURL() string {
	if b == nil {
		return ""
	}
	return b.startupURL
}

// Headless reports the resolved presentation mode, including display-aware
// fallback when headless was not explicitly requested.
func (b *ManagedBrowser) Headless() bool {
	return b != nil && b.headless
}

// PID returns the operating-system process ID when the production process
// starter supplied one. A fake process may legitimately return zero.
func (b *ManagedBrowser) PID() int {
	if b == nil {
		return 0
	}
	return b.pid
}

// Done closes when the managed process exits. It is useful to a later
// lifecycle owner that needs to distinguish a healthy detached process from
// an unexpectedly dead one.
func (b *ManagedBrowser) Done() <-chan struct{} {
	if b == nil || b.process == nil {
		return nil
	}
	return b.process.done
}

// WaitError returns the process exit error after Done has closed.
func (b *ManagedBrowser) WaitError() error {
	if b == nil || b.process == nil {
		return nil
	}
	return b.process.waitError()
}

// Close terminates this exact agent-started process once. It is intentionally
// separate from session detach so the default browser remains warm.
func (b *ManagedBrowser) Close() error {
	if b == nil || b.process == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		if b.closeHook != nil {
			b.closeErr = b.closeHook()
			return
		}
		b.closeErr = b.process.stop(b.shutdown)
	})
	return b.closeErr
}

type managedBrowserProcessState struct {
	process ManagedBrowserProcess
	done    chan struct{}

	mu      sync.RWMutex
	waitErr error

	stopOnce sync.Once
	stopErr  error
}

func newManagedBrowserProcessState(process ManagedBrowserProcess) *managedBrowserProcessState {
	state := &managedBrowserProcessState{process: process, done: make(chan struct{})}
	go func() {
		err := process.Wait()
		state.mu.Lock()
		state.waitErr = err
		state.mu.Unlock()
		close(state.done)
	}()
	return state
}

func (s *managedBrowserProcessState) waitError() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.waitErr
}

func (s *managedBrowserProcessState) stop(timeout time.Duration) error {
	if s == nil || s.process == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		s.stopErr = stopManagedBrowserProcess(s.process, s.done, timeout)
	})
	return s.stopErr
}

func stopManagedBrowserProcess(process ManagedBrowserProcess, done <-chan struct{}, timeout time.Duration) error {
	if process == nil {
		return nil
	}
	if managedBrowserDone(done) {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultManagedBrowserShutdownTimeout
	}

	terminateResult := make(chan error, 1)
	go func() { terminateResult <- process.Terminate() }()
	terminationTimer := time.NewTimer(timeout)
	defer terminationTimer.Stop()
	select {
	case err := <-terminateResult:
		if err != nil && !managedBrowserProcessDoneError(err) {
			// Give a process that rejected termination a bounded opportunity to
			// exit before escalating. The original error remains the useful
			// cleanup classification if Kill also fails.
			if managedBrowserDoneWithin(done, timeout) {
				return nil
			}
			killErr := killManagedBrowserProcess(process, done, timeout)
			if killErr != nil {
				return errors.Join(err, killErr)
			}
			return err
		}
		if managedBrowserDoneWithin(done, timeout) {
			return nil
		}
		return killManagedBrowserProcess(process, done, timeout)
	case <-terminationTimer.C:
		return killManagedBrowserProcess(process, done, timeout)
	}
}

func killManagedBrowserProcess(process ManagedBrowserProcess, done <-chan struct{}, timeout time.Duration) error {
	killResult := make(chan error, 1)
	go func() { killResult <- process.Kill() }()
	if timeout <= 0 {
		timeout = defaultManagedBrowserShutdownTimeout
	}
	waitTimer := time.NewTimer(timeout)
	defer waitTimer.Stop()
	select {
	case err := <-killResult:
		if err != nil && !managedBrowserProcessDoneError(err) {
			return err
		}
		if managedBrowserDoneWithin(done, timeout) {
			return nil
		}
		return errors.New("managed browser did not exit after termination")
	case <-waitTimer.C:
		return errors.New("managed browser did not exit after termination")
	}
}

func managedBrowserDone(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func managedBrowserDoneWithin(done <-chan struct{}, timeout time.Duration) bool {
	if managedBrowserDone(done) {
		return true
	}
	if timeout <= 0 {
		timeout = defaultManagedBrowserShutdownTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func managedBrowserProcessDoneError(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ECHILD)
}

func waitForManagedBrowser(ctx context.Context, client *http.Client, port int, processDone <-chan struct{}, timeout, poll time.Duration) (ManagedBrowserEndpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = http.DefaultClient
	}
	if timeout <= 0 {
		timeout = defaultManagedBrowserStartupTimeout
	}
	if poll <= 0 {
		poll = defaultManagedBrowserPollInterval
	}
	startupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	versionURL := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	var lastErr error
	for {
		if err := startupCtx.Err(); err != nil {
			if lastErr != nil {
				return ManagedBrowserEndpoint{}, errors.Join(err, lastErr)
			}
			return ManagedBrowserEndpoint{}, err
		}
		if managedBrowserDone(processDone) {
			if lastErr == nil {
				lastErr = errors.New("managed Chrome exited before DevTools became ready")
			}
			return ManagedBrowserEndpoint{}, lastErr
		}

		requestCtx, requestCancel := context.WithTimeout(startupCtx, managedBrowserRequestTimeout)
		request, requestErr := http.NewRequestWithContext(requestCtx, http.MethodGet, versionURL, nil)
		if requestErr == nil {
			response, doErr := client.Do(request)
			if doErr == nil {
				endpoint, decodeErr := decodeManagedBrowserVersion(response, port)
				if response.Body != nil {
					_ = response.Body.Close()
				}
				requestCancel()
				if decodeErr == nil {
					if managedBrowserDone(processDone) {
						return ManagedBrowserEndpoint{}, errors.New("managed Chrome exited before DevTools became ready")
					}
					return endpoint, nil
				}
				lastErr = decodeErr
			} else {
				lastErr = doErr
			}
		} else {
			lastErr = requestErr
		}
		requestCancel()

		timer := time.NewTimer(poll)
		select {
		case <-startupCtx.Done():
			if lastErr != nil {
				return ManagedBrowserEndpoint{}, errors.Join(startupCtx.Err(), lastErr)
			}
			return ManagedBrowserEndpoint{}, startupCtx.Err()
		case <-processDone:
			if lastErr == nil {
				lastErr = errors.New("managed Chrome exited before DevTools became ready")
			}
			timer.Stop()
			return ManagedBrowserEndpoint{}, lastErr
		case <-timer.C:
		}
	}
}

func decodeManagedBrowserVersion(response *http.Response, port int) (ManagedBrowserEndpoint, error) {
	if response == nil {
		return ManagedBrowserEndpoint{}, errors.New("DevTools version response is empty")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ManagedBrowserEndpoint{}, fmt.Errorf("DevTools version endpoint returned HTTP status %d", response.StatusCode)
	}
	if response.Body == nil {
		return ManagedBrowserEndpoint{}, errors.New("DevTools version response has no body")
	}
	var version struct {
		Browser              string `json:"Browser"`
		ProtocolVersion      string `json:"Protocol-Version"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, managedBrowserVersionResponseLimit))
	if err := decoder.Decode(&version); err != nil {
		return ManagedBrowserEndpoint{}, errors.New("DevTools version response is invalid")
	}
	websocket, err := normalizeManagedBrowserWebSocket(version.WebSocketDebuggerURL, port)
	if err != nil {
		return ManagedBrowserEndpoint{}, err
	}
	return ManagedBrowserEndpoint{
		CDPURL:            fmt.Sprintf("http://127.0.0.1:%d/json/version", port),
		BrowserWSEndpoint: websocket,
		Browser:           strings.TrimSpace(version.Browser),
		ProtocolVersion:   strings.TrimSpace(version.ProtocolVersion),
	}, nil
}

func normalizeManagedBrowserWebSocket(raw string, port int) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.User != nil || parsed.Hostname() == "" {
		return "", errors.New("DevTools version response has no valid browser websocket")
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", errors.New("DevTools version response has no valid browser websocket")
	}
	if !isManagedLoopbackHost(parsed.Hostname()) || parsed.Port() != strconv.Itoa(port) || !strings.HasPrefix(parsed.Path, "/devtools/browser/") {
		return "", errors.New("DevTools version response is not a loopback browser websocket")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func managedBrowserProfileDir(configDir string) (string, error) {
	resolved := strings.TrimSpace(configDir)
	if resolved == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", errors.New("agent config directory could not be resolved")
		}
		resolved = filepath.Join(home, ".agent-cli")
	}
	abs, err := filepath.Abs(resolved)
	if err != nil || strings.TrimSpace(abs) == "" {
		return "", errors.New("agent config directory is invalid")
	}
	return filepath.Join(abs, ManagedBrowserProfileDirName), nil
}

func prepareManagedBrowserProfile(profileDir string) error {
	if strings.TrimSpace(profileDir) == "" {
		return errors.New("managed browser profile directory is empty")
	}
	if info, err := os.Lstat(profileDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed browser profile directory must not be a symlink")
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(profileDir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("managed browser profile directory is not a private directory")
	}
	if err := os.Chmod(profileDir, 0o700); err != nil {
		return err
	}
	directory, err := os.Open(profileDir)
	if err != nil {
		return err
	}
	if err := directory.Close(); err != nil {
		return err
	}
	probe, err := os.CreateTemp(profileDir, ".agent-browser-profile-check-*")
	if err != nil {
		return err
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return err
	}
	if err := os.Remove(probePath); err != nil {
		return err
	}
	return nil
}

func normalizeManagedStartupURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "about:blank", nil
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("startup URL contains a control character")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.User != nil {
		return "", errors.New("startup URL must be an absolute URL without credentials")
	}
	if _, supported := managedBrowserURLPattern[strings.ToLower(parsed.Scheme)]; supported && parsed.Hostname() == "" {
		return "", errors.New("HTTP startup URLs require a host")
	}
	return value, nil
}

func managedBrowserArgs(profileDir string, port int, startupURL string, headless bool) []string {
	args := []string{
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-extensions",
		"--disable-sync",
		"--no-default-browser-check",
		"--no-first-run",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
		"--enable-blink-features=DeclarativeWebmcp",
		"--enable-experimental-web-platform-features",
		"--user-data-dir=" + profileDir,
	}
	if headless {
		args = append([]string{"--headless=new", "--disable-gpu"}, args...)
	}
	return append(args, startupURL)
}

func reserveManagedLoopbackPort() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func managedLoopbackPort(listener net.Listener) (int, error) {
	if listener == nil || listener.Addr() == nil {
		return 0, errors.New("loopback port reservation has no address")
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress == nil || tcpAddress.Port < 1 || tcpAddress.Port > 65535 || tcpAddress.IP == nil || !tcpAddress.IP.IsLoopback() {
		return 0, errors.New("loopback port reservation is invalid")
	}
	return tcpAddress.Port, nil
}

func defaultManagedBrowserDisplayAvailable() bool {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	case "linux":
		return strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
	default:
		return false
	}
}

func managedBrowserMode(headless bool) string {
	if headless {
		return "headless"
	}
	return "headful"
}

func isManagedLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func newManagedBrowserLaunchError(phase, mode string, processExit, cause error) error {
	if phase == "" {
		phase = "startup"
	}
	if mode == "" {
		mode = "unknown"
	}
	details := errors.Join(cause, processExit)
	return &ManagedBrowserLaunchError{Phase: phase, Mode: mode, Cause: details}
}

// ManagedBrowserLaunchError is the single safe operator-facing launch error.
// Phase and Mode are bounded labels; Cause is retained for errors.Is and
// diagnostics but is never interpolated into Error, preventing profile paths,
// URLs, command output, and nested process details from leaking.
type ManagedBrowserLaunchError struct {
	Phase string
	Mode  string
	Cause error
}

func (e *ManagedBrowserLaunchError) Error() string {
	if e == nil {
		return ErrManagedBrowserLaunch.Error()
	}
	phase := safeManagedBrowserLabel(e.Phase, "startup")
	mode := safeManagedBrowserLabel(e.Mode, "unknown")
	remediation := "check the Chrome prerequisite, writable agent config directory, and loopback DevTools availability, or supply an explicit browser endpoint"
	switch phase {
	case "configuration":
		remediation = "fix the managed browser startup URL and retry"
	case "profile":
		remediation = "make the agent config directory writable and retry"
	case "acquisition":
		remediation = fmt.Sprintf("install Chrome %d or newer, or supply an explicit browser endpoint", MinimumManagedChromeMajor)
	case "port":
		remediation = "retry so the agent can reserve a free loopback DevTools port"
	case "start":
		remediation = "check that the qualified Chrome executable can start with an agent-owned profile"
	case "readiness", "startup":
		remediation = "check that Chrome can publish a loopback DevTools endpoint and retry"
	}
	return fmt.Sprintf("managed WebMCP browser launch failed during %s in %s mode; %s", phase, mode, remediation)
}

func (e *ManagedBrowserLaunchError) Unwrap() error {
	if e == nil {
		return ErrManagedBrowserLaunch
	}
	return errors.Join(ErrManagedBrowserLaunch, e.Cause)
}

func safeManagedBrowserLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 32 {
		return fallback
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return fallback
	}
	return value
}

type osManagedBrowserProcess struct {
	command *exec.Cmd
}

func startManagedBrowserProcess(executable string, args []string) (ManagedBrowserProcess, error) {
	command := exec.Command(executable, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &osManagedBrowserProcess{command: command}, nil
}

func (p *osManagedBrowserProcess) Wait() error {
	if p == nil || p.command == nil {
		return errors.New("managed browser process is unavailable")
	}
	return p.command.Wait()
}

func (p *osManagedBrowserProcess) Terminate() error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		return p.command.Process.Kill()
	}
	return p.command.Process.Signal(syscall.SIGTERM)
}

func (p *osManagedBrowserProcess) Kill() error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	return p.command.Process.Kill()
}

func (p *osManagedBrowserProcess) PID() int {
	if p == nil || p.command == nil || p.command.Process == nil {
		return 0
	}
	return p.command.Process.Pid
}
