package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type cliTestTool struct {
	id       string
	result   []messages.Message
	err      error
	mu       sync.Mutex
	calls    int
	lastArgs map[string]any
}

func (t *cliTestTool) Name() string               { return t.id }
func (t *cliTestTool) Description() string        { return "test tool" }
func (t *cliTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *cliTestTool) Execute(_ context.Context, args map[string]any) ([]messages.Message, error) {
	t.mu.Lock()
	t.calls++
	t.lastArgs = make(map[string]any, len(args))
	for key, value := range args {
		t.lastArgs[key] = value
	}
	t.mu.Unlock()
	return t.result, t.err
}

type toolTestFailWriter struct{ err error }

func (w toolTestFailWriter) Write([]byte) (int, error) { return 0, w.err }

func newToolTestCommand(t *testing.T, registry *tools.ToolRegistry) *ToolCommand {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := NewToolCommand(globalFlags)
	command.registryLoader = func() (*tools.ToolRegistry, error) { return registry, nil }
	return command
}

func runToolTestCommand(t *testing.T, command *ToolCommand, args []string, out io.Writer) error {
	t.Helper()
	cmd := command.Generate()
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	return cmd.ExecuteContext(context.Background())
}

func TestToolCommandS2FlagMatrix(t *testing.T) {
	good := &cliTestTool{id: "capture", result: []messages.Message{messages.NewTextMessage(messages.RoleTool, "captured")}}
	listTool := &cliTestTool{id: "only-tool"}
	tests := []struct {
		name       string
		args       []string
		registry   *tools.ToolRegistry
		wantOutput string
		wantErr    string
		wantIs     error
	}{
		{name: "list mode", args: []string{"--list"}, registry: testToolRegistry(&cliTestTool{id: "zeta"}, listTool), wantOutput: "only-tool\nzeta\n"},
		{name: "missing tool id", args: nil, registry: testToolRegistry(), wantErr: "tool-id required", wantIs: errToolIDRequired},
		{name: "coerced key values", args: []string{"capture", "name='Ada Lovelace'", "count=7", "ratio=2.5", "enabled=true", "text=hello=world"}, registry: testToolRegistry(good), wantOutput: "captured"},
		{name: "malformed key value", args: []string{"capture", "broken"}, registry: testToolRegistry(good), wantErr: `invalid argument "broken": expected key=value`, wantIs: errToolArguments},
		{name: "empty key", args: []string{"capture", "=value"}, registry: testToolRegistry(good), wantErr: `invalid argument "=value": expected key=value`, wantIs: errToolArguments},
		{name: "trimmed empty key", args: []string{"capture", " =value"}, registry: testToolRegistry(good), wantErr: `invalid argument " =value": empty key`, wantIs: errToolArguments},
		{name: "list and invocation conflict", args: []string{"--list", "capture"}, registry: testToolRegistry(listTool), wantErr: "cannot combine --list with a tool id", wantIs: errToolFlagConflict},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			err := runToolTestCommand(t, newToolTestCommand(t, tc.registry), tc.args, stdout)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want message containing %q", err, tc.wantErr)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Fatalf("error = %v, want wrapped identity %v", err, tc.wantIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("execute tool: %v", err)
			}
			if got := stdout.String(); got != tc.wantOutput {
				t.Errorf("stdout = %q, want %q", got, tc.wantOutput)
			}
		})
	}

	good.mu.Lock()
	defer good.mu.Unlock()
	wantArgs := map[string]any{"name": "Ada Lovelace", "count": int64(7), "ratio": 2.5, "enabled": true, "text": "hello=world"}
	if !reflect.DeepEqual(good.lastArgs, wantArgs) {
		t.Errorf("tool args = %#v, want %#v", good.lastArgs, wantArgs)
	}
	if good.calls != 1 {
		t.Errorf("tool calls = %d, want 1", good.calls)
	}
}

