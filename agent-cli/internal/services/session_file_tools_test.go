package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var sessionFileWorkingDirectoryMu sync.Mutex

// enterSessionFileWorkingDirectory makes the process cwd the isolated root
// used by the real sandboxed file tools. The production sandbox resolver keeps
// its current process-relative I/O behavior, so the lock makes that behavior
// deterministic without allowing this test to affect another test's cwd.
func enterSessionFileWorkingDirectory(t *testing.T, directory string) string {
	t.Helper()
	sessionFileWorkingDirectoryMu.Lock()
	previous, err := os.Getwd()
	if err != nil {
		sessionFileWorkingDirectoryMu.Unlock()
		t.Fatalf("get process working directory: %v", err)
	}
	if err := os.Chdir(directory); err != nil {
		sessionFileWorkingDirectoryMu.Unlock()
		t.Fatalf("enter session working directory %q: %v", directory, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore process working directory %q: %v", previous, err)
		}
		sessionFileWorkingDirectoryMu.Unlock()
	})
	return previous
}

func newSessionFileToolSurface(t *testing.T, workspace string) (messages.ToolExecutor, []messages.ToolDefinition) {
	t.Helper()
	registry := tools.NewEmptyToolRegistry()
	for _, tool := range []tools.Tool{
		tools.NewReadFileTool(workspace, true),
		tools.NewWriteFileTool(workspace, true),
		tools.NewAppendFileTool(workspace, true),
		tools.NewExecTool(workspace, true),
	} {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("register %q: %v", tool.Name(), err)
		}
	}

	staticExecutor := tools.NewRegistryExecutor(registry)
	surface, err := tools.ComposeToolSurface(staticExecutor, registry.ToAgentLoopDefs(), nil, nil)
	if err != nil {
		t.Fatalf("compose registry-backed file tool surface: %v", err)
	}
	if surface.Executor == nil || len(surface.Definitions) != 4 {
		t.Fatalf("file tool surface = %#v, want composed executor and four definitions", surface)
	}
	return surface.Executor, surface.Definitions
}

func marshalSessionFileArguments(t *testing.T, args map[string]string) string {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal session file arguments: %v", err)
	}
	return string(encoded)
}

type fileRoundTripCall struct {
	id        string
	name      string
	arguments string
}

type fileRoundTripCheckpoint struct {
	call       messages.ToolCall
	response   messages.ToolCallResponse
	disk       []byte
	diskExists bool
	diskErr    error
}

// fileRoundTripExecutor observes the real composed executor without replacing
// it. Each checkpoint reads the absolute target independently of the tool so
// creation, append, and deletion are proven at the operation boundary.
type fileRoundTripExecutor struct {
	inner  messages.ToolExecutor
	target string

	mu          sync.Mutex
	calls       []messages.ToolCall
	checkpoints []fileRoundTripCheckpoint
}

func (e *fileRoundTripExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	response, err := e.inner.Execute(ctx, call)
	checkpoint := fileRoundTripCheckpoint{call: call, response: response}
	checkpoint.disk, checkpoint.diskErr = os.ReadFile(e.target)
	checkpoint.diskExists = checkpoint.diskErr == nil
	if checkpoint.diskErr != nil && errors.Is(checkpoint.diskErr, os.ErrNotExist) {
		checkpoint.diskErr = nil
	}

	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.checkpoints = append(e.checkpoints, checkpoint)
	e.mu.Unlock()
	return response, err
}

func (e *fileRoundTripExecutor) snapshots() ([]messages.ToolCall, []fileRoundTripCheckpoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	calls := append([]messages.ToolCall(nil), e.calls...)
	checkpoints := append([]fileRoundTripCheckpoint(nil), e.checkpoints...)
	for i := range checkpoints {
		checkpoints[i].disk = append([]byte(nil), checkpoints[i].disk...)
	}
	return calls, checkpoints
}

