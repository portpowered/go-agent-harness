package logger

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	agentlooplogging "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	gatewaylogging "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// updateGolden is intentionally opt-in: ordinary test runs compare against
// committed goldens and never rewrite them. Use `go test -update` when a
// deliberate formatting change needs a refreshed golden.
var updateGolden = flag.Bool("update", false, "update logger golden files")

var (
	ansiSequence = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	timestamp    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{4}|[+-]\d{2}:\d{2})`)
	caller       = regexp.MustCompile(`[A-Za-z0-9_./\\-]+\.go:\d+`)
)

// recordingWriteSyncer is an injected sink used to observe exact writes and
// to return a typed error without touching the real process destinations.
type recordingWriteSyncer struct {
	mu         sync.Mutex
	writes     [][]byte
	writeError error
	errors     []error
}

func (s *recordingWriteSyncer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, append([]byte(nil), p...))
	s.errors = append(s.errors, s.writeError)
	return len(p), s.writeError
}

func (s *recordingWriteSyncer) Sync() error { return nil }

func (s *recordingWriteSyncer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, write := range s.writes {
		b.Write(write)
	}
	return b.String()
}

func (s *recordingWriteSyncer) WriteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.writes)
}

func withConsoleWriteSyncer(t *testing.T, factory func() zapcore.WriteSyncer) {
	t.Helper()
	previous := newConsoleWriteSyncer
	newConsoleWriteSyncer = factory
	t.Cleanup(func() { newConsoleWriteSyncer = previous })
}

func readAgentLog(t *testing.T, configDir string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(configDir, "agent.log"))
	if err != nil {
		t.Fatalf("read agent.log: %v", err)
	}
	return string(contents)
}

func normalizeFormattedLog(output string) string {
	output = strings.ReplaceAll(output, "\r\n", "\n")
	lines := strings.Split(output, "\n")
	normalized := make([]string, 0, len(lines))
	inErrorStack := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		if inErrorStack {
			if timestamp.MatchString(line) {
				inErrorStack = false
			} else {
				// AddStacktrace emits caller/function frames on following lines;
				// those are nondeterministic and are intentionally omitted.
				continue
			}
		}
		line = ansiSequence.ReplaceAllString(line, "")
		line = timestamp.ReplaceAllString(line, "<timestamp>")
		line = caller.ReplaceAllString(line, "<caller>")
		normalized = append(normalized, line)
		if strings.Contains(line, "\terror record\t") {
			inErrorStack = true
		}
	}
	return strings.Join(normalized, "\n") + "\n"
}

func assertMessagesInOrder(t *testing.T, output string, messages ...string) {
	t.Helper()
	previous := -1
	for _, message := range messages {
		position := strings.Index(output, message)
		if position < 0 {
			t.Fatalf("expected log output to contain %q; output=%q", message, output)
		}
		if position <= previous {
			t.Fatalf("expected %q after the previous message; output=%q", message, output)
		}
		previous = position
	}
}

func TestS3FormattedRecordsMatchGolden(t *testing.T) {
	configDir := t.TempDir()
	logger, closer, err := NewLoggerWithCloser(LoggerConfig{
		VerbosityLevel: 2,
		ConfigDir:      configDir,
	})
	if err != nil {
		t.Fatalf("create file logger: %v", err)
	}
	if closer == nil {
		t.Fatal("file logger must return a closer")
	}

	logger.Debug("debug record", zap.String("kind", "debug"), zap.Int("sequence", 1))
	logger.Info("info record", zap.String("kind", "info"), zap.Int("sequence", 2))
	logger.Warn("warn record", zap.String("kind", "warn"), zap.Int("sequence", 3))
	logger.Error("error record", zap.String("kind", "error"), zap.Int("sequence", 4))
	if err := closer.Close(); err != nil {
		t.Fatalf("close file logger: %v", err)
	}

	actual := normalizeFormattedLog(readAgentLog(t, configDir))
	goldenPath := filepath.Join("testdata", "formatted.golden")
	want, err := os.ReadFile(goldenPath)
	if err != nil && (!*updateGolden || !os.IsNotExist(err)) {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}
	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(actual), 0644); err != nil {
			t.Fatalf("update golden %s: %v", goldenPath, err)
		}
		want = []byte(actual)
	}
	if string(want) != actual {
		t.Fatalf("formatted log differs from %s\nwant:\n%s\ngot:\n%s", goldenPath, want, actual)
	}
	assertMessagesInOrder(t, actual, "debug record", "info record", "warn record", "error record")
}

func TestS5LoggerRoutingAndSinkEffects(t *testing.T) {
	t.Run("file routing", func(t *testing.T) {
		consoleOut := &recordingWriteSyncer{}
		consoleErr := &recordingWriteSyncer{}
		withConsoleWriteSyncer(t, func() zapcore.WriteSyncer {
			return zapcore.NewMultiWriteSyncer(consoleOut, consoleErr)
		})

		configDir := t.TempDir()
		logger, closer, err := NewLoggerWithCloser(LoggerConfig{
			VerbosityLevel: 1,
			ConfigDir:      configDir,
		})
		if err != nil {
			t.Fatalf("create file logger: %v", err)
		}
		logger.Info("file first")
		logger.Warn("file second")
		if closer == nil {
			t.Fatal("file routing must return a closer")
		}
		if consoleOut.WriteCount() != 0 || consoleErr.WriteCount() != 0 {
			t.Fatalf("file routing wrote to console sinks: stdout=%d stderr=%d", consoleOut.WriteCount(), consoleErr.WriteCount())
		}
		if err := closer.Close(); err != nil {
			t.Fatalf("close file logger: %v", err)
		}

		output := readAgentLog(t, configDir)
		assertMessagesInOrder(t, output, "file first", "file second")
		if err := os.Rename(filepath.Join(configDir, "agent.log"), filepath.Join(configDir, "agent.log.closed")); err != nil {
			t.Fatalf("rename closed agent.log to prove the handle was released: %v", err)
		}
	})

	t.Run("console routing", func(t *testing.T) {
		stdout := &recordingWriteSyncer{}
		stderr := &recordingWriteSyncer{}
		withConsoleWriteSyncer(t, func() zapcore.WriteSyncer {
			return zapcore.NewMultiWriteSyncer(stdout, stderr)
		})

		configDir := t.TempDir()
		logger, closer, err := NewLoggerWithCloser(LoggerConfig{
			VerbosityLevel: 1,
			ConfigDir:      configDir,
			LogToStdout:    true,
		})
		if err != nil {
			t.Fatalf("create console logger: %v", err)
		}
		if closer != nil {
			t.Fatal("console routing must not return a file closer")
		}
		logger.Info("console first")
		logger.Warn("console second")
		if err := logger.Sync(); err != nil {
			t.Fatalf("sync console logger: %v", err)
		}
		for name, sink := range map[string]*recordingWriteSyncer{"stdout": stdout, "stderr": stderr} {
			output := sink.String()
			assertMessagesInOrder(t, output, "console first", "console second")
			if sink.WriteCount() != 2 {
				t.Fatalf("%s received %d writes, want 2", name, sink.WriteCount())
			}
		}
		if _, err := os.Stat(filepath.Join(configDir, "agent.log")); !os.IsNotExist(err) {
			t.Fatalf("console routing created agent.log: stat error=%v", err)
		}
	})
}

func TestS5LoggerLevelThresholds(t *testing.T) {
	tests := []struct {
		name       string
		verbosity  int
		allowed    []string
		suppressed []string
	}{
		{name: "errors only", verbosity: 0, allowed: []string{"threshold error"}, suppressed: []string{"threshold debug", "threshold info", "threshold warn"}},
		{name: "info and above", verbosity: 1, allowed: []string{"threshold info", "threshold warn", "threshold error"}, suppressed: []string{"threshold debug"}},
		{name: "debug and above", verbosity: 2, allowed: []string{"threshold debug", "threshold info", "threshold warn", "threshold error"}, suppressed: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			logger, closer, err := NewLoggerWithCloser(LoggerConfig{
				VerbosityLevel: test.verbosity,
				ConfigDir:      configDir,
			})
			if err != nil {
				t.Fatalf("create logger: %v", err)
			}
			logger.Debug("threshold debug")
			logger.Info("threshold info")
			logger.Warn("threshold warn")
			logger.Error("threshold error")
			if closer == nil {
				t.Fatal("file logger must return a closer")
			}
			if err := closer.Close(); err != nil {
				t.Fatalf("close logger: %v", err)
			}

			output := readAgentLog(t, configDir)
			for _, message := range test.allowed {
				if count := strings.Count(output, message); count != 1 {
					t.Errorf("allowed message %q occurred %d times, want exactly 1; output=%q", message, count, output)
				}
			}
			for _, message := range test.suppressed {
				if count := strings.Count(output, message); count != 0 {
					t.Errorf("below-threshold message %q occurred %d times, want exactly 0; output=%q", message, count, output)
				}
			}
		})
	}
}

func TestS5LoggerSinkErrorsDoNotSuppressLaterWrites(t *testing.T) {
	sentinel := errors.New("sentinel sink failure")
	sink := &recordingWriteSyncer{writeError: sentinel}
	errorOutput := &recordingWriteSyncer{}
	processStderrPath := filepath.Join(t.TempDir(), "process-stderr")
	processStderr, err := os.Create(processStderrPath)
	if err != nil {
		t.Fatalf("create process stderr capture: %v", err)
	}
	previousStderr := os.Stderr
	os.Stderr = processStderr
	t.Cleanup(func() {
		os.Stderr = previousStderr
		_ = processStderr.Close()
	})
	withConsoleWriteSyncer(t, func() zapcore.WriteSyncer { return sink })

	logger, closer, err := NewLoggerWithCloser(LoggerConfig{
		VerbosityLevel: 2,
		ConfigDir:      t.TempDir(),
		LogToStdout:    true,
	})
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	if closer != nil {
		t.Fatal("console logger must not return a closer")
	}
	logger = logger.WithOptions(zap.ErrorOutput(errorOutput))
	logger.Info("failed first")
	logger.Info("attempted second")

	if got := sink.WriteCount(); got != 2 {
		t.Fatalf("sink received %d writes after the first error, want 2", got)
	}
	if len(sink.errors) != 2 || sink.errors[0] != sentinel || sink.errors[1] != sentinel {
		t.Fatalf("sink did not return the sentinel error for both attempts: %#v", sink.errors)
	}
	output := sink.String()
	assertMessagesInOrder(t, output, "failed first", "attempted second")
	if count := strings.Count(errorOutput.String(), "write error: sentinel sink failure"); count != 2 {
		t.Fatalf("injected error output received %d reports, want 2; output=%q", count, errorOutput.String())
	}
	if err := processStderr.Close(); err != nil {
		t.Fatalf("close process stderr capture: %v", err)
	}
	os.Stderr = previousStderr
	if output, err := os.ReadFile(processStderrPath); err != nil {
		t.Fatalf("read process stderr capture: %v", err)
	} else if len(output) != 0 {
		t.Fatalf("real process stderr received unexpected output: %q", output)
	}
}

func TestS5LoggerRotationIsSkippedWhenTheDefectIsAbsent(t *testing.T) {
	t.Skip("defect: current logger has no rotation implementation or injectable clock seam; the coverage lane must not invent production rotation")
}

func TestLoggerContextRequestIDAndDefaultBehavior(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	base := zap.New(core)
	previous := log
	log = base
	t.Cleanup(func() { log = previous })

	requestContext := context.WithValue(context.Background(), REQUEST_ID, "request-123")
	NewRequestLogger(requestContext).Info("request-scoped", zap.String("scope", "request"))
	entries := observed.FilterMessage("request-scoped").All()
	if len(entries) != 1 || entries[0].ContextMap()["request_id"] != "request-123" {
		t.Fatalf("request ID was not emitted in the request-scoped record: %#v", entries)
	}

	NewRequestLogger(context.Background()).Info("request-without-id")
	entries = observed.FilterMessage("request-without-id").All()
	if len(entries) != 1 {
		t.Fatalf("expected one no-ID record, got %d", len(entries))
	}
	if _, ok := entries[0].ContextMap()["request_id"]; ok {
		t.Fatal("request ID unexpectedly appeared when the context had no request ID")
	}

	WithRequestID("explicit-456").Info("explicit-request")
	entries = observed.FilterMessage("explicit-request").All()
	if len(entries) != 1 || entries[0].ContextMap()["request_id"] != "explicit-456" {
		t.Fatalf("explicit request ID was not emitted: %#v", entries)
	}
	WithRequestID("").Info("empty-request")
	entries = observed.FilterMessage("empty-request").All()
	if len(entries) != 1 {
		t.Fatalf("expected one empty-ID record, got %d", len(entries))
	}
	if _, ok := entries[0].ContextMap()["request_id"]; ok {
		t.Fatal("empty request ID unexpectedly appeared in the record")
	}

	contextLoggerCore, contextObserved := observer.New(zapcore.DebugLevel)
	contextLogger := zap.New(contextLoggerCore)
	ctx := WithLogger(context.Background(), contextLogger)
	GetRequestLoggerFromContext(ctx).Info("context logger", zap.String("scope", "attached"))
	if entries := contextObserved.FilterMessage("context logger").All(); len(entries) != 1 {
		t.Fatalf("attached context logger emitted %d records, want 1", len(entries))
	}
	GetRequestLoggerFromContext(context.Background()).Info("context default")
	if entries := observed.FilterMessage("context default").All(); len(entries) != 1 {
		t.Fatalf("missing context logger did not use the default logger: got %d records", len(entries))
	}

	if got := GetRequestID(context.WithValue(context.Background(), REQUEST_ID, 42)); got != "" {
		t.Fatalf("wrongly typed request ID returned %q", got)
	}
	log = nil
	GetRequestLoggerFromContext(context.Background()).Info("default noop")
	if entries := observed.FilterMessage("default noop").All(); len(entries) != 0 {
		t.Fatalf("no-op default logger emitted %d records", len(entries))
	}
}

func TestLoggerConstructorsAndConstructionErrors(t *testing.T) {
	console := &recordingWriteSyncer{}
	withConsoleWriteSyncer(t, func() zapcore.WriteSyncer { return console })

	logger, err := NewLogger(LoggerConfig{VerbosityLevel: 2, LogToStdout: true})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	logger.Debug("new logger")
	if err := logger.Sync(); err != nil {
		t.Fatalf("sync NewLogger: %v", err)
	}

	verbose, closer := NewVerboseLoggerWithCloser(1, "", true)
	if closer != nil {
		t.Fatal("verbose console logger returned a closer")
	}
	verbose.Info("verbose logger")
	if err := verbose.Sync(); err != nil {
		t.Fatalf("sync verbose logger: %v", err)
	}
	NewVerboseLogger(2, "", true).Debug("wrapper logger")

	badParent := t.TempDir()
	notDirectory := filepath.Join(badParent, "not-directory")
	if err := os.WriteFile(notDirectory, []byte("file"), 0644); err != nil {
		t.Fatalf("create invalid config parent: %v", err)
	}
	if _, closer, err := NewLoggerWithCloser(LoggerConfig{ConfigDir: notDirectory}); err == nil || closer != nil {
		t.Fatalf("expected MkdirAll failure for file config path, err=%v closer=%v", err, closer)
	}

	configDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(configDir, "agent.log"), 0755); err != nil {
		t.Fatalf("create agent.log directory: %v", err)
	}
	if _, closer, err := NewLoggerWithCloser(LoggerConfig{ConfigDir: configDir}); err == nil || closer != nil {
		t.Fatalf("expected OpenFile failure for directory agent.log, err=%v closer=%v", err, closer)
	}

	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	defaultLogger := NewDefaultLogger()
	defaultLogger.Info("default fallback")
	if defaultLogger.Check(zap.InfoLevel, "default fallback") != nil {
		t.Fatal("default logger fallback should be a no-op when the home directory is unavailable")
	}
}

func TestS5AdaptersPreserveLevelsMessagesAndFields(t *testing.T) {
	t.Run("agent loop", func(t *testing.T) {
		core, observed := observer.New(zapcore.DebugLevel)
		adapter := NewZapAgentLoopAdapter(zap.New(core))
		calls := []struct {
			name string
			call func(string, ...agentlooplogging.Field)
		}{
			{name: "debug", call: adapter.Debug},
			{name: "info", call: adapter.Info},
			{name: "warn", call: adapter.Warn},
			{name: "error", call: adapter.Error},
		}
		wantLevels := []zapcore.Level{zap.DebugLevel, zap.InfoLevel, zap.WarnLevel, zap.ErrorLevel}
		for index, call := range calls {
			call.call(fmt.Sprintf("agent %s", call.name), agentlooplogging.Field{Key: "adapter", Value: "agent-loop"}, agentlooplogging.Field{Key: "index", Value: index})
		}
		entries := observed.All()
		if len(entries) != len(calls) {
			t.Fatalf("got %d agent-loop records, want %d", len(entries), len(calls))
		}
		for index, entry := range entries {
			if entry.Level != wantLevels[index] || entry.Message != fmt.Sprintf("agent %s", calls[index].name) {
				t.Errorf("record %d = level %s message %q, want %s %q", index, entry.Level, entry.Message, wantLevels[index], fmt.Sprintf("agent %s", calls[index].name))
			}
			fields := entry.ContextMap()
			if fields["adapter"] != "agent-loop" || fmt.Sprint(fields["index"]) != fmt.Sprint(index) {
				t.Errorf("record %d fields = %#v", index, fields)
			}
		}

		nilAdapter := NewZapAgentLoopAdapter(nil)
		nilAdapter.Debug("nil debug")
		nilAdapter.Info("nil info")
		nilAdapter.Warn("nil warn")
		nilAdapter.Error("nil error")
	})

	t.Run("gateway", func(t *testing.T) {
		core, observed := observer.New(zapcore.DebugLevel)
		adapter := NewZapGatewayAdapter(zap.New(core))
		calls := []struct {
			name string
			call func(string, ...gatewaylogging.Field)
		}{
			{name: "debug", call: adapter.Debug},
			{name: "info", call: adapter.Info},
			{name: "warn", call: adapter.Warn},
			{name: "error", call: adapter.Error},
		}
		wantLevels := []zapcore.Level{zap.DebugLevel, zap.InfoLevel, zap.WarnLevel, zap.ErrorLevel}
		for index, call := range calls {
			call.call(fmt.Sprintf("gateway %s", call.name), gatewaylogging.Field{Key: "adapter", Value: "gateway"}, gatewaylogging.Field{Key: "index", Value: index})
		}
		entries := observed.All()
		if len(entries) != len(calls) {
			t.Fatalf("got %d gateway records, want %d", len(entries), len(calls))
		}
		for index, entry := range entries {
			if entry.Level != wantLevels[index] || entry.Message != fmt.Sprintf("gateway %s", calls[index].name) {
				t.Errorf("record %d = level %s message %q, want %s %q", index, entry.Level, entry.Message, wantLevels[index], fmt.Sprintf("gateway %s", calls[index].name))
			}
			fields := entry.ContextMap()
			if fields["adapter"] != "gateway" || fmt.Sprint(fields["index"]) != fmt.Sprint(index) {
				t.Errorf("record %d fields = %#v", index, fields)
			}
		}

		nilAdapter := NewZapGatewayAdapter(nil)
		nilAdapter.Debug("nil debug")
		nilAdapter.Info("nil info")
		nilAdapter.Warn("nil warn")
		nilAdapter.Error("nil error")
	})
}

func TestAdaptersTerminalMethodsInSubprocess(t *testing.T) {
	const helperEnv = "GO_AGENT_LOGGER_TERMINAL_HELPER"
	if mode := os.Getenv(helperEnv); mode != "" {
		runLoggerTerminalHelper(mode)
		return
	}

	tests := []struct {
		name    string
		mode    string
		message string
		field   string
	}{
		{name: "agent fatal", mode: "agent-fatal", message: "agent fatal", field: `"terminal": "agent"`},
		{name: "agent panic", mode: "agent-panic", message: "agent panic", field: `"terminal": "agent"`},
		{name: "gateway fatal", mode: "gateway-fatal", message: "gateway fatal", field: `"terminal": "gateway"`},
		{name: "gateway panic", mode: "gateway-panic", message: "gateway panic", field: `"terminal": "gateway"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestAdaptersTerminalMethodsInSubprocess$", "-test.count=1")
			cmd.Env = append(os.Environ(), helperEnv+"="+test.mode)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("terminal helper exited successfully; output=%q", output)
			}
			if !bytes.Contains(output, []byte(test.message)) || !bytes.Contains(output, []byte(test.field)) {
				t.Fatalf("terminal helper output=%q, want message %q and field %q", output, test.message, test.field)
			}
		})
	}
}

func runLoggerTerminalHelper(mode string) {
	terminalLogger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	switch mode {
	case "agent-fatal":
		NewZapAgentLoopAdapter(terminalLogger).Fatal("agent fatal", agentlooplogging.Field{Key: "terminal", Value: "agent"})
	case "agent-panic":
		NewZapAgentLoopAdapter(terminalLogger).Panic("agent panic", agentlooplogging.Field{Key: "terminal", Value: "agent"})
	case "gateway-fatal":
		NewZapGatewayAdapter(terminalLogger).Fatal("gateway fatal", gatewaylogging.Field{Key: "terminal", Value: "gateway"})
	case "gateway-panic":
		NewZapGatewayAdapter(terminalLogger).Panic("gateway panic", gatewaylogging.Field{Key: "terminal", Value: "gateway"})
	default:
		panic("unknown logger terminal helper mode: " + mode)
	}
}
