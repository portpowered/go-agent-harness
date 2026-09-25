package chrome

// Shared pinned Chrome for Testing harness: acquisition, isolated launch,
// process lifecycle, and DevTools HTTP discovery used by the live suites.

import (
	"bufio"
	"bytes"
	"context"
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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type pinnedChrome struct {
	Lock       chromeForTestingLock
	Executable string
	WorkDir    string
}

type devToolsVersion struct {
	Browser              string `json:"Browser"`
	ProtocolVersion      string `json:"Protocol-Version"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type devToolsTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	Attached             bool   `json:"attached"`
}

type runningChrome struct {
	cmd           *exec.Cmd
	done          chan struct{}
	endpointValue string

	waitErr error

	closeOnce sync.Once
	closeErr  error
}

func acquirePinnedChrome(ctx context.Context, workDir string) (pinnedChrome, error) {
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		return pinnedChrome{}, fmt.Errorf("locked artifact platform is %s (darwin/arm64), observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}
	root, err := repositoryRoot()
	if err != nil {
		return pinnedChrome{}, err
	}
	lockPath := filepath.Join(root, "scripts", "webmcp-o0", "chrome-for-testing.json")
	lock, err := LoadChromeForTestingLock(lockPath)
	if err != nil {
		return pinnedChrome{}, fmt.Errorf("read O0 Chrome lock: %w", err)
	}
	if err := validatePinnedChromeLock(lock); err != nil {
		return pinnedChrome{}, err
	}
	platform, err := ChromeForTestingPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return pinnedChrome{}, err
	}
	acquirer := NewChromeForTestingAcquirer(ChromeForTestingOptions{})
	executable, err := acquirer.AcquirePinnedChrome(ctx, PinnedChromeRequest{
		Platform:      platform,
		RequiredMajor: MinimumManagedChromeMajor,
		LockPath:      lockPath,
		CacheDir:      workDir,
	})
	if err != nil {
		return pinnedChrome{}, err
	}
	return pinnedChrome{Lock: lock, Executable: executable.Path, WorkDir: workDir}, nil
}

func repositoryRoot() (string, error) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("locate integration test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "..")), nil
}

func validatePinnedChromeLock(lock chromeForTestingLock) error {
	if lock.Channel != lockedChromeChannel || lock.Platform != lockedChromePlatform || lock.Version != lockedChromeVersion || lock.Revision != lockedChromeRevision || lock.ArchiveSHA256 != lockedChromeSHA256 {
		return fmt.Errorf("O0 Chrome lock is not the qualified %s/%s %s revision %s artifact", lockedChromeChannel, lockedChromePlatform, lockedChromeVersion, lockedChromeRevision)
	}
	if lock.ManifestRetrievedAt == "" || lock.ExecutableRelative == "" {
		return errors.New("O0 Chrome lock omits manifest retrieval or executable metadata")
	}
	if !strings.HasPrefix(lock.ManifestURL, "https://googlechromelabs.github.io/chrome-for-testing/") {
		return fmt.Errorf("O0 Chrome manifest URL is not official: %q", lock.ManifestURL)
	}
	if !strings.HasPrefix(lock.DownloadURL, "https://storage.googleapis.com/chrome-for-testing-public/") {
		return fmt.Errorf("O0 Chrome download URL is not official: %q", lock.DownloadURL)
	}
	return nil
}

func launchPinnedChrome(ctx context.Context, pinned pinnedChrome, fixtureURL string) (*runningChrome, error) {
	return launchPinnedChromeAtPort(ctx, pinned, fixtureURL, 0)
}

// launchPinnedChromeAtPort keeps the normal O0 launch shape when port is zero
// and permits the recovery suite to deliberately reuse the old browser's
// loopback port after that browser has exited. The caller supplies only a
// pinned executable and a temporary profile owned by this test package.
func launchPinnedChromeAtPort(ctx context.Context, pinned pinnedChrome, fixtureURL string, port int) (*runningChrome, error) {
	profileDir := filepath.Join(pinned.WorkDir, "profile")
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("create isolated Chrome profile: %w", err)
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("Chrome debugging port is invalid: %d", port)
	}
	args := pinnedChromeLaunchFlags(profileDir, fixtureURL, port)
	cmd := exec.Command(pinned.Executable, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture Chrome stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capture Chrome stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Chrome: %w", err)
	}
	running := &runningChrome{cmd: cmd, done: make(chan struct{})}
	go func() {
		running.waitErr = cmd.Wait()
		close(running.done)
	}()
	endpoint := make(chan string, 1)
	var stdoutLog, stderrLog bytes.Buffer
	go scanChromeEndpoint(io.TeeReader(stdout, &stdoutLog), endpoint)
	go scanChromeEndpoint(io.TeeReader(stderr, &stderrLog), endpoint)
	select {
	case value := <-endpoint:
		running.setEndpoint(value)
		return running, nil
	case <-running.done:
		return nil, fmt.Errorf("Chrome exited before exposing DevTools: %s (stdout=%q stderr=%q)", describeOptionalError(running.waitErr), strings.TrimSpace(stdoutLog.String()), strings.TrimSpace(stderrLog.String()))
	case <-ctx.Done():
		discardSecondaryError(running.Close)
		return nil, fmt.Errorf("wait for Chrome DevTools endpoint: %w (stdout=%q stderr=%q)", ctx.Err(), strings.TrimSpace(stdoutLog.String()), strings.TrimSpace(stderrLog.String()))
	}
}

func pinnedChromeLaunchFlags(profileDir, fixtureURL string, port int) []string {
	return []string{
		"--headless=new",
		"--disable-gpu",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-extensions",
		"--disable-sync",
		"--no-default-browser-check",
		"--no-first-run",
		"--remote-debugging-address=127.0.0.1",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
		"--enable-blink-features=DeclarativeWebmcp",
		"--enable-experimental-web-platform-features",
		"--user-data-dir=" + profileDir,
		fixtureURL,
	}
}

func scanChromeEndpoint(reader io.Reader, endpoints chan<- string) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		match := devToolsEndpointPattern.FindStringSubmatch(scanner.Text())
		if len(match) != 2 {
			continue
		}
		select {
		case endpoints <- match[1]:
		default:
		}
		return
	}
}

func (p *runningChrome) endpoint() string {
	return p.endpointValue
}

func (p *runningChrome) setEndpoint(value string) {
	p.endpointValue = value
}

func (p *runningChrome) Close() error {
	p.closeOnce.Do(func() {
		select {
		case <-p.done:
			p.closeErr = p.waitErr
			return
		default:
		}
		if p.cmd.Process != nil {
			if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
				p.closeErr = err
			}
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			if p.cmd.Process != nil {
				discardSecondaryError(p.cmd.Process.Kill)
			}
			<-p.done
		}
		if p.closeErr == nil {
			p.closeErr = p.waitErr
		}
	})
	return p.closeErr
}

func (p *runningChrome) Kill() error {
	p.closeOnce.Do(func() {
		if p.cmd == nil || p.cmd.Process == nil {
			return
		}
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			p.closeErr = err
			return
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			p.closeErr = errors.New("Chrome did not exit after kill")
		}
	})
	return p.closeErr
}

func waitForDevToolsVersion(ctx context.Context, baseURL, expectedVersion string) (devToolsVersion, error) {
	var lastErr error
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		version, err := readDevToolsVersion(ctx, baseURL)
		if err == nil {
			if strings.Contains(version.Browser, expectedVersion) {
				return version, nil
			}
			lastErr = fmt.Errorf("browser identity = %q, want %s", version.Browser, expectedVersion)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return devToolsVersion{}, fmt.Errorf("wait for DevTools version: %w (last error: %s)", ctx.Err(), describeOptionalError(lastErr))
		case <-ticker.C:
		}
	}
}

func readDevToolsVersion(ctx context.Context, baseURL string) (devToolsVersion, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+devToolsVersionPath, nil)
	if err != nil {
		return devToolsVersion{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return devToolsVersion{}, err
	}
	defer closeAfterRead(response.Body)
	if response.StatusCode != http.StatusOK {
		return devToolsVersion{}, fmt.Errorf("DevTools version HTTP status: %s", response.Status)
	}
	var version devToolsVersion
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&version); err != nil {
		return devToolsVersion{}, err
	}
	return version, nil
}

func browserHTTPURL(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	switch parsed.Scheme {
	case "ws":
		parsed.Scheme = schemeHTTP
	case "wss":
		parsed.Scheme = schemeHTTPS
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func waitForFixturePageTarget(ctx context.Context, baseURL, fixtureURL string) (devToolsTarget, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		targets, err := readDevToolsTargets(ctx, baseURL)
		if err == nil {
			var matches []devToolsTarget
			for _, target := range targets {
				if target.Type == pageTargetType && target.URL == fixtureURL {
					matches = append(matches, target)
				}
			}
			if len(matches) == 1 {
				return matches[0], nil
			}
			lastErr = fmt.Errorf("found %d page targets for fixture URL", len(matches))
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return devToolsTarget{}, fmt.Errorf("wait for pre-attach fixture target: %w (last error: %s)", ctx.Err(), describeOptionalError(lastErr))
		case <-ticker.C:
		}
	}
}

func readDevToolsTargets(ctx context.Context, baseURL string) ([]devToolsTarget, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+jsonListPath, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer closeAfterRead(response.Body)
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DevTools target list HTTP status: %s", response.Status)
	}
	var targets []devToolsTarget
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

// discardSecondaryError runs a best-effort release on a path whose outcome is
// already determined; its error cannot change that outcome.
func discardSecondaryError(release func() error) {
	if err := release(); err != nil {
		return
	}
}

func closeRecoveryTarget(ctx context.Context, baseURL string, targetID webmcp.TargetID) error {
	requestURL := strings.TrimRight(baseURL, "/") + "/json/close/" + url.PathEscape(string(targetID))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer closeAfterRead(response.Body)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("target close HTTP status: %s", response.Status)
	}
	return nil
}

func recoveryEndpointPort(endpoint string) (int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Port() == "" {
		return 0, fmt.Errorf("parse Chrome endpoint port")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid Chrome endpoint port")
	}
	return port, nil
}

type cliChromeIntegrationBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *cliChromeIntegrationBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(value)
}

func (b *cliChromeIntegrationBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type cliChromeIntegrationStderr struct {
	cliChromeIntegrationBuffer
	firstLine chan string
	notified  bool
}

func newCLIChromeIntegrationStderr() *cliChromeIntegrationStderr {
	return &cliChromeIntegrationStderr{firstLine: make(chan string, 1)}
}

func (b *cliChromeIntegrationStderr) Write(value []byte) (int, error) {
	b.mu.Lock()
	written, err := b.data.Write(value)
	if !b.notified {
		if newline := bytes.IndexByte(b.data.Bytes(), '\n'); newline >= 0 {
			b.notified = true
			line := append([]byte(nil), b.data.Bytes()[:newline+1]...)
			b.mu.Unlock()
			b.firstLine <- string(line)
			return written, err
		}
	}
	b.mu.Unlock()
	return written, err
}

type pinnedCatalogDiscoverer struct {
	candidate webmcp.BrowserCandidate
}

func (d pinnedCatalogDiscoverer) Discover(_ context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if options.BrowserID != "" && options.BrowserID != d.candidate.ID {
		return nil, nil
	}
	return []webmcp.BrowserCandidate{d.candidate}, nil
}

func findRawFixtureTarget(targets []devToolsTarget, fixtureURL string) (devToolsTarget, error) {
	var matches []devToolsTarget
	for _, target := range targets {
		if target.Type == pageTargetType && target.URL == fixtureURL {
			matches = append(matches, target)
		}
	}
	if len(matches) != 1 {
		return devToolsTarget{}, fmt.Errorf("found %d page targets for fixture URL %q", len(matches), fixtureURL)
	}
	return matches[0], nil
}

func newNegativeControlBroker(adapter *Runtime, candidate webmcp.BrowserCandidate) *webmcp.StatefulBroker {
	return webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:            adapter,
		Discoverer:         pinnedCatalogDiscoverer{candidate: candidate},
		CatalogWait:        150 * time.Millisecond,
		LoadingCatalogWait: 150 * time.Millisecond,
	})
}

// failClosingBroker releases a broker before failing so its target session
// detaches from the shared browser.
func failClosingBroker(t *testing.T, broker *webmcp.StatefulBroker, format string, args ...any) {
	t.Helper()
	discardSecondaryError(broker.Close)
	t.Fatalf(format, args...)
}

// describeOptionalError renders a possibly-nil diagnostic error exactly as the
// %v verb would ("<nil>" when absent). The error is message context here, not
// a cause callers unwrap, so wrapping with %w would print "%!w(<nil>)".
func describeOptionalError(err error) string {
	return fmt.Sprint(err)
}
