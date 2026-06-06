package logging

// This is a placeholder for logging functionality.
// Users can inject their own logging implementation by implementing the Logger interface.

type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)
	Panic(msg string, fields ...Field)
}

type Field struct {
	Key   string
	Value any
}

// DummyLogger is a logger that does nothing.
func DummyLogger() Logger {
	return &dummyLogger{}
}

type dummyLogger struct {
}

func (d *dummyLogger) Debug(msg string, fields ...Field) {
}

func (d *dummyLogger) Info(msg string, fields ...Field) {
}

func (d *dummyLogger) Warn(msg string, fields ...Field) {
}

func (d *dummyLogger) Error(msg string, fields ...Field) {
}

func (d *dummyLogger) Fatal(msg string, fields ...Field) {
}

func (d *dummyLogger) Panic(msg string, fields ...Field) {
}

// Ensure dummyLogger implements Logger.
var _ Logger = (*dummyLogger)(nil)
