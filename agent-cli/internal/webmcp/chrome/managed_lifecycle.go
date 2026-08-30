package chrome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// ManagedBrowserStateVersion is the version of the small process locator
	// written beside the managed profile. Unknown versions fail closed.
	ManagedBrowserStateVersion          uint = 1
	managedBrowserStateName                  = "managed-browser.json"
	managedBrowserLockName                   = ".managed-browser.lock"
	defaultManagedBrowserLockTimeout         = 5 * time.Second
	defaultManagedBrowserLockPoll            = 10 * time.Millisecond
	defaultManagedBrowserLockStaleAfter      = 30 * time.Second
	managedBrowserStateResponseTimeout       = 2 * time.Second
)

var (
	// ErrManagedBrowserState classifies malformed, stale, or unsafe lifecycle
	// state. It is intentionally separate from launch errors so callers can
	// observe that recovery was attempted without receiving unsafe details.
	ErrManagedBrowserState = errors.New("managed browser state is invalid")
	// ErrManagedBrowserLifecycle classifies state-lock and ownership failures.
	ErrManagedBrowserLifecycle = errors.New("managed browser lifecycle failed")
)

// ManagedBrowserState is the only persisted managed-process locator. It has
// no page URL, credentials, or arbitrary command output. The browser
// websocket is retained only as a loopback protocol identity and is checked
// again before reuse.
type ManagedBrowserState struct {
	Version           uint   `json:"version"`
	PID               int    `json:"pid"`
	ProcessIdentity   string `json:"process_identity"`
	ProfileDir        string `json:"profile_dir"`
	CDPURL            string `json:"cdp_url"`
	BrowserWSEndpoint string `json:"browser_ws_endpoint"`
	ExecutablePath    string `json:"executable_path,omitempty"`
}

// ManagedBrowserStatePath returns the default state location. An empty result
// means the config directory could not be resolved; callers that need the
// reason should use ManagedBrowserManager.Acquire.
func ManagedBrowserStatePath(configDir string) string {
	profileDir, err := managedBrowserProfileDir(configDir)
	if err != nil {
		return ""
	}
	return filepath.Join(profileDir, managedBrowserStateName)
}

// ManagedBrowserProcessInfo is the identity observed for one operating-system
// process. Identity is an incarnation marker, not a user-facing identifier.
type ManagedBrowserProcessInfo struct {
	PID        int
	Identity   string
	ProfileDir string
}

// ManagedBrowserProcessInspector validates and/or reads the exact process
// represented by a state record. It is injectable so lifecycle tests never
// need to scan the host process table.
type ManagedBrowserProcessInspector interface {
	Inspect(context.Context, ManagedBrowserState) (ManagedBrowserProcessInfo, error)
}

// ManagedBrowserProcessInspectorFunc adapts a function to the inspector seam.
type ManagedBrowserProcessInspectorFunc func(context.Context, ManagedBrowserState) (ManagedBrowserProcessInfo, error)

func (f ManagedBrowserProcessInspectorFunc) Inspect(ctx context.Context, state ManagedBrowserState) (ManagedBrowserProcessInfo, error) {
	if f == nil {
		return ManagedBrowserProcessInfo{}, errors.New("managed browser process inspector is nil")
	}
	return f(ctx, state)
}

// ManagedBrowserProcessReattacher returns a process handle for a browser that
// survived the prior session. The default implementation only signals the
// exact PID after its identity and command line have been validated.
type ManagedBrowserProcessReattacher func(context.Context, ManagedBrowserState) (ManagedBrowserProcess, error)

// ManagedBrowserManagerOptions configures the persistent managed-browser
// ownership boundary. LaunchOptions supplies the same seams as the launcher;
// manager-specific functions are used only for state validation and reuse.
type ManagedBrowserManagerOptions struct {
	ConfigDir     string
	StatePath     string
	LaunchOptions ManagedBrowserLaunchOptions

	ProcessInspector  ManagedBrowserProcessInspector
	ProcessReattacher ManagedBrowserProcessReattacher

	LockTimeout    time.Duration
	LockPoll       time.Duration
	LockStaleAfter time.Duration
}

