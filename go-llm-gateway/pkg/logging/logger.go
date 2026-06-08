package logging

// Logger is the gateway-owned logging seam for provider code.
//
// Providers accept an optional logger through their option surfaces, but remain
// safe to construct with the no-op default returned by DummyLogger.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)
	Panic(msg string, fields ...Field)
}

// Field carries structured logging metadata across the provider seam.
type Field struct {
	Key   string
	Value any
}

// DummyLogger returns a no-op logger for default-safe provider construction.
func DummyLogger() Logger {
	return dummyLogger{}
}

type dummyLogger struct{}

func (dummyLogger) Debug(string, ...Field) {}
func (dummyLogger) Info(string, ...Field)  {}
func (dummyLogger) Warn(string, ...Field)  {}
func (dummyLogger) Error(string, ...Field) {}
func (dummyLogger) Fatal(string, ...Field) {}
func (dummyLogger) Panic(string, ...Field) {}

var _ Logger = dummyLogger{}
