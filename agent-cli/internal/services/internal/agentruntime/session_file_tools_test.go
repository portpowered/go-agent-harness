package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
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

func newPolicySessionWriteToolSurface(t *testing.T, workspace string) (messages.ToolExecutor, []messages.ToolDefinition, *tools.FilesystemPolicy) {
	t.Helper()
	policy, err := tools.ResolveFilesystemPolicy(workspace)
	if err != nil {
		t.Fatalf("resolve filesystem policy: %v", err)
	}
	registry := tools.NewEmptyToolRegistry()
	if err := registry.Register(tools.NewWriteFileToolWithPolicy(policy)); err != nil {
		t.Fatalf("register write_file: %v", err)
	}
	staticExecutor := tools.NewRegistryExecutor(registry)
	surface, err := tools.ComposeToolSurface(staticExecutor, registry.ToAgentLoopDefs(), nil, nil)
	if err != nil {
		t.Fatalf("compose policy-backed write surface: %v", err)
	}
	return surface.Executor, surface.Definitions, policy
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

// TestRunAgentLoopSession_FileToolPermissionDeniedThroughRegistryAndComposition
// drives a real OS access denial through the same registry-backed/composed
// executor used by the round-trip test. The scripted provider cannot release
// its terminal response until the correlated denial has crossed the session
// boundary.
func TestRunAgentLoopSession_FileToolPermissionDeniedThroughRegistryAndComposition(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-denied session coverage requires Unix mode-bit enforcement; Windows ACL setup is unsupported here")
	}

	root := t.TempDir()
	enterSessionFileWorkingDirectory(t, root)
	relativeTarget := filepath.ToSlash(filepath.Join("nested", "session-permission-"+filepath.Base(root)+".txt"))
	target := filepath.Join(root, filepath.FromSlash(relativeTarget))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create permission fixture parent: %v", err)
	}
	protected := "PROTECTED_PERMISSION_CONTENT_MUST_NOT_LEAK\n"
	if err := os.WriteFile(target, []byte(protected), 0o600); err != nil {
		t.Fatalf("create permission fixture: %v", err)
	}
	if err := os.Chmod(target, 0o000); err != nil {
		t.Fatalf("deny permission fixture read access: %v", err)
	}
	// Register this after t.TempDir and enterSessionFileWorkingDirectory so the
	// mode is restored before either the cwd or temporary root is cleaned up.
	t.Cleanup(func() {
		if err := os.Chmod(target, 0o600); err != nil {
			t.Errorf("restore permission fixture mode: %v", err)
		}
	})
	if _, err := os.ReadFile(target); err == nil {
		t.Fatalf("mode-zero permission fixture remained readable on %s; Unix denial assertion would be invalid", runtime.GOOS)
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("mode-zero permission fixture read error = %v, want errors.Is(fs.ErrPermission)", err)
	}

	const callID = "file-permission-denied-read"
	call := fileRoundTripCall{
		id:        callID,
		name:      "read_file",
		arguments: marshalSessionFileArguments(t, map[string]string{"path": relativeTarget}),
	}
	executor, definitions := newSessionFileToolSurface(t, root)
	recordingExecutor := &fileRoundTripExecutor{inner: executor, target: target}
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(
		out,
		"permission failure session terminated",
		"access denied",
		scriptedTurn{events: toolCallEvents(call.id, call.name, call.arguments)},
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
		t.Fatalf("permission-denied session: %v\noutput:\n%s", err, out.String())
	}

	gotCalls, checkpoints := recordingExecutor.snapshots()
	if len(gotCalls) != 1 || gotCalls[0].ID != call.id || gotCalls[0].Name != call.name || gotCalls[0].Arguments != call.arguments {
		t.Fatalf("permission-denied executor calls = %#v, want one exact call %#v", gotCalls, call)
	}
	if len(checkpoints) != 1 {
		t.Fatalf("permission-denied checkpoints = %d, want one: %#v", len(checkpoints), checkpoints)
	}
	checkpoint := checkpoints[0]
	if checkpoint.diskExists || !errors.Is(checkpoint.diskErr, fs.ErrPermission) {
		t.Fatalf("protected disk checkpoint = exists=%v err=%v, want inaccessible permission error", checkpoint.diskExists, checkpoint.diskErr)
	}
	failure := checkpoint.response
	if failure.ToolCallID != call.id || failure.Name != call.name {
		t.Fatalf("permission failure response correlation = (%q, %q), want (%q, %q)", failure.ToolCallID, failure.Name, call.id, call.name)
	}
	if strings.TrimSpace(failure.Content) == "" {
		t.Fatal("permission failure response is empty")
	}
	lowerFailure := strings.ToLower(failure.Content)
	if !strings.Contains(lowerFailure, "access denied") && !strings.Contains(lowerFailure, "permission denied") {
		t.Fatalf("permission failure response = %q, want access/permission denial cause", failure.Content)
	}
	for _, forbidden := range []string{protected, "success", "read successfully", "file contents"} {
		if strings.Contains(lowerFailure, strings.ToLower(forbidden)) {
			t.Fatalf("permission failure response = %q contains protected/success wording %q", failure.Content, forbidden)
		}
	}

	session := inferencer.sessionSnapshot()
	if session == nil {
		t.Fatal("scripted provider did not retain its connected permission-denial session")
	}
	select {
	case <-inferencer.runFinished:
	case <-time.After(time.Second):
		t.Fatal("scripted provider did not finish after the permission failure terminal response")
	}
	session.mu.Lock()
	sent := append([]messages.StreamMessage(nil), session.sent...)
	session.mu.Unlock()
	resultIndex, responseCreateIndex := -1, -1
	var providerResult *messages.ToolCallEndValue
	for index, message := range sent {
		switch message.Type {
		case messages.StreamTypeToolCallEnd:
			value, ok := message.Value.(*messages.ToolCallEndValue)
			if !ok || value == nil || value.ToolCallID != call.id {
				continue
			}
			if providerResult != nil {
				t.Fatalf("provider received duplicate permission result: %#v", sent)
			}
			copyValue := *value
			providerResult = &copyValue
			resultIndex = index
		case messages.StreamTypeResponseCreate:
			if resultIndex >= 0 && responseCreateIndex < 0 {
				responseCreateIndex = index
			}
		}
	}
	if providerResult == nil {
		t.Fatalf("provider did not receive a correlated permission failure: %#v", sent)
	}
	if providerResult.Name != call.name || providerResult.Arguments != failure.Content {
		t.Fatalf("provider permission result = %#v, want name=%q content=%q", providerResult, call.name, failure.Content)
	}
	if responseCreateIndex < 0 {
		t.Fatalf("provider terminal continuation was not released after the permission result: %#v", sent)
	}
	if responseCreateIndex < resultIndex {
		t.Fatalf("provider response-create at %d preceded permission result at %d: %#v", responseCreateIndex, resultIndex, sent)
	}
	if strings.Contains(failure.Content, protected) || strings.Contains(out.String(), protected) {
		t.Fatalf("protected content leaked through permission failure: response=%q output=%q", failure.Content, out.String())
	}
	if !strings.Contains(out.String(), "permission failure session terminated") || !strings.Contains(out.String(), "[session closed: test complete]") {
		t.Fatalf("session did not reach its scripted terminal response after denial:\n%s", out.String())
	}
}

