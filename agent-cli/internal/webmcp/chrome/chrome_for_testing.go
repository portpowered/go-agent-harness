package chrome

import (
	"archive/zip"
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
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	chromeForTestingLockRelativePath = "scripts/webmcp-o0/chrome-for-testing.json"
	chromeForTestingCacheDirName     = "chrome-for-testing"
	chromeForTestingReadyName        = ".ready.json"
	chromeForTestingArchiveName      = "chrome-for-testing.zip"
	chromeForTestingExtractedName    = "extracted"
	chromeForTestingLockName         = ".acquire.lock"
	chromeForTestingManifestLimit    = 8 << 20
	chromeForTestingArchiveLimit     = 1<<30 + 1
	chromeForTestingReadyLimit       = 64 << 10
	chromeForTestingLockStaleAfter   = 10 * time.Minute
)

const (
	chromeForTestingManifestPrefix = "https://googlechromelabs.github.io/chrome-for-testing/"
	chromeForTestingDownloadPrefix = "https://storage.googleapis.com/chrome-for-testing-public/"
)

// ChromeForTestingLock is the repository-owned pin consumed by managed
// acquisition. The lock file is the only source of pinned version, download,
// digest, and executable layout metadata.
type ChromeForTestingLock struct {
	Channel             string `json:"channel"`
	Platform            string `json:"platform"`
	Version             string `json:"version"`
	Revision            string `json:"revision"`
	ManifestURL         string `json:"manifestURL"`
	ManifestRetrievedAt string `json:"manifestRetrievedAt"`
	DownloadURL         string `json:"downloadURL"`
	ArchiveSHA256       string `json:"archiveSHA256"`
	ExecutableRelative  string `json:"executable"`
}

// ChromeForTestingManifest is the subset of the official channel manifest
// needed to revalidate the repository pin before a download.
type ChromeForTestingManifest struct {
	Channels map[string]ChromeForTestingChannel `json:"channels"`
	Versions []ChromeForTestingChannel          `json:"versions"`
}

// ChromeForTestingChannel describes one official Chrome for Testing channel.
type ChromeForTestingChannel struct {
	Channel   string `json:"channel"`
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Downloads struct {
		Chrome []ChromeForTestingDownload `json:"chrome"`
	} `json:"downloads"`
}

// ChromeForTestingDownload identifies one platform-specific official archive.
type ChromeForTestingDownload struct {
	Platform string `json:"platform"`
	URL      string `json:"url"`
}

// ChromeForTestingOptions configures the verified fallback implementation.
// Empty values are resolved from the acquisition request or repository/cache
// defaults at call time.
type ChromeForTestingOptions struct {
	LockPath   string
	CacheDir   string
	HTTPClient *http.Client
}

// ChromeForTestingAcquirer downloads and verifies the repository-pinned
// Chrome for Testing artifact, caching only an atomically complete result.
type ChromeForTestingAcquirer struct {
	options ChromeForTestingOptions
}

// NewChromeForTestingAcquirer constructs the verified fallback acquirer.
func NewChromeForTestingAcquirer(options ChromeForTestingOptions) *ChromeForTestingAcquirer {
	return &ChromeForTestingAcquirer{options: options}
}

