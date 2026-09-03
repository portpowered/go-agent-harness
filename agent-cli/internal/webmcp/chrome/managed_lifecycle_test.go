package chrome

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedBrowserManagerReusesStateAndClosesOnlyOnExplicitClose(t *testing.T) {
	configDir := t.TempDir()
	control := &managedLifecycleTestControl{}
	var starts atomic.Int32
	manager := newManagedLifecycleTestManager(t, configDir, control, &starts, "incarnation-a")

	first, err := manager.Acquire(context.Background(), ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("first Acquire(): %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("launch count after first acquire = %d, want 1", starts.Load())
	}
	statePath := ManagedBrowserStatePath(configDir)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("managed state was not published: %v", err)
	}

	second, err := manager.Acquire(context.Background(), ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("second Acquire(): %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("launch count after reuse = %d, want one process", starts.Load())
	}
	if second.PID() != first.PID() || second.ProfileDir() != first.ProfileDir() {
		t.Fatalf("reused browser identity/profile = %d/%q, want %d/%q", second.PID(), second.ProfileDir(), first.PID(), first.ProfileDir())
	}
	if control.terminate.Load() != 0 {
		t.Fatalf("reuse terminated the managed process: %d calls", control.terminate.Load())
	}

	// A normal session release is represented by not calling ManagedBrowser.Close;
	// the state and process remain available to the next session.
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state disappeared during normal detach: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("explicit Close(): %v", err)
	}
	if control.terminate.Load() != 1 || control.kill.Load() != 0 {
		t.Fatalf("explicit close terminate/kill = %d/%d, want 1/0", control.terminate.Load(), control.kill.Load())
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state after explicit close = %v, want removed", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second explicit Close(): %v", err)
	}
	if control.terminate.Load() != 1 {
		t.Fatalf("explicit close was not idempotent: %d terminate calls", control.terminate.Load())
	}
	waitForManagedLifecycleCleanup(t, configDir)
	_ = first
}