func TestToolCommandS4ErrorTable(t *testing.T) {
	unknown := testToolRegistry()
	unavailable := &cliTestTool{id: "unavailable", err: errToolUnavailable}
	writeErr := errors.New("tool output failed")
	configInitErr := errors.New("config directory could not be resolved")
	tests := []struct {
		name       string
		command    *ToolCommand
		args       []string
		out        io.Writer
		wantErrors []string
		wantIs     error
	}{
		{name: "unknown tool", command: newToolTestCommand(t, unknown), args: []string{"missing"}, wantErrors: []string{`tool "missing": tool "missing" not found`}, wantIs: errToolNotFound},
		{name: "malformed arguments", command: newToolTestCommand(t, testToolRegistry(unavailable)), args: []string{"unavailable", " =value"}, wantErrors: []string{`invalid argument " =value": empty key`}, wantIs: errToolArguments},
		{name: "registered unavailable tool", command: newToolTestCommand(t, testToolRegistry(unavailable)), args: []string{"unavailable"}, wantErrors: []string{"tool \"unavailable\": tool unavailable in this build"}, wantIs: errToolUnavailable},
		{name: "config initialization failure", command: toolCommandWithStorageError(t, configInitErr), args: []string{"anything"}, wantErrors: []string{"config: config directory could not be resolved"}, wantIs: errToolConfig},
		{name: "registry load failure", command: toolCommandWithLoaderError(t, errors.New("config load failed")), args: []string{"anything"}, wantErrors: []string{"config load failed"}, wantIs: errToolConfig},
		{name: "list writer failure", command: newToolTestCommand(t, testToolRegistry(&cliTestTool{id: "listed"})), args: []string{"--list"}, out: toolTestFailWriter{err: writeErr}, wantErrors: []string{"tool output failed"}, wantIs: writeErr},
		{name: "message writer failure", command: newToolTestCommand(t, testToolRegistry(&cliTestTool{id: "writer", result: []messages.Message{messages.NewTextMessage(messages.RoleTool, "payload")}})), args: []string{"writer"}, out: toolTestFailWriter{err: writeErr}, wantErrors: []string{"tool output failed"}, wantIs: writeErr},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.out
			if out == nil {
				out = &bytes.Buffer{}
			}
			err := runToolTestCommand(t, tc.command, tc.args, out)
			if err == nil {
				t.Fatal("expected command error")
			}
			for _, want := range tc.wantErrors {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want substring %q", err, want)
				}
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("error = %v, want wrapped identity %v", err, tc.wantIs)
			}
		})
	}
}

func testToolRegistry(testTools ...*cliTestTool) *tools.ToolRegistry {
	entries := make([]config.ToolEntry, 0, len(config.DefaultToolIDs))
	for _, id := range config.DefaultToolIDs {
		entries = append(entries, config.ToolEntry{ID: id, Enabled: false})
	}
	registry := tools.NewToolRegistryFromConfig(&config.Config{Tools: config.ToolsConfig{List: entries}})
	for _, testTool := range testTools {
		_ = registry.Register(testTool)
	}
	return registry
}

func toolCommandWithLoaderError(t *testing.T, want error) *ToolCommand {
	t.Helper()
	command := NewToolCommand(flags.NewGlobalFlags())
	command.registryLoader = func() (*tools.ToolRegistry, error) { return nil, fmt.Errorf("config: %w", want) }
	return command
}

func toolCommandWithStorageError(t *testing.T, want error) *ToolCommand {
	t.Helper()
	command := NewToolCommand(flags.NewGlobalFlags())
	command.configStorageFactory = func(string) (*config.ConfigStorage, error) { return nil, want }
	return command
}

func TestToolCommandParsingAndWriters(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want map[string]any
	}{
		{name: "strings and quotes", args: []string{"plain=value", `double="quoted value"`, "single='quoted'"}, want: map[string]any{"plain": "value", "double": "quoted value", "single": "quoted"}},
		{name: "numbers and booleans", args: []string{"integer=-4", "float=0.25", "yes=true", "no=false"}, want: map[string]any{"integer": int64(-4), "float": 0.25, "yes": true, "no": false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseKeyValueArgs(tc.args)
			if err != nil {
				t.Fatalf("parse args: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parsed = %#v, want %#v", got, tc.want)
			}
		})
	}

	if got := unquoteValue(`"unterminated`); got != `"unterminated` {
		t.Errorf("unquoteValue = %q, want unchanged input", got)
	}
	if got := coerceValue("not-a-number"); got != "not-a-number" {
		t.Errorf("coerceValue = %#v, want string", got)
	}
	if err := (&ToolCommand{}).writeMessages(toolTestFailWriter{err: errors.New("write failed")}, []messages.Message{messages.NewTextMessage(messages.RoleTool, "x")}); err == nil {
		t.Fatal("writeMessages should return writer error")
	}
}

func TestToolCommandLoadsTemporaryConfig(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := NewToolCommand(globalFlags)
	registry, err := command.getRegistry()
	if err != nil {
		t.Fatalf("getRegistry: %v", err)
	}
	if registry.Count() == 0 {
		t.Fatal("default registry should contain tools")
	}
	if _, ok := registry.Get("read_file"); !ok {
		t.Fatal("default registry should contain read_file")
	}
}