// AcquirePinnedChrome implements PinnedChromeAcquirer.
func (a *ChromeForTestingAcquirer) AcquirePinnedChrome(ctx context.Context, request PinnedChromeRequest) (ChromeExecutable, error) {
	if a == nil {
		return ChromeExecutable{}, newChromeForTestingError("acquirer_unavailable", errors.New("chrome for testing acquirer is nil"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ChromeExecutable{}, err
	}
	lockPath := firstNonEmpty(request.LockPath, a.options.LockPath)
	lock, err := LoadChromeForTestingLock(lockPath)
	if err != nil {
		return ChromeExecutable{}, newChromeForTestingError("lock_unavailable", err)
	}
	platform := request.Platform
	if platform == "" {
		return ChromeExecutable{}, newChromeForTestingError("platform_unsupported", errors.New("chrome for testing platform is empty"))
	}
	requiredMajor := request.RequiredMajor
	if requiredMajor <= 0 {
		requiredMajor = MinimumManagedChromeMajor
	}
	if err := ValidateChromeForTestingLock(lock, requiredMajor, platform); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("lock_invalid", err)
	}
	client := a.options.HTTPClient
	if client == nil {
		client = request.HTTPClient
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	manifest, err := fetchManagedChromeManifest(ctx, client, lock.ManifestURL)
	if err != nil {
		return ChromeExecutable{}, newChromeForTestingError("manifest_unavailable", err)
	}
	channel, ok := matchingChromeForTestingManifestEntry(manifest, lock)
	if !ok {
		return ChromeExecutable{}, newChromeForTestingError("manifest_mismatch", errors.New("official chrome for testing manifest does not match the lock"))
	}
	manifestDownloadURL := ""
	for _, download := range channel.Downloads.Chrome {
		if download.Platform == platform {
			if manifestDownloadURL != "" {
				return ChromeExecutable{}, newChromeForTestingError("manifest_mismatch", errors.New("official manifest has duplicate platform downloads"))
			}
			manifestDownloadURL = download.URL
		}
	}
	if manifestDownloadURL == "" || manifestDownloadURL != lock.DownloadURL {
		return ChromeExecutable{}, newChromeForTestingError("manifest_mismatch", errors.New("official chrome for testing download does not match the lock"))
	}
	if err := validateOfficialChromeURL(manifestDownloadURL, chromeForTestingDownloadPrefix); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("lock_invalid", err)
	}

	cacheDir := firstNonEmpty(request.CacheDir, a.options.CacheDir)
	if cacheDir == "" {
		cacheDir = defaultChromeForTestingCacheDir()
	}
	return a.acquireCached(ctx, client, lock, platform, requiredMajor, cacheDir)
}

func matchingChromeForTestingManifestEntry(manifest ChromeForTestingManifest, lock ChromeForTestingLock) (ChromeForTestingChannel, bool) {
	if channel, ok := manifest.Channels[lock.Channel]; ok && channel.Channel == lock.Channel && channel.Version == lock.Version && channel.Revision == lock.Revision {
		return channel, true
	}
	for _, version := range manifest.Versions {
		if version.Version == lock.Version && version.Revision == lock.Revision {
			return version, true
		}
	}
	return ChromeForTestingChannel{}, false
}

// LoadChromeForTestingLock reads and validates the JSON shape of the pin. It
// does not trust the lock until ValidateChromeForTestingLock checks its source
// URLs, digest, platform, and executable layout.
func LoadChromeForTestingLock(lockPath string) (ChromeForTestingLock, error) {
	if strings.TrimSpace(lockPath) == "" {
		resolved, err := ResolveChromeForTestingLockPath("")
		if err != nil {
			return ChromeForTestingLock{}, err
		}
		lockPath = resolved
	}
	file, err := os.Open(lockPath)
	if err != nil {
		return ChromeForTestingLock{}, err
	}
	defer file.Close()
	var lock ChromeForTestingLock
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	if err := decoder.Decode(&lock); err != nil {
		return ChromeForTestingLock{}, err
	}
	return lock, nil
}

// ValidateChromeForTestingLock verifies the repository pin before any network
// request. RequiredMajor may be zero to use MinimumManagedChromeMajor.
func ValidateChromeForTestingLock(lock ChromeForTestingLock, requiredMajor int, platform string) error {
	if requiredMajor <= 0 {
		requiredMajor = MinimumManagedChromeMajor
	}
	if strings.TrimSpace(lock.Channel) == "" || strings.TrimSpace(lock.Platform) == "" || strings.TrimSpace(lock.Version) == "" || strings.TrimSpace(lock.Revision) == "" || strings.TrimSpace(lock.ManifestRetrievedAt) == "" {
		return errors.New("chrome for testing lock omits required channel metadata")
	}
	if _, err := time.Parse(time.RFC3339, lock.ManifestRetrievedAt); err != nil {
		return errors.New("chrome for testing lock manifest retrieval time is invalid")
	}
	if platform != "" && lock.Platform != platform {
		return fmt.Errorf("chrome for testing lock platform is %q, want %q", lock.Platform, platform)
	}
	major, err := ParseChromeMajorVersion(lock.Version)
	if err != nil || major < requiredMajor {
		return fmt.Errorf("chrome for testing lock version is below the required Chrome %d minimum", requiredMajor)
	}
	if err := validateOfficialChromeURL(lock.ManifestURL, chromeForTestingManifestPrefix); err != nil {
		return fmt.Errorf("chrome for testing lock manifest source is invalid: %w", err)
	}
	if err := validateOfficialChromeURL(lock.DownloadURL, chromeForTestingDownloadPrefix); err != nil {
		return fmt.Errorf("chrome for testing lock download source is invalid: %w", err)
	}
	if !lowerHexDigestPattern.MatchString(lock.ArchiveSHA256) {
		return errors.New("chrome for testing lock archive digest is not a lowercase SHA-256")
	}
	if err := validateChromeArchivePath(lock.ExecutableRelative); err != nil {
		return fmt.Errorf("chrome for testing lock executable layout is invalid: %w", err)
	}
	return nil
}

// ResolveChromeForTestingLockPath locates the one repository-owned pin. An
// explicit path is used as-is; the environment override is intended for
// packaged installations and hermetic tests.
func ResolveChromeForTestingLockPath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	for _, name := range []string{"AGENT_WEBMCP_CHROME_FOR_TESTING_LOCK", "WEBMCP_CHROME_FOR_TESTING_LOCK"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, nil
		}
	}
	starts := make([]string, 0, 3)
	if workingDir, err := os.Getwd(); err == nil {
		starts = append(starts, workingDir)
	}
	if executable, err := os.Executable(); err == nil {
		if absolute, absErr := filepath.Abs(executable); absErr == nil {
			starts = append(starts, filepath.Dir(absolute))
		}
	}
	if sourceDir, ok := chromeSourceDirectory(); ok {
		starts = append(starts, sourceDir)
	}
	for _, start := range starts {
		if candidate, ok := findUpward(start, chromeForTestingLockRelativePath); ok {
			return candidate, nil
		}
	}
	return "", errors.New("repository Chrome for Testing lock could not be located")
}

