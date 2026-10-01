package test_logging

import (
	"fmt"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
)

// ---------------------------------------------------------------------------
// TestLogger (test debugging)
// ---------------------------------------------------------------------------

// TestLogger implements logging.Logger by writing level, message, and fields
// to the test log. Use it in functional tests to see agent-loop logs when
// debugging (e.g. go test -v ./test/functional/...). A Panic-level entry
// fails the test instead of crashing the test binary.
type TestLogger struct {
	tb testing.TB
}

// NewTestLogger returns a logger that writes to tb's log. It is safe for use
// from multiple goroutines while the test runs.
func NewTestLogger(tb testing.TB) *TestLogger {
	tb.Helper()
	return &TestLogger{tb: tb}
}

func (p *TestLogger) format(level, msg string, fields ...logging.Field) string {
	if len(fields) == 0 {
		return fmt.Sprintf("[%s] %s", level, msg)
	}
	parts := make([]string, 0, len(fields)+1)
	parts = append(parts, fmt.Sprintf("[%s] %s", level, msg))
	for _, f := range fields {
		parts = append(parts, fmt.Sprintf("%s=%v", f.Key, f.Value))
	}
	return strings.Join(parts, " ")
}

func (p *TestLogger) Debug(msg string, fields ...logging.Field) {
	p.tb.Log(p.format("DEBUG", msg, fields...))
}

func (p *TestLogger) Info(msg string, fields ...logging.Field) {
	p.tb.Log(p.format("INFO", msg, fields...))
}

func (p *TestLogger) Warn(msg string, fields ...logging.Field) {
	p.tb.Log(p.format("WARN", msg, fields...))
}

func (p *TestLogger) Error(msg string, fields ...logging.Field) {
	p.tb.Log(p.format("ERROR", msg, fields...))
}

func (p *TestLogger) Fatal(msg string, fields ...logging.Field) {
	p.tb.Log(p.format("FATAL", msg, fields...))
}

// Panic records a panic-level entry as a test failure.
func (p *TestLogger) Panic(msg string, fields ...logging.Field) {
	p.tb.Error(p.format("PANIC", msg, fields...))
}

// Ensure TestLogger implements logging.Logger.
var _ logging.Logger = (*TestLogger)(nil)