// ManagedBrowserManager serializes launch/reuse for one agent config
// directory and installs lifecycle-aware close behavior on every returned
// browser.
type ManagedBrowserManager struct {
	options ManagedBrowserManagerOptions
}

// NewManagedBrowserManager constructs a side-effect-free lifecycle manager.
// Filesystem and process work begins in Acquire.
func NewManagedBrowserManager(options ManagedBrowserManagerOptions) *ManagedBrowserManager {
	if strings.TrimSpace(options.ConfigDir) == "" {
		options.ConfigDir = options.LaunchOptions.ConfigDir
	}
	if options.LockTimeout <= 0 {
		options.LockTimeout = defaultManagedBrowserLockTimeout
	}
	if options.LockPoll <= 0 {
		options.LockPoll = defaultManagedBrowserLockPoll
	}
	if options.LockStaleAfter <= 0 {
		options.LockStaleAfter = defaultManagedBrowserLockStaleAfter
	}
	if options.ProcessInspector == nil {
		options.ProcessInspector = defaultManagedBrowserProcessInspector{}
	}
	if options.ProcessReattacher == nil {
		options.ProcessReattacher = defaultManagedBrowserProcessReattacher(options.ProcessInspector)
	}
	return &ManagedBrowserManager{options: options}
}

// Acquire returns the existing exact managed browser when its state, process
// identity, profile, and loopback DevTools endpoint all still agree. Any
// invalid state is removed under the lifecycle lock and replaced by one fresh
// launch; an invalid record never causes an unrelated process to be stopped.
func (m *ManagedBrowserManager) Acquire(ctx context.Context, request ManagedBrowserLaunchOptions) (*ManagedBrowser, error) {
	if m == nil {
		return nil, newManagedBrowserLifecycleError("acquire", errors.New("manager is nil"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, newManagedBrowserLifecycleError("acquire", err)
	}

	launchOptions := m.launchOptions(request)
	profileDir, err := managedBrowserProfileDir(launchOptions.ConfigDir)
	if err != nil {
		return nil, newManagedBrowserLifecycleError("profile", err)
	}
	if err := prepareManagedBrowserProfile(profileDir); err != nil {
		return nil, newManagedBrowserLifecycleError("profile", err)
	}
	statePath, err := m.statePath(profileDir)
	if err != nil {
		return nil, newManagedBrowserLifecycleError("state", err)
	}
	lease, err := acquireManagedBrowserLease(ctx, filepath.Join(profileDir, managedBrowserLockName), m.options.LockTimeout, m.options.LockPoll, m.options.LockStaleAfter)
	if err != nil {
		return nil, newManagedBrowserLifecycleError("lock", err)
	}
	defer lease.release()

	state, present, readErr := readManagedBrowserState(statePath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		if !errors.Is(readErr, ErrManagedBrowserState) {
			return nil, newManagedBrowserLifecycleError("state", readErr)
		}
		if err := removeManagedBrowserState(statePath); err != nil {
			return nil, newManagedBrowserLifecycleError("state", err)
		}
		present = false
	}
	if present {
		browser, reusable := m.reuse(ctx, launchOptions, profileDir, state)
		if reusable {
			browser.closeHook = func() error {
				return m.closeManagedBrowser(browser, statePath, state)
			}
			go m.watchManagedBrowser(browser, statePath, state)
			return browser, nil
		}
		// State can be stale because the process exited between inspection and
		// endpoint validation. Removing only this exact state is safe; no
		// cleanup signal is sent to the stale PID.
		if err := removeManagedBrowserState(statePath); err != nil {
			return nil, newManagedBrowserLifecycleError("state", err)
		}
	}

	return m.launchFresh(ctx, launchOptions, profileDir, statePath)
}

// LaunchOrReuse is a descriptive alias for Acquire.
func (m *ManagedBrowserManager) LaunchOrReuse(ctx context.Context, request ManagedBrowserLaunchOptions) (*ManagedBrowser, error) {
	return m.Acquire(ctx, request)
}

func (m *ManagedBrowserManager) launchOptions(request ManagedBrowserLaunchOptions) ManagedBrowserLaunchOptions {
	options := m.options.LaunchOptions
	if strings.TrimSpace(options.ConfigDir) == "" {
		options.ConfigDir = m.options.ConfigDir
	}
	if strings.TrimSpace(request.ConfigDir) != "" {
		options.ConfigDir = request.ConfigDir
	}
	if strings.TrimSpace(request.StartupURL) != "" {
		options.StartupURL = request.StartupURL
	}
	// Headless is a value rather than a pointer in the existing public launch
	// contract. A true request always wins; false deliberately retains the
	// manager's display-aware default.
	if request.Headless {
		options.Headless = true
	}
	if request.DisplayAvailable != nil {
		options.DisplayAvailable = request.DisplayAvailable
	}
	if request.Acquirer != nil {
		options.Acquirer = request.Acquirer
	}
	if managedChromeAcquisitionOptionsProvided(request.Acquisition) {
		options.Acquisition = request.Acquisition
	}
	if request.HTTPClient != nil {
		options.HTTPClient = request.HTTPClient
	}
	if request.PortAllocator != nil {
		options.PortAllocator = request.PortAllocator
	}
	if request.ProcessStarter != nil {
		options.ProcessStarter = request.ProcessStarter
	}
	if request.StartupTimeout > 0 {
		options.StartupTimeout = request.StartupTimeout
	}
	if request.PollInterval > 0 {
		options.PollInterval = request.PollInterval
	}
	if request.ShutdownTimeout > 0 {
		options.ShutdownTimeout = request.ShutdownTimeout
	}
	return options
}

func managedChromeAcquisitionOptionsProvided(options ManagedChromeAcquisitionOptions) bool {
	return options.GOOS != "" || options.GOARCH != "" || options.RequiredMajor != 0 ||
		options.StockPaths != nil || options.VersionQuery != nil || options.ExecutableCheck != nil ||
		options.VersionTimeout != 0 || options.PinnedAcquirer != nil || options.LockPath != "" ||
		options.CacheDir != "" || options.HTTPClient != nil
}

func (m *ManagedBrowserManager) statePath(profileDir string) (string, error) {
	path := strings.TrimSpace(m.options.StatePath)
	if path == "" {
		return filepath.Join(profileDir, managedBrowserStateName), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil || strings.TrimSpace(abs) == "" {
		return "", errors.New("managed browser state path is invalid")
	}
	if filepath.Dir(abs) != filepath.Clean(profileDir) {
		return "", errors.New("managed browser state path must be beside the managed profile")
	}
	return abs, nil
}

func (m *ManagedBrowserManager) reuse(ctx context.Context, options ManagedBrowserLaunchOptions, profileDir string, state ManagedBrowserState) (*ManagedBrowser, bool) {
	if err := validateManagedBrowserState(state, profileDir); err != nil {
		return nil, false
	}
	info, err := m.options.ProcessInspector.Inspect(ctx, state)
	if err != nil || (info.PID != 0 && info.PID != state.PID) || strings.TrimSpace(info.Identity) == "" || info.Identity != state.ProcessIdentity {
		return nil, false
	}
	if info.ProfileDir != "" && filepath.Clean(info.ProfileDir) != filepath.Clean(profileDir) {
		return nil, false
	}
	endpoint, err := fetchManagedBrowserEndpoint(ctx, options.HTTPClient, state.CDPURL, state.BrowserWSEndpoint)
	if err != nil {
		return nil, false
	}
	process, err := m.options.ProcessReattacher(ctx, state)
	if err != nil || process == nil {
		return nil, false
	}
	browser := &ManagedBrowser{
		endpoint:   endpoint,
		executable: ChromeExecutable{Path: state.ExecutablePath},
		profileDir: profileDir,
		startupURL: "about:blank",
		process:    newManagedBrowserProcessState(process),
		shutdown:   normalizedManagedShutdown(options.ShutdownTimeout),
		pid:        state.PID,
	}
	return browser, true
}

func (m *ManagedBrowserManager) launchFresh(ctx context.Context, options ManagedBrowserLaunchOptions, profileDir, statePath string) (*ManagedBrowser, error) {
	browser, err := NewManagedBrowserLauncher(options).Launch(ctx)
	if err != nil {
		return nil, err
	}
	if browser.PID() <= 0 {
		_ = browser.Close()
		return nil, newManagedBrowserLifecycleError("state", errors.New("managed browser process has no stable identity"))
	}
	state := ManagedBrowserState{
		Version:           ManagedBrowserStateVersion,
		PID:               browser.PID(),
		ProfileDir:        profileDir,
		CDPURL:            browser.Endpoint().CDPURL,
		BrowserWSEndpoint: browser.Endpoint().BrowserWSEndpoint,
		ExecutablePath:    browser.Executable().Path,
	}
	info, err := m.options.ProcessInspector.Inspect(ctx, state)
	if err != nil || (info.PID != 0 && info.PID != state.PID) || strings.TrimSpace(info.Identity) == "" {
		_ = browser.Close()
		if err == nil {
			err = errors.New("managed browser process identity is unavailable")
		}
		return nil, newManagedBrowserLifecycleError("state", err)
	}
	if info.ProfileDir != "" && filepath.Clean(info.ProfileDir) != filepath.Clean(profileDir) {
		_ = browser.Close()
		return nil, newManagedBrowserLifecycleError("state", errors.New("managed browser process profile does not match"))
	}
	state.ProcessIdentity = info.Identity
	if err := writeManagedBrowserState(statePath, state); err != nil {
		_ = browser.Close()
		return nil, newManagedBrowserLifecycleError("state", err)
	}
	browser.closeHook = func() error {
		return m.closeManagedBrowser(browser, statePath, state)
	}
	go m.watchManagedBrowser(browser, statePath, state)
	return browser, nil
}

func (m *ManagedBrowserManager) closeManagedBrowser(browser *ManagedBrowser, statePath string, expected ManagedBrowserState) error {
	ctx := context.Background()
	profileDir := expected.ProfileDir
	lease, err := acquireManagedBrowserLease(ctx, filepath.Join(profileDir, managedBrowserLockName), m.options.LockTimeout, m.options.LockPoll, m.options.LockStaleAfter)
	if err != nil {
		return newManagedBrowserLifecycleError("close", err)
	}
	defer lease.release()
	current, present, readErr := readManagedBrowserState(statePath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		if errors.Is(readErr, ErrManagedBrowserState) {
			if removeErr := removeManagedBrowserState(statePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return newManagedBrowserLifecycleError("close", removeErr)
			}
			present = false
		} else {
			return newManagedBrowserLifecycleError("close", readErr)
		}
	}
	if present && !managedBrowserStatesMatch(current, expected) {
		// A replacement owns the profile now. Never signal it from an older
		// browser handle.
		return nil
	}
	var stopErr error
	if browser != nil && browser.process != nil {
		stopErr = browser.process.stop(normalizedManagedShutdown(browser.shutdown))
	}
	removeErr := removeManagedBrowserState(statePath)
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return errors.Join(stopErr, newManagedBrowserLifecycleError("close", removeErr))
	}
	return stopErr
}

func (m *ManagedBrowserManager) watchManagedBrowser(browser *ManagedBrowser, statePath string, expected ManagedBrowserState) {
	if browser == nil || browser.Done() == nil {
		return
	}
	<-browser.Done()
	lease, err := acquireManagedBrowserLease(context.Background(), filepath.Join(expected.ProfileDir, managedBrowserLockName), m.options.LockTimeout, m.options.LockPoll, m.options.LockStaleAfter)
	if err != nil {
		return
	}
	defer lease.release()
	current, present, err := readManagedBrowserState(statePath)
	if err != nil || !present || !managedBrowserStatesMatch(current, expected) {
		return
	}
	_ = removeManagedBrowserState(statePath)
}

func validateManagedBrowserState(state ManagedBrowserState, expectedProfile string) error {
	if state.Version != ManagedBrowserStateVersion || state.PID <= 0 || strings.TrimSpace(state.ProcessIdentity) == "" {
		return fmt.Errorf("%w: incomplete record", ErrManagedBrowserState)
	}
	if filepath.Clean(state.ProfileDir) != filepath.Clean(expectedProfile) || !filepath.IsAbs(state.ProfileDir) {
		return fmt.Errorf("%w: profile mismatch", ErrManagedBrowserState)
	}
	if info, err := os.Lstat(state.ProfileDir); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: profile unavailable", ErrManagedBrowserState)
	}
	port, err := managedBrowserCDPPort(state.CDPURL)
	if err != nil {
		return fmt.Errorf("%w: endpoint invalid", ErrManagedBrowserState)
	}
	if strings.TrimSpace(state.BrowserWSEndpoint) != "" {
		if _, err := normalizeManagedBrowserWebSocket(state.BrowserWSEndpoint, port); err != nil {
			return fmt.Errorf("%w: websocket invalid", ErrManagedBrowserState)
		}
	}
	return nil
}

func managedBrowserStatesMatch(left, right ManagedBrowserState) bool {
	return left.Version == right.Version &&
		left.PID == right.PID &&
		left.ProcessIdentity == right.ProcessIdentity &&
		filepath.Clean(left.ProfileDir) == filepath.Clean(right.ProfileDir) &&
		left.CDPURL == right.CDPURL
}

func managedBrowserCDPPort(raw string) (int, error) {
	parsed, err := urlParseManaged(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() == "" || !isManagedLoopbackHost(parsed.Hostname()) || parsed.Path != "/json/version" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return 0, errors.New("managed browser endpoint is not loopback DevTools")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("managed browser endpoint port is invalid")
	}
	return port, nil
}

func fetchManagedBrowserEndpoint(ctx context.Context, client *http.Client, rawCDPURL, expectedWS string) (ManagedBrowserEndpoint, error) {
	port, err := managedBrowserCDPPort(rawCDPURL)
	if err != nil {
		return ManagedBrowserEndpoint{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: managedBrowserRequestTimeout}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, managedBrowserStateResponseTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawCDPURL, nil)
	if err != nil {
		return ManagedBrowserEndpoint{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return ManagedBrowserEndpoint{}, err
	}
	if response == nil {
		return ManagedBrowserEndpoint{}, errors.New("DevTools version response is empty")
	}
	if response.Request != nil && response.Request.URL != nil && response.Request.URL.String() != request.URL.String() {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return ManagedBrowserEndpoint{}, errors.New("managed browser endpoint redirected")
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	endpoint, err := decodeManagedBrowserVersion(response, port)
	if err != nil {
		return ManagedBrowserEndpoint{}, err
	}
	if strings.TrimSpace(expectedWS) != "" && endpoint.BrowserWSEndpoint != expectedWS {
		return ManagedBrowserEndpoint{}, errors.New("managed browser websocket changed")
	}
	return endpoint, nil
}

func readManagedBrowserState(path string) (ManagedBrowserState, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ManagedBrowserState{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ManagedBrowserState{}, true, fmt.Errorf("%w: state file is not regular", ErrManagedBrowserState)
	}
	file, err := os.Open(path)
	if err != nil {
		return ManagedBrowserState{}, false, err
	}
	defer file.Close()
	var state ManagedBrowserState
	decoder := json.NewDecoder(io.LimitReader(file, managedBrowserStateResponseLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return ManagedBrowserState{}, true, fmt.Errorf("%w: malformed record", ErrManagedBrowserState)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ManagedBrowserState{}, true, fmt.Errorf("%w: malformed record", ErrManagedBrowserState)
	}
	return state, true, nil
}

func writeManagedBrowserState(path string, state ManagedBrowserState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".managed-browser-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func removeManagedBrowserState(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return os.Remove(path)
}

type managedBrowserLease struct{ path string }

func acquireManagedBrowserLease(ctx context.Context, path string, timeout, poll, staleAfter time.Duration) (*managedBrowserLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = defaultManagedBrowserLockTimeout
	}
	if poll <= 0 {
		poll = defaultManagedBrowserLockPoll
	}
	if staleAfter <= 0 {
		staleAfter = defaultManagedBrowserLockStaleAfter
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = io.WriteString(file, strconv.Itoa(os.Getpid()))
			_ = file.Close()
			return &managedBrowserLease{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if managedBrowserLockStale(path, staleAfter) {
			if removeErr := os.Remove(path); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return nil, errors.New("managed browser lifecycle lock timed out")
		case <-timer.C:
		}
	}
}

func (l *managedBrowserLease) release() {
	if l == nil || strings.TrimSpace(l.path) == "" {
		return
	}
	_ = os.Remove(l.path)
}

func managedBrowserLockStale(path string, staleAfter time.Duration) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	data, _ := os.ReadFile(path)
	pid, pidErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if pidErr == nil && pid > 0 && managedProcessAlive(pid) {
		return false
	}
	return staleAfter <= 0 || time.Since(info.ModTime()) >= staleAfter
}

type defaultManagedBrowserProcessInspector struct{}

func (defaultManagedBrowserProcessInspector) Inspect(ctx context.Context, state ManagedBrowserState) (ManagedBrowserProcessInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ManagedBrowserProcessInfo{}, err
	}
	if state.PID <= 0 || !managedProcessAlive(state.PID) {
		return ManagedBrowserProcessInfo{}, errors.New("managed browser process is not alive")
	}
	commandLine, err := managedProcessCommandLine(ctx, state.PID)
	if err != nil {
		return ManagedBrowserProcessInfo{}, err
	}
	if !managedCommandLineMatches(commandLine, state) {
		return ManagedBrowserProcessInfo{}, errors.New("managed browser process command line does not match")
	}
	identity, err := managedProcessIdentity(ctx, state.PID, commandLine)
	if err != nil {
		return ManagedBrowserProcessInfo{}, err
	}
	return ManagedBrowserProcessInfo{PID: state.PID, Identity: identity, ProfileDir: state.ProfileDir}, nil
}

func managedCommandLineMatches(commandLine []string, state ManagedBrowserState) bool {
	if len(commandLine) == 0 {
		return false
	}
	profile := filepath.Clean(state.ProfileDir)
	hasProfile := false
	hasLoopback := false
	hasPort := false
	port, _ := managedBrowserCDPPort(state.CDPURL)
	for index, argument := range commandLine {
		if argument == "--user-data-dir" && index+1 < len(commandLine) {
			hasProfile = filepath.Clean(commandLine[index+1]) == profile
		}
		if strings.HasPrefix(argument, "--user-data-dir=") {
			hasProfile = filepath.Clean(strings.TrimPrefix(argument, "--user-data-dir=")) == profile
		}
		if argument == "--remote-debugging-address=127.0.0.1" {
			hasLoopback = true
		}
		if argument == "--remote-debugging-port="+strconv.Itoa(port) {
			hasPort = true
		}
	}
	return hasProfile && hasLoopback && hasPort
}

func managedProcessCommandLine(ctx context.Context, pid int) ([]string, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil {
			return nil, err
		}
		parts := strings.Split(string(data), "\x00")
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			if part != "" {
				result = append(result, part)
			}
		}
		return result, nil
	}
	command := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=")
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(strings.TrimSpace(string(output))), nil
}