func (a *ChromeForTestingAcquirer) acquireCached(ctx context.Context, client *http.Client, lock ChromeForTestingLock, platform string, requiredMajor int, cacheDir string) (ChromeExecutable, error) {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	if err := os.Chmod(cacheDir, 0o700); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	key := chromeForTestingCacheKey(platform, lock)
	finalDir := filepath.Join(cacheDir, chromeForTestingCacheDirName, key)
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o700); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	unlock, err := acquireChromeForTestingLock(ctx, finalDir+chromeForTestingLockName)
	if err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_lock_unavailable", err)
	}
	defer unlock()

	if executable, ok := readReadyChromeCache(ctx, finalDir, lock, requiredMajor); ok {
		return executable, nil
	}
	if err := os.RemoveAll(finalDir); err != nil && !os.IsNotExist(err) {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	stagingDir, err := os.MkdirTemp(filepath.Dir(finalDir), ".chrome-for-testing-*")
	if err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	defer os.RemoveAll(stagingDir)
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}

	archivePath := filepath.Join(stagingDir, chromeForTestingArchiveName)
	if err := downloadAndVerifyManagedChrome(ctx, client, lock.DownloadURL, archivePath, lock.ArchiveSHA256); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("archive_integrity", err)
	}
	extractDir := filepath.Join(stagingDir, chromeForTestingExtractedName)
	if err := os.Mkdir(extractDir, 0o700); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("archive_layout", err)
	}
	if err := extractManagedChromeArchive(archivePath, extractDir); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("archive_layout", err)
	}
	executablePath := filepath.Join(extractDir, filepath.FromSlash(lock.ExecutableRelative))
	if err := checkChromeExecutable(executablePath); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("executable_unavailable", err)
	}
	version, err := boundedVersionQuery(ctx, queryChromeVersion, executablePath, defaultChromeVersionTimeout)
	if err != nil {
		return ChromeExecutable{}, newChromeForTestingError("version_unverified", err)
	}
	major, err := ParseChromeMajorVersion(version)
	if err != nil || major < requiredMajor || !strings.Contains(version, lock.Version) {
		return ChromeExecutable{}, newChromeForTestingError("version_unverified", errors.New("extracted Chrome version does not match the verified lock"))
	}
	marker := chromeForTestingReadyMarker{
		Channel:            lock.Channel,
		Platform:           platform,
		Version:            lock.Version,
		Revision:           lock.Revision,
		ArchiveSHA256:      lock.ArchiveSHA256,
		ExecutableRelative: lock.ExecutableRelative,
	}
	markerBytes, err := json.Marshal(marker)
	if err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	markerTemp, err := os.CreateTemp(stagingDir, ".ready-*")
	if err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	markerTempName := markerTemp.Name()
	defer os.Remove(markerTempName)
	if chmodErr := markerTemp.Chmod(0o600); chmodErr != nil {
		_ = markerTemp.Close()
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", chmodErr)
	}
	if _, err := markerTemp.Write(markerBytes); err != nil {
		_ = markerTemp.Close()
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	if err := markerTemp.Close(); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	if err := os.Rename(markerTempName, filepath.Join(stagingDir, chromeForTestingReadyName)); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	if err := os.Rename(stagingDir, finalDir); err != nil {
		return ChromeExecutable{}, newChromeForTestingError("cache_unavailable", err)
	}
	return ChromeExecutable{
		Path:    filepath.Join(finalDir, chromeForTestingExtractedName, filepath.FromSlash(lock.ExecutableRelative)),
		Version: strings.TrimSpace(version),
		Major:   major,
		Source:  ExecutableSourceChromeForTesting,
	}, nil
}

