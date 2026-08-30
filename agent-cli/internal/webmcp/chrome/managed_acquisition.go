package chrome

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// MinimumManagedChromeMajor is the minimum Chrome major version that the
	// managed WebMCP path is allowed to start.
	MinimumManagedChromeMajor = 151

	// A freshly extracted Chrome for Testing binary can spend several seconds
	// completing macOS first-launch validation before answering --version. Keep
	// the probe bounded, but leave enough room for that one-time platform work.
	defaultChromeVersionTimeout = 10 * time.Second
	maxChromeVersionOutputBytes = 64 << 10
)

// ExecutableSource identifies how a Chrome executable was obtained.
type ExecutableSource string

const (
	// ExecutableSourceStock identifies an already-installed system Chrome.
	ExecutableSourceStock ExecutableSource = "stock"
	// ExecutableSourceChromeForTesting identifies the verified pinned fallback.
	ExecutableSourceChromeForTesting ExecutableSource = "chrome_for_testing"
)

// ChromeExecutable is the safe result of managed browser acquisition. Path is
// intended for the process launcher and is never included in user-facing
// acquisition errors.
type ChromeExecutable struct {
	Path    string
	Version string
	Major   int
	Source  ExecutableSource
}

// VersionQuery runs the executable's bounded product-version query. It is an
// injection seam for hermetic tests; the production implementation executes
// the candidate with --version.
type VersionQuery func(context.Context, string) (string, error)

// ExecutableCheck validates that a candidate can be started. It is injectable
// so acquisition tests can model permissions and platform-specific behavior
// without weakening the production executable check.
type ExecutableCheck func(string) error

// PinnedChromeRequest carries the validated platform and cache inputs to the
// Chrome for Testing fallback.
type PinnedChromeRequest struct {
	Platform      string
	RequiredMajor int
	LockPath      string
	CacheDir      string
	HTTPClient    *http.Client
}

// PinnedChromeAcquirer is the fallback seam used after all stock candidates
// have failed qualification.
type PinnedChromeAcquirer interface {
	AcquirePinnedChrome(context.Context, PinnedChromeRequest) (ChromeExecutable, error)
}

// PinnedChromeAcquirerFunc adapts a function to PinnedChromeAcquirer.
type PinnedChromeAcquirerFunc func(context.Context, PinnedChromeRequest) (ChromeExecutable, error)

// AcquirePinnedChrome implements PinnedChromeAcquirer.
func (f PinnedChromeAcquirerFunc) AcquirePinnedChrome(ctx context.Context, request PinnedChromeRequest) (ChromeExecutable, error) {
	if f == nil {
		return ChromeExecutable{}, errors.New("pinned Chrome acquirer is nil")
	}
	return f(ctx, request)
}

// ManagedChromeAcquisitionError is the one safe operator-facing failure for
// managed executable selection. The underlying cause remains available for
// diagnostics through Unwrap, but Error never includes paths, URLs, command
// output, or nested download/process details.
type ManagedChromeAcquisitionError struct {
	RequiredMajor    int
	Platform         string
	FallbackCategory string
	Cause            error
}

var ErrManagedChromeUnavailable = errors.New("managed Chrome is unavailable")

func (e *ManagedChromeAcquisitionError) Error() string {
	if e == nil {
		return ErrManagedChromeUnavailable.Error()
	}
	major := e.RequiredMajor
	if major <= 0 {
		major = MinimumManagedChromeMajor
	}
	category := safeAcquisitionCategory(e.FallbackCategory)
	if category == "" {
		category = "unavailable"
	}
	platform := safePlatformLabel(e.Platform)
	if platform == "" {
		platform = "current platform"
	}
	return fmt.Sprintf("managed WebMCP browser requires Chrome %d or newer; no qualified stock Chrome was available and the verified Chrome for Testing fallback failed (%s on %s); install Chrome %d or newer, or supply an explicit --browser-cdp-url or --browser-ws-endpoint", major, category, platform, major)
}

