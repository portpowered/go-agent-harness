package test_logging

import (
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
)

// ---------------------------------------------------------------------------
// PrintLogger (test debugging)
// ---------------------------------------------------------------------------

// PrintLogger implements logging.Logger by writing level, message, and fields
// to stdout via fmt.Println. Use it in functional tests to see agent-loop
// logs when debugging (e.g. go test -v ./test/functional/...).
type PrintLogger struct{}

// NewPrintLogger returns a logger that prints to stdout. Safe for use from
// multiple goroutines for debugging; not intended for production.
func NewPrintLogger() *PrintLogger {
	return &PrintLogger{}
}

func (p *PrintLogger) format(level, msg string, fields ...logging.Field) string {
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

func (p *PrintLogger) Debug(msg string, fields ...logging.Field) {
	fmt.Println(p.format("DEBUG", msg, fields...))
}

func (p *PrintLogger) Info(msg string, fields ...logging.Field) {
	fmt.Println(p.format("INFO", msg, fields...))
}

func (p *PrintLogger) Warn(msg string, fields ...logging.Field) {
	fmt.Println(p.format("WARN", msg, fields...))
}

func (p *PrintLogger) Error(msg string, fields ...logging.Field) {
	fmt.Println(p.format("ERROR", msg, fields...))
}

func (p *PrintLogger) Fatal(msg string, fields ...logging.Field) {
	fmt.Println(p.format("FATAL", msg, fields...))
}

func (p *PrintLogger) Panic(msg string, fields ...logging.Field) {
	s := p.format("PANIC", msg, fields...)
	fmt.Println(s)
	panic(s)
}

// Ensure PrintLogger implements logging.Logger.
var _ logging.Logger = (*PrintLogger)(nil)