func TestManagedBrowserManagerRecoversMalformedOrStaleStateWithoutSignalingOldPID(t *testing.T) {
	configDir := t.TempDir()
	control := &managedLifecycleTestControl{}
	var starts atomic.Int32
	manager := newManagedLifecycleTestManager(t, configDir, control, &starts, "new-incarnation")
	statePath := ManagedBrowserStatePath(configDir)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("create managed profile: %v", err)
	}
	if err := os.WriteFile(statePath, []byte(`{"version":`), 0o600); err != nil {
		t.Fatalf("write malformed state: %v", err)
	}
	browser, err := manager.Acquire(context.Background(), ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("Acquire() after malformed state: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("launch count after malformed state = %d, want 1", starts.Load())
	}
	if control.terminate.Load() != 0 {
		t.Fatalf("malformed state signaled an unrelated process: %d calls", control.terminate.Load())
	}
	state, present, err := readManagedBrowserState(statePath)
	if err != nil || !present || state.ProcessIdentity != "new-incarnation" {
		t.Fatalf("replacement state = %#v present=%v err=%v", state, present, err)
	}
	if err := browser.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	control = &managedLifecycleTestControl{}
	starts.Store(0)
	manager = newManagedLifecycleTestManager(t, configDir, control, &starts, "replacement-incarnation")
	stale := ManagedBrowserState{
		Version:           ManagedBrowserStateVersion,
		PID:               99999,
		ProcessIdentity:   "stale-incarnation",
		ProfileDir:        browser.ProfileDir(),
		CDPURL:            "http://127.0.0.1:43210/json/version",
		BrowserWSEndpoint: "ws://127.0.0.1:43210/devtools/browser/stale",
	}
	if err := writeManagedBrowserState(statePath, stale); err != nil {
		t.Fatalf("write stale state: %v", err)
	}
	browser, err = manager.Acquire(context.Background(), ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("Acquire() after stale state: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("launch count after stale state = %d, want 1", starts.Load())
	}
	if control.terminate.Load() != 0 {
		t.Fatalf("stale state signaled old PID: %d calls", control.terminate.Load())
	}
	_ = browser.Close()
}

func TestManagedBrowserManagerRestartsVerifiedProfileOwnerAndRetriesOnce(t *testing.T) {
	configDir := t.TempDir()
	ownerControl := &managedLifecycleTestControl{}
	freshControl := &managedLifecycleTestControl{}
	var starts atomic.Int32
	var resolutions atomic.Int32
	manager := NewManagedBrowserManager(ManagedBrowserManagerOptions{
		ConfigDir: configDir,
		LaunchOptions: ManagedBrowserLaunchOptions{
			ConfigDir:        configDir,
			StartupURL:       "about:blank",
			DisplayAvailable: func() bool { return true },
			Acquirer: ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
				return ChromeExecutable{Path: "/qualified/test-chrome", Major: 152, Source: ExecutableSourceStock}, nil
			}),
			HTTPClient: &http.Client{Transport: managedLifecycleRecoveryTransport{starts: &starts}},
			ProcessStarter: func(string, []string) (ManagedBrowserProcess, error) {
				if starts.Add(1) == 1 {
					return managedLifecycleExitedProcess{pid: 7101}, nil
				}
				return freshControl.newProcess(7102), nil
			},
			StartupTimeout:  500 * time.Millisecond,
			PollInterval:    time.Millisecond,
			ShutdownTimeout: 50 * time.Millisecond,
		},
		ProcessInspector: ManagedBrowserProcessInspectorFunc(func(_ context.Context, state ManagedBrowserState) (ManagedBrowserProcessInfo, error) {
			return ManagedBrowserProcessInfo{PID: state.PID, Identity: "fresh-incarnation", ProfileDir: state.ProfileDir}, nil
		}),
		ProfileOwner: func(context.Context, string) (ManagedBrowserProcess, error) {
			resolutions.Add(1)
			return ownerControl.newProcess(7001), nil
		},
		LockTimeout: 2 * time.Second,
		LockPoll:    time.Millisecond,
	})

	browser, err := manager.Acquire(context.Background(), ManagedBrowserLaunchOptions{})
	if err != nil {
		t.Fatalf("Acquire() with orphaned profile owner: %v", err)
	}
	if starts.Load() != 2 {
		t.Fatalf("launch attempts = %d, want one failed attempt and one retry", starts.Load())
	}
	if resolutions.Load() != 1 || ownerControl.terminate.Load() != 1 || ownerControl.kill.Load() != 0 {
		t.Fatalf("profile-owner recovery = resolutions:%d terminate:%d kill:%d, want 1/1/0", resolutions.Load(), ownerControl.terminate.Load(), ownerControl.kill.Load())
	}
	if browser.PID() != 7102 {
		t.Fatalf("replacement browser PID = %d, want 7102", browser.PID())
	}
	if _, err := os.Stat(ManagedBrowserStatePath(configDir)); err != nil {
		t.Fatalf("replacement state was not published: %v", err)
	}
	if err := browser.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

func TestManagedBrowserManagerDoesNotRetryWithoutVerifiedProfileOwner(t *testing.T) {
	configDir := t.TempDir()
	var starts atomic.Int32
	var resolutions atomic.Int32
	manager := NewManagedBrowserManager(ManagedBrowserManagerOptions{
		ConfigDir: configDir,
		LaunchOptions: ManagedBrowserLaunchOptions{
			ConfigDir:        configDir,
			DisplayAvailable: func() bool { return true },
			Acquirer: ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
				return ChromeExecutable{Path: "/qualified/test-chrome", Major: 152, Source: ExecutableSourceStock}, nil
			}),
			HTTPClient: &http.Client{Transport: managedLaunchVersionTransport{alwaysError: errors.New("endpoint unavailable")}},
			ProcessStarter: func(string, []string) (ManagedBrowserProcess, error) {
				starts.Add(1)
				return managedLifecycleExitedProcess{pid: 7201}, nil
			},
			StartupTimeout:  100 * time.Millisecond,
			PollInterval:    time.Millisecond,
			ShutdownTimeout: 10 * time.Millisecond,
		},
		ProfileOwner: func(context.Context, string) (ManagedBrowserProcess, error) {
			resolutions.Add(1)
			return nil, nil
		},
		LockTimeout: time.Second,
		LockPoll:    time.Millisecond,
	})

	_, err := manager.Acquire(context.Background(), ManagedBrowserLaunchOptions{})
	if err == nil || !errors.Is(err, ErrManagedBrowserLaunch) {
		t.Fatalf("Acquire() error = %v, want managed launch failure", err)
	}
	if starts.Load() != 1 || resolutions.Load() != 1 {
		t.Fatalf("unverified recovery attempts = starts:%d resolutions:%d, want 1/1", starts.Load(), resolutions.Load())
	}
}

