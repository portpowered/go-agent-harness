package integration

import (
	"bytes"
	"fmt"
	"io"
)

// testWriter captures stdout and stderr for assertion in tests.
type testWriter struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func (w *testWriter) Stdout() io.Writer {
	return &w.stdout
}

func (w *testWriter) Stderr() io.Writer {
	return &w.stderr
}

func (w *testWriter) StdoutString() string {
	return w.stdout.String()
}

func (w *testWriter) StderrString() string {
	return w.stderr.String()
}

func (w *testWriter) Reset() {
	w.stdout.Reset()
	w.stderr.Reset()
}

// NewTestWriter returns a writer that captures stdout and stderr.
func NewTestWriter() *testWriter {
	return &testWriter{
		stdout: bytes.Buffer{},
		stderr: bytes.Buffer{},
	}
}

// writeSimulatedMainError mirrors cmd/agent/main.go's own error rendering
// exactly: fmt.Fprintf(os.Stderr, "Error: %s\n", err) for any error
// Execute() returns. A test in this package that builds the real root
// command tree and calls rootCmd.ExecuteContext(...) in-process (rather
// than exec'ing the built binary) never runs main.go, so its captured
// stdout/stderr only reflects Cobra's own channel -- which cmd/agent's root
// command deliberately silences (SilenceErrors) to avoid printing every
// error twice (once from Cobra, once from main.go). Call this after such an
// in-process Execute() returns a non-nil error so the captured output still
// reflects what a real invocation actually prints, exactly once.
func writeSimulatedMainError(w io.Writer, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(w, "Error: %s\n", err)
}