func managedProcessIdentity(ctx context.Context, pid int, commandLine []string) (string, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return "", err
		}
		closeParen := strings.LastIndex(string(data), ")")
		if closeParen < 0 || closeParen+2 >= len(data) {
			return "", errors.New("managed browser process identity is unavailable")
		}
		fields := strings.Fields(string(data)[closeParen+2:])
		// The slice starts at /proc field 3, so field 22 (starttime) is index 19.
		if len(fields) <= 19 || strings.TrimSpace(fields[19]) == "" {
			return "", errors.New("managed browser process identity is unavailable")
		}
		return "linux-start-" + fields[19], nil
	}
	if runtime.GOOS != "windows" {
		command := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
		output, err := command.Output()
		if err == nil && strings.TrimSpace(string(output)) != "" {
			digest := sha256.Sum256([]byte(strings.TrimSpace(string(output))))
			return "ps-start-" + hex.EncodeToString(digest[:12]), nil
		}
	}
	// Windows does not provide a portable standard-library creation-time API.
	// Including the command-line digest still prevents a different process
	// with the same PID but different launch arguments from being reused.
	digest := sha256.Sum256([]byte(strings.Join(commandLine, "\x00")))
	return "command-" + hex.EncodeToString(digest[:12]), nil
}

func managedProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil || process == nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func defaultManagedBrowserProcessReattacher(inspector ManagedBrowserProcessInspector) ManagedBrowserProcessReattacher {
	if inspector == nil {
		inspector = defaultManagedBrowserProcessInspector{}
	}
	return func(ctx context.Context, state ManagedBrowserState) (ManagedBrowserProcess, error) {
		if _, err := inspector.Inspect(ctx, state); err != nil {
			return nil, err
		}
		return &reattachedManagedBrowserProcess{state: state, inspector: inspector}, nil
	}
}

