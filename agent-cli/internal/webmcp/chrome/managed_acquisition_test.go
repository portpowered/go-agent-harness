package chrome

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedChromeAcquirerPrefersQualifiedStockChrome(t *testing.T) {
	stockPath := writeExecutableFixture(t, "#!/bin/sh\necho 'Google Chrome 151.0.7922.174'\n")
	pinnedCalls := 0
	acquirer := NewManagedChromeAcquirer(ManagedChromeAcquisitionOptions{
		GOOS:       "darwin",
		GOARCH:     "arm64",
		StockPaths: []string{stockPath},
		VersionQuery: func(context.Context, string) (string, error) {
			return "Google Chrome 151.0.7922.174", nil
		},
		PinnedAcquirer: PinnedChromeAcquirerFunc(func(context.Context, PinnedChromeRequest) (ChromeExecutable, error) {
			pinnedCalls++
			return ChromeExecutable{}, errors.New("fallback must not run")
		}),
	})

	got, err := acquirer.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(): %v", err)
	}
	if got.Path != stockPath || got.Major != MinimumManagedChromeMajor || got.Source != ExecutableSourceStock {
		t.Fatalf("stock result = %+v", got)
	}
	if pinnedCalls != 0 {
		t.Fatalf("pinned fallback calls = %d, want zero", pinnedCalls)
	}
}

func TestManagedChromeAcquirerFallsBackAfterOldUnusableStockCandidates(t *testing.T) {
	oldPath := writeExecutableFixture(t, "#!/bin/sh\necho 'Google Chrome 150.0.1.2'\n")
	otherPath := writeExecutableFixture(t, "#!/bin/sh\necho 'not a version'\n")
	fallbackPath := writeExecutableFixture(t, "#!/bin/sh\necho 'Google Chrome for Testing 152.0.7977.64'\n")
	var request PinnedChromeRequest
	acquirer := NewManagedChromeAcquirer(ManagedChromeAcquisitionOptions{
		GOOS:       "darwin",
		GOARCH:     "arm64",
		StockPaths: []string{oldPath, otherPath},
		VersionQuery: func(_ context.Context, path string) (string, error) {
			if path == oldPath {
				return "Google Chrome 150.0.1.2", nil
			}
			return "not a version", nil
		},
		PinnedAcquirer: PinnedChromeAcquirerFunc(func(_ context.Context, got PinnedChromeRequest) (ChromeExecutable, error) {
			request = got
			return ChromeExecutable{Path: fallbackPath, Version: "Google Chrome for Testing 152.0.7977.64", Major: 152}, nil
		}),
	})

	got, err := acquirer.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(): %v", err)
	}
	if got.Path != fallbackPath || got.Major != 152 || got.Source != ExecutableSourceChromeForTesting {
		t.Fatalf("fallback result = %+v", got)
	}
	if request.Platform != "mac-arm64" || request.RequiredMajor != MinimumManagedChromeMajor {
		t.Fatalf("fallback request = %+v", request)
	}
}

func TestManagedChromeAcquirerSkipsNonExecutableAndBoundedFailures(t *testing.T) {
	nonExecutable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("Chrome 151.0.1.2"), 0o600); err != nil {
		t.Fatalf("write non-executable candidate: %v", err)
	}
	slowPath := writeExecutableFixture(t, "#!/bin/sh\n")
	validPath := writeExecutableFixture(t, "#!/bin/sh\n")
	var queried []string
	var mu sync.Mutex
	acquirer := NewManagedChromeAcquirer(ManagedChromeAcquisitionOptions{
		GOOS:           "linux",
		GOARCH:         "amd64",
		StockPaths:     []string{nonExecutable, slowPath, validPath},
		VersionTimeout: 20 * time.Millisecond,
		VersionQuery: func(ctx context.Context, path string) (string, error) {
			mu.Lock()
			queried = append(queried, path)
			mu.Unlock()
			if path == slowPath {
				<-ctx.Done()
				return "", ctx.Err()
			}
			if path == validPath {
				return "Google Chrome 151.0.1.2", nil
			}
			return "", errors.New("candidate should have been rejected before query")
		},
		PinnedAcquirer: PinnedChromeAcquirerFunc(func(context.Context, PinnedChromeRequest) (ChromeExecutable, error) {
			return ChromeExecutable{Path: validPath, Version: "Google Chrome 151.0.1.2", Major: 151}, nil
		}),
	})

	got, err := acquirer.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(): %v", err)
	}
	if got.Path != validPath || got.Source != ExecutableSourceStock {
		t.Fatalf("result = %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queried) != 2 || queried[0] != slowPath || queried[1] != validPath {
		t.Fatalf("version queries = %v, want only slow then valid stock candidates", queried)
	}
}