// TestRunAgentLoopSession_FilesystemRefusalIsHonestAndRecoverable drives the
// policy-backed production route with a scripted live-shaped provider. The
// provider must receive the structured refusal before it is allowed to emit an
// honest customer-facing denial, and the requested outside tree must remain
// absent.
func TestRunAgentLoopSession_FilesystemRefusalIsHonestAndRecoverable(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "not-created", "nested", "refused.txt")
	const callID = "filesystem-refusal-call"
	arguments := marshalSessionFileArguments(t, map[string]string{
		"path": target, "content": "MUST-NOT-WRITE",
	})

	executor, definitions, policy := newPolicySessionWriteToolSurface(t, root)
	recordingExecutor := &fileRoundTripExecutor{inner: executor, target: target}
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(
		out,
		"The write was refused and not performed.",
		tools.FilesystemRefusalVersion,
		scriptedTurn{events: toolCallEvents(callID, "write_file", arguments)},
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
		t.Fatalf("filesystem refusal session: %v\noutput:\n%s", err, out.String())
	}

	gotCalls, checkpoints := recordingExecutor.snapshots()
	if len(gotCalls) != 1 || len(checkpoints) != 1 {
		t.Fatalf("filesystem refusal calls/checkpoints = %d/%d, want one each", len(gotCalls), len(checkpoints))
	}
	checkpoint := checkpoints[0]
	if checkpoint.diskExists || checkpoint.diskErr != nil {
		t.Fatalf("refused target checkpoint = exists=%v err=%v, want absent", checkpoint.diskExists, checkpoint.diskErr)
	}
	refusal, err := tools.DecodeFilesystemRefusal([]byte(checkpoint.response.Content))
	if err != nil {
		t.Fatalf("decode session refusal: %v; content=%q", err, checkpoint.response.Content)
	}
	if refusal.Operation != "write_file" || refusal.Path != target || refusal.WorkDir != policy.PrimaryRoot() || refusal.Reason != tools.FilesystemRefusalOutsidePermittedRoots {
		t.Fatalf("session refusal = %#v, want write/path/workdir/outside identity", refusal)
	}
	if strings.Contains(checkpoint.response.Content, "MUST-NOT-WRITE") || strings.Contains(checkpoint.response.Content, "File written") {
		t.Fatalf("session refusal was contaminated with request/success text: %q", checkpoint.response.Content)
	}
	if !strings.Contains(out.String(), tools.FilesystemRefusalVersion) || !strings.Contains(out.String(), "refused and not performed") {
		t.Fatalf("session did not expose refusal before honest customer response:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Dir(target)); !os.IsNotExist(statErr) {
		t.Fatalf("refused parent = %v, want absent", statErr)
	}
}
