package chrome

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
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
	if timeout <= 0 {
		return defaultManagedBrowserShutdownTimeout
	}
	return timeout
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