func (e *ManagedChromeAcquisitionError) Unwrap() error {
	if e == nil {
		return ErrManagedChromeUnavailable
	}
	return errors.Join(ErrManagedChromeUnavailable, e.Cause)
}

// ManagedChromeAcquisitionOptions configures managed executable selection.
// Nil slices and functions select production defaults; a non-nil empty
// StockPaths deliberately disables stock probing for hermetic tests.
type ManagedChromeAcquisitionOptions struct {
	GOOS            string
	GOARCH          string
	RequiredMajor   int
	StockPaths      []string
	VersionQuery    VersionQuery
	ExecutableCheck ExecutableCheck
	VersionTimeout  time.Duration
	PinnedAcquirer  PinnedChromeAcquirer
	LockPath        string
	CacheDir        string
	HTTPClient      *http.Client
}

// ManagedChromeAcquirer selects a qualified stock Chrome or the verified
// Chrome for Testing fallback.
type ManagedChromeAcquirer struct {
	options ManagedChromeAcquisitionOptions
}

// NewManagedChromeAcquirer constructs a stock-first managed browser selector.
func NewManagedChromeAcquirer(options ManagedChromeAcquisitionOptions) *ManagedChromeAcquirer {
	if options.RequiredMajor <= 0 {
		options.RequiredMajor = MinimumManagedChromeMajor
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.VersionTimeout <= 0 {
		options.VersionTimeout = defaultChromeVersionTimeout
	}
	if options.VersionQuery == nil {
		options.VersionQuery = queryChromeVersion
	}
	if options.ExecutableCheck == nil {
		options.ExecutableCheck = checkChromeExecutable
	}
	if options.StockPaths == nil {
		options.StockPaths = DefaultStockChromePaths(options.GOOS, options.GOARCH)
	} else {
		options.StockPaths = append([]string(nil), options.StockPaths...)
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	return &ManagedChromeAcquirer{options: options}
}

// NewAcquirer is a concise constructor alias for callers that already name
// the managed-browser context around the acquisition seam.
func NewAcquirer(options ManagedChromeAcquisitionOptions) *ManagedChromeAcquirer {
	return NewManagedChromeAcquirer(options)
}

// Acquire selects a qualified executable. Stock failures are intentionally
// collapsed and never prevent the verified fallback from being attempted.
func (a *ManagedChromeAcquirer) Acquire(ctx context.Context) (ChromeExecutable, error) {
	if a == nil {
		return ChromeExecutable{}, &ManagedChromeAcquisitionError{
			RequiredMajor:    MinimumManagedChromeMajor,
			Platform:         runtime.GOOS + "/" + runtime.GOARCH,
			FallbackCategory: "selector_unavailable",
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ChromeExecutable{}, err
	}

	for _, candidate := range uniquePaths(a.options.StockPaths) {
		if err := ctx.Err(); err != nil {
			return ChromeExecutable{}, err
		}
		if err := a.options.ExecutableCheck(candidate); err != nil {
			continue
		}
		version, err := boundedVersionQuery(ctx, a.options.VersionQuery, candidate, a.options.VersionTimeout)
		if err != nil {
			continue
		}
		major, err := ParseChromeMajorVersion(version)
		if err != nil || major < a.options.RequiredMajor {
			continue
		}
		return ChromeExecutable{
			Path:    candidate,
			Version: strings.TrimSpace(version),
			Major:   major,
			Source:  ExecutableSourceStock,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return ChromeExecutable{}, err
	}

	platform, platformErr := ChromeForTestingPlatform(a.options.GOOS, a.options.GOARCH)
	if platformErr != nil {
		return ChromeExecutable{}, a.safeFailure(a.options.GOOS+"/"+a.options.GOARCH, "platform_unsupported", platformErr)
	}
	fallback := a.options.PinnedAcquirer
	if fallback == nil {
		fallback = NewChromeForTestingAcquirer(ChromeForTestingOptions{
			LockPath:   a.options.LockPath,
			CacheDir:   a.options.CacheDir,
			HTTPClient: a.options.HTTPClient,
		})
	}
	request := PinnedChromeRequest{
		Platform:      platform,
		RequiredMajor: a.options.RequiredMajor,
		LockPath:      a.options.LockPath,
		CacheDir:      a.options.CacheDir,
		HTTPClient:    a.options.HTTPClient,
	}
	executable, err := fallback.AcquirePinnedChrome(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ChromeExecutable{}, ctxErr
		}
		return ChromeExecutable{}, a.safeFailure(platform, fallbackFailureCategory(err), err)
	}
	validated, err := a.validateFallbackExecutable(ctx, executable)
	if err != nil {
		return ChromeExecutable{}, a.safeFailure(platform, fallbackFailureCategory(err), err)
	}
	executable = validated
	executable.Source = ExecutableSourceChromeForTesting
	return executable, nil
}

// AcquireManagedChrome is the function-form entry point for composition
// roots that do not need to retain an acquirer instance.
func AcquireManagedChrome(ctx context.Context, options ManagedChromeAcquisitionOptions) (ChromeExecutable, error) {
	return NewManagedChromeAcquirer(options).Acquire(ctx)
}

func (a *ManagedChromeAcquirer) validateFallbackExecutable(ctx context.Context, executable ChromeExecutable) (ChromeExecutable, error) {
	if err := a.options.ExecutableCheck(executable.Path); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("executable_unavailable", err)
	}
	major := executable.Major
	if major <= 0 {
		version := executable.Version
		if strings.TrimSpace(version) == "" {
			queried, err := boundedVersionQuery(ctx, a.options.VersionQuery, executable.Path, a.options.VersionTimeout)
			if err != nil {
				return ChromeExecutable{}, newChromeForTestingError("version_unverified", err)
			}
			version = queried
		}
		parsed, err := ParseChromeMajorVersion(version)
		if err != nil {
			return ChromeExecutable{}, newChromeForTestingError("version_unverified", err)
		}
		major = parsed
		executable.Version = strings.TrimSpace(version)
	}
	if major < a.options.RequiredMajor {
		return ChromeExecutable{}, newChromeForTestingError("unqualified_executable", fmt.Errorf("major version %d is below the required minimum", major))
	}
	executable.Major = major
	return executable, nil
}

func (a *ManagedChromeAcquirer) safeFailure(platform, category string, cause error) error {
	return &ManagedChromeAcquisitionError{
		RequiredMajor:    a.options.RequiredMajor,
		Platform:         platform,
		FallbackCategory: category,
		Cause:            cause,
	}
}

// ParseChromeMajorVersion extracts the first complete Chrome-style semantic
// version from --version output and returns its major component.
func ParseChromeMajorVersion(output string) (int, error) {
	match := chromeVersionPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		match = bareChromeVersionPattern.FindStringSubmatch(strings.TrimSpace(output))
	}
	if len(match) != 2 {
		return 0, errors.New("chrome version output is unparsable")
	}
	major, err := strconv.Atoi(match[1])
	if err != nil || major <= 0 {
		return 0, errors.New("chrome version major is invalid")
	}
	return major, nil
}

var chromeVersionPattern = regexp.MustCompile(`(?i)\b(?:google\s+chrome|chrome|chromium)(?:\s+for\s+testing)?\s+([0-9]{1,4})\.[0-9]+(?:\.[0-9]+){1,2}(?:[^0-9]|$)`)
var bareChromeVersionPattern = regexp.MustCompile(`^([0-9]{1,4})\.[0-9]+(?:\.[0-9]+){1,2}$`)

func boundedVersionQuery(ctx context.Context, query VersionQuery, path string, timeout time.Duration) (string, error) {
	if query == nil {
		return "", errors.New("chrome version query is not configured")
	}
	if timeout <= 0 {
		timeout = defaultChromeVersionTimeout
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		output string
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		output, err := query(queryCtx, path)
		resultCh <- result{output: output, err: err}
	}()
	select {
	case result := <-resultCh:
		if len(result.output) > maxChromeVersionOutputBytes {
			return "", errors.New("chrome version output exceeds the safety bound")
		}
		return result.output, result.err
	case <-queryCtx.Done():
		return "", queryCtx.Err()
	}
}

func queryChromeVersion(ctx context.Context, executable string) (string, error) {
	command := exec.CommandContext(ctx, executable, "--version")
	var output limitedChromeOutput
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return "", err
	}
	if output.overflow {
		return "", errors.New("chrome version output exceeds the safety bound")
	}
	return output.String(), nil
}

type limitedChromeOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedChromeOutput) Write(value []byte) (int, error) {
	remaining := maxChromeVersionOutputBytes - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = b.Buffer.Write(value[:remaining])
		b.overflow = true
		return len(value), nil
	}
	return b.Buffer.Write(value)
}

func checkChromeExecutable(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("chrome executable path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return errors.New("chrome candidate is not executable")
	}
	return nil
}

// DefaultStockChromePaths returns recognized system Chrome locations for the
// supplied platform. It does not inspect running processes or user profiles.
func DefaultStockChromePaths(goos, goarch string) []string {
	var paths []string
	home, _ := os.UserHomeDir()
	joinHome := func(parts ...string) {
		if home != "" {
			paths = append(paths, filepath.Join(append([]string{home}, parts...)...))
		}
	}
	switch goos {
	case "darwin":
		paths = append(paths,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
		)
		joinHome("Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome")
		joinHome("Applications", "Google Chrome Canary.app", "Contents", "MacOS", "Google Chrome Canary")
	case "linux":
		paths = append(paths,
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
			"/opt/google/chrome/google-chrome",
		)
		joinHome(".local", "bin", "google-chrome-stable")
		joinHome(".local", "bin", "google-chrome")
	case "windows":
		for _, root := range []string{os.Getenv("LOCALAPPDATA"), os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("ProgramW6432")} {
			if root == "" {
				continue
			}
			paths = append(paths, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
		}
	}
	pathNames := []string{"google-chrome-stable", "google-chrome", "chrome", "chrome.exe"}
	if goos == "windows" {
		pathNames = []string{"chrome.exe", "chrome"}
	}
	for _, name := range pathNames {
		if located, err := exec.LookPath(name); err == nil {
			paths = append(paths, located)
		}
	}
	return uniquePaths(paths)
}

// ChromeForTestingPlatform maps Go's platform tuple to the official Chrome
// for Testing download platform names.
func ChromeForTestingPlatform(goos, goarch string) (string, error) {
	switch {
	case goos == "darwin" && goarch == "arm64":
		return "mac-arm64", nil
	case goos == "darwin" && goarch == "amd64":
		return "mac-x64", nil
	case goos == "linux" && goarch == "amd64":
		return "linux64", nil
	case goos == "windows" && goarch == "amd64":
		return "win64", nil
	case goos == "windows" && goarch == "386":
		return "win32", nil
	default:
		return "", fmt.Errorf("chrome for testing has no supported artifact for %s/%s", goos, goarch)
	}
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

func fallbackFailureCategory(err error) string {
	var fallbackErr *ChromeForTestingError
	if errors.As(err, &fallbackErr) && fallbackErr != nil && fallbackErr.Category != "" {
		return fallbackErr.Category
	}
	var acquisitionErr *ManagedChromeAcquisitionError
	if errors.As(err, &acquisitionErr) && acquisitionErr != nil && acquisitionErr.FallbackCategory != "" {
		return acquisitionErr.FallbackCategory
	}
	return "acquisition_failed"
}

func safeAcquisitionCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
		}
		if builder.Len() >= 48 {
			break
		}
	}
	return builder.String()
}

func safePlatformLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 32 {
		value = value[:32]
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '/' || r == '-' || r == '_') {
			return "current platform"
		}
	}
	return value
}