func TestManagedBrowserProfileOwnerStateRequiresExactManagedFlags(t *testing.T) {
	profileDir := filepath.Join(t.TempDir(), "browser-profile")
	managed := []string{
		"/Applications/Google", "Chrome.app/Contents/MacOS/Google", "Chrome",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=52507",
		"--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
		"--enable-blink-features=DeclarativeWebmcp",
		"--user-data-dir=" + profileDir,
	}
	state, ok := managedBrowserProfileOwnerState(55511, profileDir, managed)
	if !ok || state.PID != 55511 || state.CDPURL != "http://127.0.0.1:52507/json/version" {
		t.Fatalf("managed profile owner = %#v ok=%t", state, ok)
	}

	for name, remove := range map[string]string{
		"wrong profile":  "--user-data-dir=" + profileDir,
		"remote address": "--remote-debugging-address=127.0.0.1",
		"webmcp flags":   "--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
		"blink flag":     "--enable-blink-features=DeclarativeWebmcp",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := make([]string, 0, len(managed)-1)
			for _, argument := range managed {
				if argument != remove {
					candidate = append(candidate, argument)
				}
			}
			if _, ok := managedBrowserProfileOwnerState(55511, profileDir, candidate); ok {
				t.Fatal("process without the exact managed ownership flags was accepted")
			}
		})
	}
}

func TestManagedBrowserSingletonPIDRequiresChromeSymlinkShape(t *testing.T) {
	profileDir := t.TempDir()
	lockPath := filepath.Join(profileDir, managedBrowserSingletonLockName)
	if err := os.Symlink("host-with-dashes-55511", lockPath); err != nil {
		t.Fatalf("create singleton link: %v", err)
	}
	pid, err := managedBrowserSingletonPID(profileDir)
	if err != nil || pid != 55511 {
		t.Fatalf("singleton PID = %d, %v; want 55511", pid, err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove singleton link: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("55511"), 0o600); err != nil {
		t.Fatalf("create non-symlink singleton: %v", err)
	}
	pid, err = managedBrowserSingletonPID(profileDir)
	if err != nil || pid != 0 {
		t.Fatalf("regular-file singleton PID = %d, %v; want no attributable owner", pid, err)
	}
}

func TestReattachedManagedBrowserToleratesTransientInspectionFailures(t *testing.T) {
	var calls atomic.Int32
	var persistentFailure atomic.Bool
	process := &reattachedManagedBrowserProcess{
		state: ManagedBrowserState{PID: 55511},
		inspector: ManagedBrowserProcessInspectorFunc(func(context.Context, ManagedBrowserState) (ManagedBrowserProcessInfo, error) {
			call := calls.Add(1)
			if persistentFailure.Load() || call <= 2 {
				return ManagedBrowserProcessInfo{}, errors.New("transient inspection failure")
			}
			return ManagedBrowserProcessInfo{PID: 55511, Identity: "still-running"}, nil
		}),
	}
	done := make(chan struct{})
	go func() {
		_ = process.Wait()
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatal("reattached process exited after transient inspection failures")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if calls.Load() < 3 {
		t.Fatal("reattached process did not retry inspection")
	}
	persistentFailure.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reattached process did not exit after the bounded persistent-failure threshold")
	}
}

func TestManagedBrowserManagerSerializesConcurrentAcquisition(t *testing.T) {
	configDir := t.TempDir()
	control := &managedLifecycleTestControl{}
	var starts atomic.Int32
	manager := newManagedLifecycleTestManager(t, configDir, control, &starts, "concurrent-incarnation")
	var waitGroup sync.WaitGroup
	errorsCh := make(chan error, 2)
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := manager.Acquire(context.Background(), ManagedBrowserLaunchOptions{})
			if err == nil {
				// Keep the shared process alive; the test is checking acquisition,
				// not close-on-exit policy.
				return
			}
			errorsCh <- err
		}()
	}
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent Acquire(): %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("concurrent launch count = %d, want one", starts.Load())
	}
	if _, err := os.Stat(ManagedBrowserStatePath(configDir)); err != nil {
		t.Fatalf("state after concurrent acquisition: %v", err)
	}
	control.exit()
	waitForManagedLifecycleCleanup(t, configDir)
}