func TestParseChromeMajorVersion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		major  int
		valid  bool
	}{
		{name: "stock", output: "Google Chrome 151.0.7922.174", major: 151, valid: true},
		{name: "for testing", output: "Google Chrome for Testing 152.0.7977.64\n", major: 152, valid: true},
		{name: "stderr prefix", output: "warning\nChromium 151.0.1.2", major: 151, valid: true},
		{name: "missing", output: "Google Chrome unknown", valid: false},
		{name: "incomplete", output: "Chrome 151.", valid: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			major, err := ParseChromeMajorVersion(testCase.output)
			if testCase.valid {
				if err != nil || major != testCase.major {
					t.Fatalf("ParseChromeMajorVersion() = %d/%v, want %d", major, err, testCase.major)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseChromeMajorVersion(%q) unexpectedly succeeded with %d", testCase.output, major)
			}
		})
	}
}

func TestChromeForTestingPlatform(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
		valid  bool
	}{
		{goos: "darwin", goarch: "arm64", want: "mac-arm64", valid: true},
		{goos: "darwin", goarch: "amd64", want: "mac-x64", valid: true},
		{goos: "linux", goarch: "amd64", want: "linux64", valid: true},
		{goos: "windows", goarch: "amd64", want: "win64", valid: true},
		{goos: "windows", goarch: "386", want: "win32", valid: true},
		{goos: "linux", goarch: "arm64", valid: false},
	}
	for _, testCase := range tests {
		got, err := ChromeForTestingPlatform(testCase.goos, testCase.goarch)
		if testCase.valid && (err != nil || got != testCase.want) {
			t.Errorf("ChromeForTestingPlatform(%s/%s) = %q/%v, want %q", testCase.goos, testCase.goarch, got, err, testCase.want)
		}
		if !testCase.valid && err == nil {
			t.Errorf("ChromeForTestingPlatform(%s/%s) unexpectedly succeeded with %q", testCase.goos, testCase.goarch, got)
		}
	}
}

func TestChromeForTestingAcquirerVerifiesAndCachesOneCompleteArtifact(t *testing.T) {
	platform, err := ChromeForTestingPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("test platform has no Chrome for Testing artifact: %v", err)
	}
	const version = "152.0.7977.64"
	const revision = "test-revision"
	const manifestURL = "https://googlechromelabs.github.io/chrome-for-testing/test-manifest.json"
	const downloadURL = "https://storage.googleapis.com/chrome-for-testing-public/test/chrome.zip"
	executableRelative := filepath.ToSlash(filepath.Join("chrome-"+platform, "Chrome", "chrome-test"))
	archive := chromeArchive(t, executableRelative, "#!/bin/sh\necho 'Google Chrome for Testing "+version+"'\n")
	digest := sha256.Sum256(archive)
	lock := ChromeForTestingLock{
		Channel:             "Stable",
		Platform:            platform,
		Version:             version,
		Revision:            revision,
		ManifestURL:         manifestURL,
		ManifestRetrievedAt: "2026-08-30T00:00:00Z",
		DownloadURL:         downloadURL,
		ArchiveSHA256:       hex.EncodeToString(digest[:]),
		ExecutableRelative:  executableRelative,
	}
	lockPath := filepath.Join(t.TempDir(), "chrome-for-testing.json")
	lockBytes, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := os.WriteFile(lockPath, lockBytes, 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	transport := &chromeForTestingFixtureTransport{
		manifest: []byte(`{"versions":[{"version":"` + version + `","revision":"` + revision + `","downloads":{"chrome":[{"platform":"` + platform + `","url":"` + downloadURL + `"}]}}]}`),
		archive:  archive,
	}
	client := &http.Client{Transport: transport}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	acquirer := NewChromeForTestingAcquirer(ChromeForTestingOptions{HTTPClient: client})
	request := PinnedChromeRequest{Platform: platform, RequiredMajor: MinimumManagedChromeMajor, LockPath: lockPath, CacheDir: cacheDir, HTTPClient: client}

	first, err := acquirer.AcquirePinnedChrome(context.Background(), request)
	if err != nil {
		t.Fatalf("first AcquirePinnedChrome(): %v", err)
	}
	second, err := acquirer.AcquirePinnedChrome(context.Background(), request)
	if err != nil {
		t.Fatalf("cached AcquirePinnedChrome(): %v", err)
	}
	if first.Path != second.Path || first.Source != ExecutableSourceChromeForTesting || !strings.Contains(first.Version, version) {
		t.Fatalf("first/second results = %+v/%+v", first, second)
	}
	if transport.archiveCalls.Load() != 1 {
		t.Fatalf("archive downloads = %d, want one", transport.archiveCalls.Load())
	}
	readyPath := filepath.Join(cacheDir, chromeForTestingCacheDirName, chromeForTestingPlatformCacheKey(lock), chromeForTestingReadyName)
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("ready marker is unavailable: %v", err)
	}
}