func TestToolCommandFilesystemScopeUsesLaunchCwdOrExplicitWorkdir(t *testing.T) {
	launchDir := t.TempDir()
	configDir := t.TempDir()
	selectedDir := t.TempDir()

	tests := []struct {
		name        string
		workdir     string
		wantDir     string
		unwantedDir string
	}{
		{name: "launch cwd default", wantDir: launchDir, unwantedDir: selectedDir},
		{name: "explicit workdir", workdir: selectedDir, wantDir: selectedDir, unwantedDir: launchDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(launchDir)
			fileName := strings.ReplaceAll(tt.name, " ", "-") + ".txt"
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = configDir
			globalFlags.WorkDirPath = tt.workdir
			command := NewToolCommand(globalFlags)
			command.registryLoader = func() (*tools.ToolRegistry, error) {
				return tools.NewToolRegistryFromConfig(&config.Config{}), nil
			}

			var out bytes.Buffer
			err := runToolTestCommand(t, command, []string{"write_file", "path=" + fileName, "content=cwd-marker"}, &out)
			if err != nil {
				t.Fatalf("write_file: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(tt.wantDir, fileName)); err != nil || string(got) != "cwd-marker" {
				t.Fatalf("effective workdir file = %q, %v; want cwd-marker in %s", got, err, tt.wantDir)
			}
			if _, err := os.Stat(filepath.Join(tt.unwantedDir, fileName)); !os.IsNotExist(err) {
				t.Fatalf("relative write escaped into %s: stat error = %v", tt.unwantedDir, err)
			}
			if _, err := os.Stat(filepath.Join(configDir, fileName)); !os.IsNotExist(err) {
				t.Fatalf("relative write used config dir: stat error = %v", err)
			}
		})
	}
}

func TestToolCommandFilesystemScopeAllowsMultipleRootsAndRejectsOutside(t *testing.T) {
	launchDir := t.TempDir()
	allowedOne := t.TempDir()
	allowedTwo := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(allowedOne, "readable.txt"), []byte("allowed content"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(launchDir)
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	globalFlags.WorkDirPath = launchDir
	globalFlags.AllowPathList = []string{allowedOne, allowedTwo, allowedOne}
	command := NewToolCommand(globalFlags)
	command.registryLoader = func() (*tools.ToolRegistry, error) {
		return tools.NewToolRegistryFromConfig(&config.Config{}), nil
	}

	for _, root := range []string{allowedOne, allowedTwo} {
		name := filepath.Join(root, "written.txt")
		var out bytes.Buffer
		if err := runToolTestCommand(t, command, []string{"write_file", "path=" + name, "content=allowed"}, &out); err != nil {
			t.Fatalf("write in explicitly allowed root %q: %v", root, err)
		}
		if got, err := os.ReadFile(name); err != nil || string(got) != "allowed" {
			t.Fatalf("allowed-root write = %q, %v; want allowed", got, err)
		}
	}

	var out bytes.Buffer
	if err := runToolTestCommand(t, command, []string{"read_file", "path=" + filepath.Join(allowedOne, "readable.txt")}, &out); err != nil {
		t.Fatalf("read in explicitly allowed root: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "allowed content") || strings.Contains(got, "path escapes workspace") {
		t.Fatalf("allowed-root read output = %q, want content without denial", got)
	}

	deniedParent := filepath.Join(outside, "not-created", "nested")
	deniedTarget := filepath.Join(deniedParent, "denied.txt")
	cmd := command.Generate()
	cmd.SetArgs([]string{"write_file", "path=" + deniedTarget, "content=must-not-write"})
	out.Reset()
	stderr := &bytes.Buffer{}
	cmd.SetOut(&out)
	cmd.SetErr(stderr)
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, errToolRefusal) || !errors.Is(err, tools.ErrFilesystemRefused) {
		t.Fatalf("outside write error = %v, want stable refusal identity", err)
	}
	if out.Len() != 0 {
		t.Fatalf("outside write stdout = %q, want empty", out.String())
	}
	refusal, decodeErr := tools.DecodeFilesystemRefusal([]byte(strings.TrimSpace(stderr.String())))
	if decodeErr != nil {
		t.Fatalf("decode outside write refusal: %v; stderr=%q", decodeErr, stderr.String())
	}
	if refusal.Operation != "write_file" || refusal.Path != deniedTarget || refusal.Reason != tools.FilesystemRefusalOutsidePermittedRoots {
		t.Fatalf("outside write refusal = %#v, want write/path/outside identity", refusal)
	}
	if strings.Contains(stderr.String(), "must-not-write") {
		t.Fatalf("outside write refusal leaked mutation content: %q", stderr.String())
	}
	if _, err := os.Stat(deniedParent); !os.IsNotExist(err) {
		t.Fatalf("outside write parent = %v, want absent", err)
	}
}

func TestToolCommandFilesystemRefusalIsStderrAndNonZero(t *testing.T) {
	workdir := t.TempDir()
	outside := t.TempDir()
	deniedParent := filepath.Join(outside, "not-created", "nested")
	deniedTarget := filepath.Join(deniedParent, "denied.txt")
	canonicalWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatalf("canonicalize workdir: %v", err)
	}

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	globalFlags.WorkDirPath = workdir
	command := NewToolCommand(globalFlags)
	command.registryLoader = func() (*tools.ToolRegistry, error) {
		return tools.NewToolRegistryFromConfig(&config.Config{}), nil
	}

	cmd := command.Generate()
	cmd.SetArgs([]string{"write_file", "path=" + deniedTarget, "content=MUST-NOT-WRITE"})
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	err = cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("denied direct tool call unexpectedly succeeded")
	}
	if !errors.Is(err, errToolRefusal) || !errors.Is(err, tools.ErrFilesystemRefused) {
		t.Fatalf("direct refusal error = %v, want stable refusal identity", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("denied direct tool stdout = %q, want empty", stdout.String())
	}
	refusal, decodeErr := tools.DecodeFilesystemRefusal([]byte(strings.TrimSpace(stderr.String())))
	if decodeErr != nil {
		t.Fatalf("decode stderr refusal: %v; stderr=%q", decodeErr, stderr.String())
	}
	if refusal.Operation != "write_file" || refusal.Path != deniedTarget || refusal.WorkDir != canonicalWorkdir || refusal.Reason != tools.FilesystemRefusalOutsidePermittedRoots {
		t.Fatalf("stderr refusal = %#v, want write/path/workdir/outside identity", refusal)
	}
	if strings.Contains(stderr.String(), "MUST-NOT-WRITE") {
		t.Fatalf("stderr refusal leaked mutation content: %q", stderr.String())
	}
	if _, statErr := os.Stat(deniedParent); !os.IsNotExist(statErr) {
		t.Fatalf("denied parent = %v, want absent", statErr)
	}
}

