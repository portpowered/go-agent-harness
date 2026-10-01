package chrome

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	defaultEventBuffer    = 256
	defaultCommandTimeout = 15 * time.Second
)

// RuntimeOptions configures the adapter without exposing a browser protocol
// type. A zero value is valid and receives safe production defaults.
type RuntimeOptions struct {
	EventBuffer    int
	CommandTimeout time.Duration
	HTTPClient     *http.Client
	WireTrace      webmcp.WireTraceSink
}

// Option customizes a Runtime.
type Option func(*RuntimeOptions)

func WithEventBuffer(size int) Option {
	return func(options *RuntimeOptions) {
		if size > 0 {
			options.EventBuffer = size
		}
	}
}

func WithCommandTimeout(timeout time.Duration) Option {
	return func(options *RuntimeOptions) {
		if timeout > 0 {
			options.CommandTimeout = timeout
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(options *RuntimeOptions) {
		if client != nil {
			options.HTTPClient = client
		}
	}
}

// WithWireTraceSink records safe target/session and CDP method evidence at
// the command boundary. The sink never receives endpoint, input, or output
// values from the adapter.
func WithWireTraceSink(sink webmcp.WireTraceSink) Option {
	return func(options *RuntimeOptions) {
		if sink != nil {
			options.WireTrace = sink
		}
	}
}

func defaultRuntimeOptions() RuntimeOptions {
	return RuntimeOptions{
		EventBuffer:    defaultEventBuffer,
		CommandTimeout: defaultCommandTimeout,
		HTTPClient:     http.DefaultClient,
	}
}

func (h *handle) timeout() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.commandTimeout > 0 {
		return h.commandTimeout
	}
	return defaultCommandTimeout
}

func stringSetContains(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}

func normalizedManagedShutdown(timeout time.Duration) time.Duration {
	return durationOrDefault(timeout, defaultManagedBrowserShutdownTimeout)
}

// durationOrDefault returns value when it is positive and fallback otherwise.
// Unset timing fields use it to fall back to their production defaults.
func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// versionQueryOrDefault returns query, or the executable --version query when
// query is nil.
func versionQueryOrDefault(query VersionQuery) VersionQuery {
	if query == nil {
		return queryChromeVersion
	}
	return query
}

// discardCleanupError runs a release on an abandon or cleanup path whose
// outcome is already decided. The resource is dropped either way, so its
// release error cannot change the result reported to the caller.
func discardCleanupError(release func() error) {
	if err := release(); err != nil {
		return
	}
}

// recordManagedBrowserLeaseOwner writes this process's PID into a newly
// created lease. A lease whose owner cannot be recorded is removed at once so
// stale detection never has to guess at its holder.
func recordManagedBrowserLeaseOwner(path string, file *os.File) (*managedBrowserLease, error) {
	_, writeErr := io.WriteString(file, strconv.Itoa(os.Getpid()))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		removeBestEffort(os.Remove, path)
		return nil, err
	}
	return &managedBrowserLease{path: path}, nil
}

// proveManagedBrowserProfileOwner reports whether pid is provably the managed
// browser that owns profileDir. Any inspection failure is a failed proof.
func proveManagedBrowserProfileOwner(ctx context.Context, inspector ManagedBrowserProcessInspector, pid int, profileDir string) (ManagedBrowserState, bool) {
	commandLine, err := managedProcessCommandLine(ctx, pid)
	if err != nil {
		return ManagedBrowserState{}, false
	}
	state, ok := managedBrowserProfileOwnerState(pid, profileDir, commandLine)
	if !ok {
		return ManagedBrowserState{}, false
	}
	identity, err := managedProcessIdentity(ctx, pid, commandLine)
	if err != nil || strings.TrimSpace(identity) == "" {
		return ManagedBrowserState{}, false
	}
	state.ProcessIdentity = identity
	_, err = inspector.Inspect(ctx, state)
	return state, err == nil
}

// positivePIDOrZero parses a singleton lock PID suffix. A suffix that is not a
// positive integer attributes the lock to no process.
func positivePIDOrZero(text string) int {
	pid, err := strconv.Atoi(text)
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func defaultChromeForTestingCacheDir() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "agent-cli")
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

func extractManagedChromeSymlink(entry *zip.File, destination string) error {
	name, err := validateChromeArchivePathValue(entry.Name)
	if err != nil {
		return err
	}
	linkPath := filepath.Join(destination, filepath.FromSlash(name))
	reader, err := entry.Open()
	if err != nil {
		return err
	}
	linkTargetBytes, readErr := io.ReadAll(io.LimitReader(reader, chromeArchiveSymlinkTargetLimit))
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
	if err := os.MkdirAll(filepath.Dir(linkPath), chromeArchiveDirMode); err != nil {
		return err
	}
	return os.Symlink(linkTarget, linkPath)
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