func TestChromeForTestingAcquirerConcurrentCallersPublishOnlyReadyCache(t *testing.T) {
	platform, err := ChromeForTestingPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("test platform has no Chrome for Testing artifact: %v", err)
	}
	const version = "152.0.7977.64"
	const revision = "concurrent-revision"
	const manifestURL = "https://googlechromelabs.github.io/chrome-for-testing/concurrent-manifest.json"
	const downloadURL = "https://storage.googleapis.com/chrome-for-testing-public/concurrent/chrome.zip"
	executableRelative := filepath.ToSlash(filepath.Join("chrome-"+platform, "Chrome", "chrome-test"))
	archive := chromeArchive(t, executableRelative, "#!/bin/sh\necho 'Google Chrome for Testing "+version+"'\n")
	digest := sha256.Sum256(archive)
	lock := ChromeForTestingLock{Channel: "Stable", Platform: platform, Version: version, Revision: revision, ManifestURL: manifestURL, ManifestRetrievedAt: "2026-08-30T00:00:00Z", DownloadURL: downloadURL, ArchiveSHA256: hex.EncodeToString(digest[:]), ExecutableRelative: executableRelative}
	lockPath := filepath.Join(t.TempDir(), "chrome-for-testing.json")
	lockBytes, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := os.WriteFile(lockPath, lockBytes, 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	transport := &chromeForTestingFixtureTransport{
		manifest:     []byte(`{"channels":{"Stable":{"channel":"Stable","version":"` + version + `","revision":"` + revision + `","downloads":{"chrome":[{"platform":"` + platform + `","url":"` + downloadURL + `"}]}}}}`),
		archive:      archive,
		delayArchive: true,
	}
	client := &http.Client{Transport: transport}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	acquirer := NewChromeForTestingAcquirer(ChromeForTestingOptions{HTTPClient: client})
	request := PinnedChromeRequest{Platform: platform, RequiredMajor: MinimumManagedChromeMajor, LockPath: lockPath, CacheDir: cacheDir, HTTPClient: client}

	const callers = 6
	results := make(chan ChromeExecutable, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	group.Add(callers)
	for range callers {
		go func() {
			defer group.Done()
			result, acquireErr := acquirer.AcquirePinnedChrome(context.Background(), request)
			results <- result
			errs <- acquireErr
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for acquireErr := range errs {
		if acquireErr != nil {
			t.Fatalf("concurrent AcquirePinnedChrome(): %v", acquireErr)
		}
	}
	var firstPath string
	for result := range results {
		if firstPath == "" {
			firstPath = result.Path
		}
		if result.Path != firstPath || result.Source != ExecutableSourceChromeForTesting {
			t.Fatalf("concurrent result = %+v, first path = %q", result, firstPath)
		}
	}
	if transport.archiveCalls.Load() != 1 {
		t.Fatalf("concurrent archive downloads = %d, want one", transport.archiveCalls.Load())
	}
	readyPath := filepath.Join(cacheDir, chromeForTestingCacheDirName, chromeForTestingPlatformCacheKey(lock), chromeForTestingReadyName)
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("concurrent ready marker is unavailable: %v", err)
	}
}

func TestChromeForTestingAcquirerRemovesFailedAttemptWithoutReadyState(t *testing.T) {
	platform, err := ChromeForTestingPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("test platform has no Chrome for Testing artifact: %v", err)
	}
	const version = "152.0.7977.64"
	const revision = "failed-revision"
	const manifestURL = "https://googlechromelabs.github.io/chrome-for-testing/failed-manifest.json"
	const downloadURL = "https://storage.googleapis.com/chrome-for-testing-public/failed/chrome.zip"
	executableRelative := filepath.ToSlash(filepath.Join("chrome-"+platform, "Chrome", "chrome-test"))
	validArchive := chromeArchive(t, executableRelative, "#!/bin/sh\necho 'Google Chrome for Testing "+version+"'\n")
	corruptArchive := append(append([]byte(nil), validArchive...), []byte("corrupt")...)
	digest := sha256.Sum256(validArchive)
	lock := ChromeForTestingLock{Channel: "Stable", Platform: platform, Version: version, Revision: revision, ManifestURL: manifestURL, ManifestRetrievedAt: "2026-08-30T00:00:00Z", DownloadURL: downloadURL, ArchiveSHA256: hex.EncodeToString(digest[:]), ExecutableRelative: executableRelative}
	lockPath := filepath.Join(t.TempDir(), "chrome-for-testing.json")
	lockBytes, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := os.WriteFile(lockPath, lockBytes, 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	transport := &chromeForTestingFixtureTransport{
		manifest: []byte(`{"channels":{"Stable":{"channel":"` + "Stable" + `","version":"` + version + `","revision":"` + revision + `","downloads":{"chrome":[{"platform":"` + platform + `","url":"` + downloadURL + `"}]}}}}`),
		archive:  corruptArchive,
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	client := &http.Client{Transport: transport}
	acquirer := NewChromeForTestingAcquirer(ChromeForTestingOptions{HTTPClient: client})
	_, err = acquirer.AcquirePinnedChrome(context.Background(), PinnedChromeRequest{Platform: platform, RequiredMajor: MinimumManagedChromeMajor, LockPath: lockPath, CacheDir: cacheDir, HTTPClient: client})
	if err == nil || !strings.Contains(err.Error(), "archive_integrity") {
		t.Fatalf("corrupt archive error = %v, want archive_integrity", err)
	}
	readyPath := filepath.Join(cacheDir, chromeForTestingCacheDirName, chromeForTestingPlatformCacheKey(lock), chromeForTestingReadyName)
	if _, statErr := os.Stat(readyPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed acquisition ready marker stat error = %v, want absent", statErr)
	}
}

func chromeForTestingPlatformCacheKey(lock ChromeForTestingLock) string {
	platform, _ := ChromeForTestingPlatform(runtime.GOOS, runtime.GOARCH)
	return chromeForTestingCacheKey(platform, lock)
}

func TestManagedChromeAcquirerReturnsOneRedactedFailure(t *testing.T) {
	secret := "/private/secret/profile?token=do-not-print"
	acquirer := NewManagedChromeAcquirer(ManagedChromeAcquisitionOptions{
		GOOS:       "linux",
		GOARCH:     "amd64",
		StockPaths: []string{},
		PinnedAcquirer: PinnedChromeAcquirerFunc(func(context.Context, PinnedChromeRequest) (ChromeExecutable, error) {
			return ChromeExecutable{}, newChromeForTestingError("download_failed", errors.New(secret))
		}),
	})
	_, err := acquirer.Acquire(context.Background())
	if err == nil {
		t.Fatal("Acquire() unexpectedly succeeded")
	}
	var classified *ManagedChromeAcquisitionError
	if !errors.As(err, &classified) {
		t.Fatalf("error = %T %v, want ManagedChromeAcquisitionError", err, err)
	}
	message := err.Error()
	for _, want := range []string{"Chrome 151 or newer", "download_failed", "install Chrome 151 or newer", "--browser-cdp-url", "--browser-ws-endpoint"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not contain %q", message, want)
		}
	}
	if strings.Contains(message, secret) || strings.Contains(message, "private") || strings.Contains(message, "token") {
		t.Fatalf("failure exposed sensitive nested detail: %q", message)
	}
	if !errors.Is(err, ErrManagedChromeUnavailable) {
		t.Fatalf("errors.Is(%v, ErrManagedChromeUnavailable) = false", err)
	}
}

func writeExecutableFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write executable fixture: %v", err)
	}
	return path
}

func chromeArchive(t *testing.T, executableRelative, contents string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: executableRelative, Method: zip.Store}
	header.SetMode(0o700)
	file, err := archive.CreateHeader(header)
	if err != nil {
		t.Fatalf("create Chrome archive entry: %v", err)
	}
	if _, err := file.Write([]byte(contents)); err != nil {
		t.Fatalf("write Chrome archive entry: %v", err)
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close Chrome archive: %v", err)
	}
	return buffer.Bytes()
}

type chromeForTestingFixtureTransport struct {
	manifest     []byte
	archive      []byte
	delayArchive bool
	archiveCalls atomic.Int32
}

func (t *chromeForTestingFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.Contains(request.URL.Path, "manifest") {
		return chromeForTestingResponse(http.StatusOK, t.manifest), nil
	}
	if strings.Contains(request.URL.Path, "chrome.zip") {
		t.archiveCalls.Add(1)
		if t.delayArchive {
			time.Sleep(20 * time.Millisecond)
		}
		return chromeForTestingResponse(http.StatusOK, t.archive), nil
	}
	return chromeForTestingResponse(http.StatusNotFound, nil), nil
}

func chromeForTestingResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}
}