func waitForManagedLifecycleCleanup(t *testing.T, configDir string) {
	t.Helper()
	statePath := ManagedBrowserStatePath(configDir)
	lockPath := filepath.Join(filepath.Dir(statePath), managedBrowserLockName)
	deadline := time.Now().Add(time.Second)
	for {
		_, stateErr := os.Stat(statePath)
		_, lockErr := os.Stat(lockPath)
		if errors.Is(stateErr, os.ErrNotExist) && errors.Is(lockErr, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed lifecycle cleanup still active: state=%v lock=%v", stateErr, lockErr)
		}
		time.Sleep(time.Millisecond)
	}
}

func newManagedLifecycleTestManager(t *testing.T, configDir string, control *managedLifecycleTestControl, starts *atomic.Int32, identity string) *ManagedBrowserManager {
	t.Helper()
	return NewManagedBrowserManager(ManagedBrowserManagerOptions{
		ConfigDir: configDir,
		LaunchOptions: ManagedBrowserLaunchOptions{
			ConfigDir:        configDir,
			StartupURL:       "about:blank",
			DisplayAvailable: func() bool { return true },
			Acquirer: ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
				return ChromeExecutable{Path: "/qualified/test-chrome", Major: 152, Source: ExecutableSourceStock}, nil
			}),
			HTTPClient: &http.Client{Transport: managedLaunchVersionTransport{}},
			ProcessStarter: func(string, []string) (ManagedBrowserProcess, error) {
				starts.Add(1)
				return control.newProcess(7001), nil
			},
			StartupTimeout:  500 * time.Millisecond,
			PollInterval:    time.Millisecond,
			ShutdownTimeout: 50 * time.Millisecond,
		},
		ProcessInspector: ManagedBrowserProcessInspectorFunc(func(_ context.Context, state ManagedBrowserState) (ManagedBrowserProcessInfo, error) {
			return ManagedBrowserProcessInfo{PID: state.PID, Identity: identity, ProfileDir: state.ProfileDir}, nil
		}),
		ProcessReattacher: func(context.Context, ManagedBrowserState) (ManagedBrowserProcess, error) {
			return control.newProcess(7001), nil
		},
		LockTimeout: 2 * time.Second,
		LockPoll:    time.Millisecond,
	})
}

type managedLifecycleTestControl struct {
	done      chan struct{}
	initOnce  sync.Once
	exitOnce  sync.Once
	terminate atomic.Int32
	kill      atomic.Int32
}

func (c *managedLifecycleTestControl) initialize() {
	c.initOnce.Do(func() { c.done = make(chan struct{}) })
}

func (c *managedLifecycleTestControl) newProcess(pid int) *managedLifecycleTestProcess {
	c.initialize()
	return &managedLifecycleTestProcess{control: c, pid: pid}
}

func (c *managedLifecycleTestControl) exit() {
	c.initialize()
	c.exitOnce.Do(func() { close(c.done) })
}

type managedLifecycleTestProcess struct {
	control *managedLifecycleTestControl
	pid     int
}

func (p *managedLifecycleTestProcess) Wait() error {
	if p == nil || p.control == nil {
		return errors.New("test process unavailable")
	}
	<-p.control.done
	return nil
}

func (p *managedLifecycleTestProcess) Terminate() error {
	p.control.terminate.Add(1)
	p.control.exit()
	return nil
}

func (p *managedLifecycleTestProcess) Kill() error {
	p.control.kill.Add(1)
	p.control.exit()
	return nil
}

func (p *managedLifecycleTestProcess) PID() int { return p.pid }

type managedLifecycleExitedProcess struct{ pid int }

func (p managedLifecycleExitedProcess) Wait() error      { return errors.New("process exited") }
func (p managedLifecycleExitedProcess) Terminate() error { return nil }
func (p managedLifecycleExitedProcess) Kill() error      { return nil }
func (p managedLifecycleExitedProcess) PID() int         { return p.pid }

type managedLifecycleRecoveryTransport struct{ starts *atomic.Int32 }

func (t managedLifecycleRecoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.starts != nil && t.starts.Load() == 1 {
		time.Sleep(5 * time.Millisecond)
		return nil, errors.New("first launch endpoint unavailable")
	}
	return managedLaunchVersionTransport{}.RoundTrip(request)
}