type chromeForTestingReadyMarker struct {
	Channel            string `json:"channel"`
	Platform           string `json:"platform"`
	Version            string `json:"version"`
	Revision           string `json:"revision"`
	ArchiveSHA256      string `json:"archive_sha256"`
	ExecutableRelative string `json:"executable"`
}

func readReadyChromeCache(ctx context.Context, finalDir string, lock ChromeForTestingLock, requiredMajor int) (ChromeExecutable, bool) {
	data, err := os.ReadFile(filepath.Join(finalDir, chromeForTestingReadyName))
	if err != nil || len(data) > chromeForTestingReadyLimit {
		return ChromeExecutable{}, false
	}
	var marker chromeForTestingReadyMarker
	if json.Unmarshal(data, &marker) != nil || marker.Channel != lock.Channel || marker.Platform != lock.Platform || marker.Version != lock.Version || marker.Revision != lock.Revision || marker.ArchiveSHA256 != lock.ArchiveSHA256 || marker.ExecutableRelative != lock.ExecutableRelative {
		return ChromeExecutable{}, false
	}
	archivePath := filepath.Join(finalDir, chromeForTestingArchiveName)
	if !verifyManagedChromeArchive(archivePath, lock.ArchiveSHA256) {
		return ChromeExecutable{}, false
	}
	executable := filepath.Join(finalDir, chromeForTestingExtractedName, filepath.FromSlash(lock.ExecutableRelative))
	if err := checkChromeExecutable(executable); err != nil {
		return ChromeExecutable{}, false
	}
	version, err := boundedVersionQuery(ctx, queryChromeVersion, executable, defaultChromeVersionTimeout)
	if err != nil || !strings.Contains(version, lock.Version) {
		return ChromeExecutable{}, false
	}
	major, err := ParseChromeMajorVersion(version)
	if err != nil || major < requiredMajor {
		return ChromeExecutable{}, false
	}
	return ChromeExecutable{Path: executable, Version: strings.TrimSpace(version), Major: major, Source: ExecutableSourceChromeForTesting}, true
}

