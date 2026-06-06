package integration

import (
	"bytes"
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