type reattachedManagedBrowserProcess struct {
	state     ManagedBrowserState
	inspector ManagedBrowserProcessInspector
}

func (p *reattachedManagedBrowserProcess) Wait() error {
	if p == nil {
		return errors.New("managed browser process is unavailable")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := p.inspector.Inspect(context.Background(), p.state); err != nil {
			return nil
		}
		<-ticker.C
	}
}

func (p *reattachedManagedBrowserProcess) Terminate() error {
	return signalManagedBrowserPID(p.pid(), false)
}

func (p *reattachedManagedBrowserProcess) Kill() error {
	return signalManagedBrowserPID(p.pid(), true)
}

func (p *reattachedManagedBrowserProcess) PID() int {
	if p == nil {
		return 0
	}
	return p.pid()
}

func (p *reattachedManagedBrowserProcess) pid() int {
	if p == nil {
		return 0
	}
	return p.state.PID
}

func signalManagedBrowserPID(pid int, kill bool) error {
	process, err := os.FindProcess(pid)
	if err != nil || process == nil {
		return os.ErrProcessDone
	}
	if kill || runtime.GOOS == "windows" {
		return process.Kill()
	}
	return process.Signal(syscall.SIGTERM)
}

func normalizedManagedShutdown(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultManagedBrowserShutdownTimeout
	}
	return timeout
}

func newManagedBrowserLifecycleError(phase string, cause error) error {
	return &ManagedBrowserLifecycleError{Phase: phase, Cause: cause}
}

// ManagedBrowserLifecycleError is safe to render to operators. Its cause is
// retained for errors.Is/errors.As but paths, URLs, and process output never
// appear in Error.
type ManagedBrowserLifecycleError struct {
	Phase string
	Cause error
}

func (e *ManagedBrowserLifecycleError) Error() string {
	if e == nil {
		return ErrManagedBrowserLifecycle.Error()
	}
	phase := safeManagedBrowserLabel(e.Phase, "lifecycle")
	return fmt.Sprintf("managed WebMCP browser lifecycle failed during %s; retry the managed browser operation", phase)
}

func (e *ManagedBrowserLifecycleError) Unwrap() error {
	if e == nil {
		return ErrManagedBrowserLifecycle
	}
	return errors.Join(ErrManagedBrowserLifecycle, e.Cause)
}

const managedBrowserStateResponseLimit = 64 << 10

// urlParseManaged keeps URL parsing in this file's narrow lifecycle seam and
// avoids exposing any state URL in diagnostic errors.
func urlParseManaged(raw string) (*url.URL, error) {
	return url.Parse(strings.TrimSpace(raw))
}