func acquireChromeForTestingLock(ctx context.Context, lockPath string) (func(), error) {
	if err := os.Mkdir(filepath.Dir(lockPath), 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > chromeForTestingLockStaleAfter {
			_ = os.Remove(lockPath)
			continue
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
}

func fetchManagedChromeManifest(ctx context.Context, client *http.Client, endpoint string) (ChromeForTestingManifest, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ChromeForTestingManifest{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return ChromeForTestingManifest{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ChromeForTestingManifest{}, fmt.Errorf("chrome for testing manifest returned HTTP status %d", response.StatusCode)
	}
	var manifest ChromeForTestingManifest
	if err := json.NewDecoder(io.LimitReader(response.Body, chromeForTestingManifestLimit)).Decode(&manifest); err != nil {
		return ChromeForTestingManifest{}, err
	}
	return manifest, nil
}

func downloadAndVerifyManagedChrome(ctx context.Context, client *http.Client, endpoint, destination, expectedSHA string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("chrome for testing archive returned HTTP status %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	bytesWritten, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, chromeForTestingArchiveLimit))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if bytesWritten >= chromeForTestingArchiveLimit {
		return errors.New("chrome for testing archive exceeds the 1 GiB safety bound")
	}
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if actualSHA != expectedSHA {
		return errors.New("chrome for testing archive digest does not match the lock")
	}
	return nil
}

func verifyManagedChromeArchive(archivePath, expectedSHA string) bool {
	file, err := os.Open(archivePath)
	if err != nil {
		return false
	}
	defer file.Close()
	hasher := sha256.New()
	bytesRead, err := io.Copy(io.MultiWriter(hasher), io.LimitReader(file, chromeForTestingArchiveLimit))
	if err != nil || bytesRead >= chromeForTestingArchiveLimit {
		return false
	}
	return hex.EncodeToString(hasher.Sum(nil)) == expectedSHA
}

func extractManagedChromeArchive(archivePath, destination string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	var symlinks []*zip.File
	for _, entry := range archive.File {
		name, err := validateChromeArchivePathValue(entry.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if entry.Mode()&os.ModeSymlink != 0 {
			symlinks = append(symlinks, entry)
			continue
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		file, createErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if createErr == nil {
			_, createErr = io.Copy(file, reader)
			closeErr := file.Close()
			if createErr == nil {
				createErr = closeErr
			}
		}
		_ = reader.Close()
		if createErr != nil {
			return createErr
		}
		mode := entry.Mode().Perm()
		if mode == 0 {
			mode = 0o600
		}
		if err := os.Chmod(target, mode); err != nil {
			return err
		}
	}
	for _, entry := range symlinks {
		name, err := validateChromeArchivePathValue(entry.Name)
		if err != nil {
			return err
		}
		linkPath := filepath.Join(destination, filepath.FromSlash(name))
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		linkTargetBytes, readErr := io.ReadAll(io.LimitReader(reader, 4096))
		closeErr := reader.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		linkTarget := strings.TrimSpace(string(linkTargetBytes))
		if linkTarget == "" || filepath.IsAbs(filepath.FromSlash(linkTarget)) {
			return errors.New("chrome archive symlink target is unsafe")
		}
		resolvedTarget := filepath.Clean(filepath.Join(filepath.Dir(linkPath), filepath.FromSlash(linkTarget)))
		relativeTarget, err := filepath.Rel(destination, resolvedTarget)
		if err != nil || relativeTarget == ".." || strings.HasPrefix(relativeTarget, ".."+string(os.PathSeparator)) {
			return errors.New("chrome archive symlink escapes extraction directory")
		}
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
			return err
		}
		if err := os.Symlink(linkTarget, linkPath); err != nil {
			return err
		}
	}
	return nil
}

func validateChromeArchivePath(raw string) error {
	_, err := validateChromeArchivePathValue(raw)
	return err
}

func validateChromeArchivePathValue(raw string) (string, error) {
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("chrome archive path contains NUL")
	}
	normalized := strings.ReplaceAll(raw, "\\", "/")
	cleaned := path.Clean(normalized)
	converted := filepath.FromSlash(cleaned)
	if normalized == "" || normalized == "." || strings.HasPrefix(normalized, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") || filepath.IsAbs(converted) || filepath.VolumeName(converted) != "" {
		return "", errors.New("chrome archive contains an unsafe path")
	}
	return cleaned, nil
}

func validateOfficialChromeURL(raw, prefix string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasPrefix(raw, prefix) {
		return errors.New("chrome for testing source is not an official HTTPS URL")
	}
	return nil
}

var lowerHexDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func chromeForTestingCacheKey(platform string, lock ChromeForTestingLock) string {
	return strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(platform + "-" + lock.Version + "-" + lock.Revision)
}

func defaultChromeForTestingCacheDir() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "agent-cli")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "agent-cli")
	}
	return filepath.Join(os.TempDir(), "agent-cli-cache")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newChromeForTestingError(category string, cause error) error {
	return &ChromeForTestingError{Category: category, Cause: cause}
}

// ChromeForTestingError classifies an internal fallback failure without
// rendering URLs, paths, command output, or HTTP details.
type ChromeForTestingError struct {
	Category string
	Cause    error
}

func (e *ChromeForTestingError) Error() string {
	if e == nil {
		return "Chrome for Testing fallback failed"
	}
	category := safeAcquisitionCategory(e.Category)
	if category == "" {
		category = "acquisition_failed"
	}
	return "Chrome for Testing fallback failed: " + category
}

func (e *ChromeForTestingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func chromeSourceDirectory() (string, bool) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	return filepath.Dir(source), true
}

func findUpward(start, relative string) (string, bool) {
	start, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	if info, statErr := os.Stat(start); statErr == nil && !info.IsDir() {
		start = filepath.Dir(start)
	}
	for {
		candidate := filepath.Join(start, relative)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(start)
		if parent == start {
			return "", false
		}
		start = parent
	}
}