// TestRunAgentLoopSession_FileToolRoundTripThroughRegistryAndComposition
// drives the complete relative-file lifecycle through the existing scripted
// provider seam. The registry, composed route, session adapter, tool runner,
// ordering layer, and result forwarder are all production implementations.
func TestRunAgentLoopSession_FileToolRoundTripThroughRegistryAndComposition(t *testing.T) {
	root := t.TempDir()
	launchDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get launch directory: %v", err)
	}
	enterSessionFileWorkingDirectory(t, root)

	relativeTarget := filepath.ToSlash(filepath.Join("nested", "session-roundtrip-"+filepath.Base(root)+".txt"))
	target := filepath.Join(root, filepath.FromSlash(relativeTarget))
	outOfRootTarget := filepath.Join(launchDirectory, filepath.FromSlash(relativeTarget))
	if _, err := os.Lstat(outOfRootTarget); err == nil {
		t.Fatalf("out-of-root target already exists before test: %q", outOfRootTarget)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check out-of-root target: %v", err)
	}

	initial := "  first line — α  \n\t第二行 with tabs\t\nemoji: 🧪\n"
	suffix := "\n  appended: café / 東京\t\nfinal whitespace  \n"
	combined := initial + suffix
	deleteCommand := "cd " + filepath.ToSlash(filepath.Dir(relativeTarget)) + " && rm " + filepath.Base(relativeTarget)
	if runtime.GOOS == "windows" {
		deleteCommand = "Set-Location " + filepath.ToSlash(filepath.Dir(relativeTarget)) + "; Remove-Item " + filepath.Base(relativeTarget)
	}

	calls := []fileRoundTripCall{
		{id: "file-roundtrip-write", name: "write_file", arguments: marshalSessionFileArguments(t, map[string]string{
			"path": relativeTarget, "content": initial,
		})},
		{id: "file-roundtrip-read-initial", name: "read_file", arguments: marshalSessionFileArguments(t, map[string]string{
			"path": relativeTarget,
		})},
		{id: "file-roundtrip-append", name: "append_file", arguments: marshalSessionFileArguments(t, map[string]string{
			"path": relativeTarget, "content": suffix,
		})},
		{id: "file-roundtrip-read-appended", name: "read_file", arguments: marshalSessionFileArguments(t, map[string]string{
			"path": relativeTarget,
		})},
		{id: "file-roundtrip-delete", name: "exec", arguments: marshalSessionFileArguments(t, map[string]string{
			"command": deleteCommand,
		})},
		{id: "file-roundtrip-read-missing", name: "read_file", arguments: marshalSessionFileArguments(t, map[string]string{
			"path": relativeTarget,
		})},
	}

	executor, definitions := newSessionFileToolSurface(t, root)
	recordingExecutor := &fileRoundTripExecutor{inner: executor, target: target}
	out := newSignalingBuffer()

	wantWriteResult := "File written: " + relativeTarget
	wantAppendResult := "Appended to " + relativeTarget
	inferencer := newScriptedToolCallInferencer(
		out,
		"file session round trip completed",
		"file not found",
		scriptedTurn{events: toolCallEvents(calls[0].id, calls[0].name, calls[0].arguments)},
		scriptedTurn{after: wantWriteResult, events: toolCallEvents(calls[1].id, calls[1].name, calls[1].arguments)},
		scriptedTurn{after: initial, events: toolCallEvents(calls[2].id, calls[2].name, calls[2].arguments)},
		scriptedTurn{after: wantAppendResult, events: toolCallEvents(calls[3].id, calls[3].name, calls[3].arguments)},
		scriptedTurn{after: combined, events: toolCallEvents(calls[4].id, calls[4].name, calls[4].arguments)},
		scriptedTurn{after: "(no output)", events: toolCallEvents(calls[5].id, calls[5].name, calls[5].arguments)},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runAgentLoopSession(ctx, out, inferencer, sessionLoopOptions{
		MaxDuration:              4 * time.Second,
		WaitForClose:             true,
		ToolExecutor:             recordingExecutor,
		ToolDefinitions:          definitions,
		ToolExecutionTimeout:     2 * time.Second,
		AdvertiseToolDefinitions: true,
	}); err != nil {
		t.Fatalf("file round-trip session: %v\noutput:\n%s", err, out.String())
	}

	gotCalls, checkpoints := recordingExecutor.snapshots()
	if len(gotCalls) != len(calls) {
		t.Fatalf("executor calls = %d, want %d: %#v\noutput=%q\ncheckpoints=%#v", len(gotCalls), len(calls), gotCalls, out.String(), checkpoints)
	}
	for i, want := range calls {
		got := gotCalls[i]
		if got.ID != want.id || got.Name != want.name || got.Arguments != want.arguments {
			t.Fatalf("executor call %d = %#v, want id=%q name=%q arguments=%q", i, got, want.id, want.name, want.arguments)
		}
	}

	wantDisk := []struct {
		exists bool
		data   []byte
	}{
		{exists: true, data: []byte(initial)},
		{exists: true, data: []byte(initial)},
		{exists: true, data: []byte(combined)},
		{exists: true, data: []byte(combined)},
		{exists: false},
		{exists: false},
	}
	wantResponses := []string{wantWriteResult, initial, wantAppendResult, combined, "(no output)", ""}
	if len(checkpoints) != len(wantDisk) {
		t.Fatalf("disk checkpoints = %d, want %d: %#v", len(checkpoints), len(wantDisk), checkpoints)
	}
	for i, checkpoint := range checkpoints {
		if checkpoint.diskErr != nil {
			t.Fatalf("checkpoint %d disk read: %v", i, checkpoint.diskErr)
		}
		if checkpoint.diskExists != wantDisk[i].exists {
			t.Fatalf("checkpoint %d disk exists = %v, want %v", i, checkpoint.diskExists, wantDisk[i].exists)
		}
		if wantDisk[i].exists && !bytes.Equal(checkpoint.disk, wantDisk[i].data) {
			t.Fatalf("checkpoint %d disk bytes = %q, want exact %q", i, checkpoint.disk, wantDisk[i].data)
		}
		if checkpoint.response.ToolCallID != calls[i].id || checkpoint.response.Name != calls[i].name {
			t.Fatalf("checkpoint %d response correlation = (%q, %q), want (%q, %q)", i, checkpoint.response.ToolCallID, checkpoint.response.Name, calls[i].id, calls[i].name)
		}
		if i < len(wantResponses)-1 && checkpoint.response.Content != wantResponses[i] {
			t.Fatalf("checkpoint %d response = %q, want exact %q", i, checkpoint.response.Content, wantResponses[i])
		}
	}

	missing := checkpoints[len(checkpoints)-1].response.Content
	if !strings.Contains(missing, "file not found") {
		t.Fatalf("post-delete read result = %q, want a missing-file cause", missing)
	}
	for _, stale := range []string{initial, combined, "File written:", "Appended to", "success"} {
		if strings.Contains(strings.ToLower(missing), strings.ToLower(stale)) {
			t.Fatalf("post-delete read result = %q contains stale/success text %q", missing, stale)
		}
	}

	session := inferencer.sessionSnapshot()
	if session == nil {
		t.Fatal("scripted provider did not retain its connected session")
	}
	select {
	case <-inferencer.runFinished:
	case <-time.After(time.Second):
		t.Fatal("scripted provider did not finish after sending the terminal response")
	}

	session.mu.Lock()
	sent := append([]messages.StreamMessage(nil), session.sent...)
	session.mu.Unlock()
	providerResults := make([]messages.ToolCallEndValue, 0, len(calls))
	for _, message := range sent {
		if message.Type != messages.StreamTypeToolCallEnd {
			continue
		}
		value, ok := message.Value.(*messages.ToolCallEndValue)
		if !ok || value == nil {
			t.Fatalf("provider-visible tool result value = %T, want *messages.ToolCallEndValue", message.Value)
		}
		providerResults = append(providerResults, *value)
	}
	if len(providerResults) != len(calls) {
		t.Fatalf("provider-visible tool results = %d, want %d: %#v", len(providerResults), len(calls), sent)
	}
	for i, result := range providerResults {
		if result.ToolCallID != calls[i].id || result.Name != calls[i].name {
			t.Fatalf("provider-visible result %d correlation = (%q, %q), want (%q, %q)", i, result.ToolCallID, result.Name, calls[i].id, calls[i].name)
		}
		wantOutput := wantResponses[i]
		if i == len(wantResponses)-1 {
			wantOutput = missing
		}
		if result.Arguments != wantOutput {
			t.Fatalf("provider-visible result %d = %q, want exact %q", i, result.Arguments, wantOutput)
		}
	}
	if !strings.Contains(out.String(), "file session round trip completed") || !strings.Contains(out.String(), "[session closed: test complete]") {
		t.Fatalf("session did not reach scripted terminal response:\n%s", out.String())
	}

	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target after delete = %v, want not-exist", err)
	}
	if _, err := os.Lstat(outOfRootTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("out-of-root target after session = %v, want not-exist", err)
	}
}