func TestToolCommandInvalidWorkdirFailsBeforeRegistryLoad(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.WorkDirPath = filepath.Join(t.TempDir(), "missing-workdir")
	command := NewToolCommand(globalFlags)
	loaderCalled := false
	command.registryLoader = func() (*tools.ToolRegistry, error) {
		loaderCalled = true
		return tools.NewToolRegistryFromConfig(&config.Config{}), nil
	}

	err := runToolTestCommand(t, command, []string{"write_file", "path=relative.txt", "content=should-not-run"}, &bytes.Buffer{})
	if err == nil || !errors.Is(err, tools.ErrInvalidFilesystemRoot) {
		t.Fatalf("invalid workdir error = %v, want invalid filesystem root", err)
	}
	if loaderCalled {
		t.Fatal("registry loaded after invalid workdir; scope validation must precede tool setup")
	}
}

func TestToolCommandInvalidAllowPathFailsBeforeRegistryLoad(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.WorkDirPath = t.TempDir()
	globalFlags.AllowPathList = []string{filepath.Join(t.TempDir(), "missing-allowed-root")}
	command := NewToolCommand(globalFlags)
	loaderCalled := false
	command.registryLoader = func() (*tools.ToolRegistry, error) {
		loaderCalled = true
		return tools.NewToolRegistryFromConfig(&config.Config{}), nil
	}

	err := runToolTestCommand(t, command, []string{"write_file", "path=relative.txt", "content=should-not-run"}, &bytes.Buffer{})
	if err == nil || !errors.Is(err, tools.ErrInvalidFilesystemRoot) {
		t.Fatalf("invalid allow-path error = %v, want ErrInvalidFilesystemRoot", err)
	}
	if loaderCalled {
		t.Fatal("registry loaded after invalid allow-path; scope validation must precede tool setup")
	}
}

func TestToolCommandConfigLoadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte("model: ["), 0600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = dir
	_, err := NewToolCommand(globalFlags).getRegistry()
	if err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("getRegistry error = %v, want load config context", err)
	}
	if !errors.Is(err, errToolConfig) {
		t.Fatalf("getRegistry error = %v, want tool config identity", err)
	}
}

func TestToolCommandListOrderingIsStableForMultipleTools(t *testing.T) {
	registry := testToolRegistry(&cliTestTool{id: "zeta"}, &cliTestTool{id: "alpha"})
	var out bytes.Buffer
	if err := NewToolCommand(flags.NewGlobalFlags()).listTools(&out, registry); err != nil {
		t.Fatalf("listTools: %v", err)
	}
	if got, want := out.String(), "alpha\nzeta\n"; got != want {
		t.Fatalf("listed tools = %q, want %q", got, want)
	}
}
