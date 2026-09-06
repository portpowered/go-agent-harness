package services

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestBuildExecuteInput_ReachableErrors(t *testing.T) {
	readErr := errors.New("stdin read failed")
	missingPath := filepath.Join(t.TempDir(), "missing.txt")

	tests := []struct {
		name           string
		stdin          io.Reader
		argPrompt      string
		filePaths      []string
		wantMessage    string
		wantPrefix     string
		assertIdentity func(t *testing.T, err error)
	}{
		{
			name:        "whitespace stdin has no prompt",
			stdin:       strings.NewReader(" \n\t "),
			wantMessage: "no prompt: provide a prompt as an argument or pipe text via stdin",
			assertIdentity: func(t *testing.T, err error) {
				t.Helper()
				if got := fmt.Sprintf("%T", err); got != "*errors.errorString" {
					t.Fatalf("error type = %s, want *errors.errorString", got)
				}
			},
		},
		{
			name:       "stdin reader failure",
			stdin:      failingReader{err: readErr},
			wantPrefix: "read stdin: stdin read failed",
			assertIdentity: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, readErr) {
					t.Fatalf("error = %v, want errors.Is(..., readErr)", err)
				}
			},
		},
		{
			name:       "file reader failure",
			stdin:      strings.NewReader("read this file"),
			argPrompt:  "inspect it",
			filePaths:  []string{missingPath},
			wantPrefix: "attachment \"" + missingPath + "\": missing file",
			assertIdentity: func(t *testing.T, err error) {
				t.Helper()
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) {
					t.Fatalf("error = %v, want an *os.PathError", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildExecuteInput(test.stdin, test.argPrompt, test.filePaths)
			if err == nil {
				t.Fatal("BuildExecuteInput() error = nil, want error")
			}
			if test.wantMessage != "" && err.Error() != test.wantMessage {
				t.Errorf("error = %q, want %q", err.Error(), test.wantMessage)
			}
			if test.wantPrefix != "" && !strings.HasPrefix(err.Error(), test.wantPrefix) {
				t.Errorf("error = %q, want prefix %q", err.Error(), test.wantPrefix)
			}
			test.assertIdentity(t, err)
		})
	}
}

func TestBuildExecuteInput_RejectsAttachmentBeforeReadingStdin(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.txt")
	stdinErr := errors.New("stdin must not be read")

	_, err := BuildExecuteInput(failingReader{err: stdinErr}, "inspect this", []string{missingPath})
	if err == nil {
		t.Fatal("BuildExecuteInput() error = nil, want missing attachment error")
	}
	if !strings.Contains(err.Error(), missingPath) || !strings.Contains(err.Error(), input.AttachmentReasonMissing) {
		t.Fatalf("error = %q, want missing attachment path and reason", err)
	}
	if errors.Is(err, stdinErr) {
		t.Fatalf("error = %q, want attachment validation to precede stdin read", err)
	}
}

func TestBuildExecuteInput_SuccessShapes(t *testing.T) {
	tempDir := t.TempDir()
	firstPath := filepath.Join(tempDir, "first.txt")
	secondPath := filepath.Join(tempDir, "second.txt")
	if err := os.WriteFile(firstPath, []byte("first file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second file"), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		stdin     io.Reader
		argPrompt string
		filePaths []string
		check     func(t *testing.T, input agentloop.ExecuteInput)
	}{
		{
			name:      "argument prompt",
			argPrompt: "answer this",
			check: func(t *testing.T, input agentloop.ExecuteInput) {
				t.Helper()
				if input.Message != "answer this" || len(input.ContentParts) != 0 {
					t.Fatalf("input = %#v, want text-only prompt", input)
				}
			},
		},
		{
			name:      "stdin context and argument instruction",
			stdin:     strings.NewReader("context from stdin"),
			argPrompt: "follow the instruction",
			check: func(t *testing.T, input agentloop.ExecuteInput) {
				t.Helper()
				want := "context from stdin\n\nfollow the instruction"
				if input.Message != want || len(input.ContentParts) != 0 {
					t.Fatalf("input = %#v, want combined text prompt", input)
				}
			},
		},
		{
			name:  "stdin text without argument",
			stdin: strings.NewReader("standalone stdin text"),
			check: func(t *testing.T, input agentloop.ExecuteInput) {
				t.Helper()
				if input.Message != "standalone stdin text" || len(input.ContentParts) != 0 {
					t.Fatalf("input = %#v, want stdin text prompt", input)
				}
			},
		},
		{
			name:  "binary stdin becomes a content part",
			stdin: bytes.NewReader([]byte{0, 1, 2, 3}),
			check: func(t *testing.T, input agentloop.ExecuteInput) {
				t.Helper()
				if input.Message != "" || len(input.ContentParts) != 1 {
					t.Fatalf("input = %#v, want one binary content part", input)
				}
				part, ok := input.ContentParts[0].(messages.FilePart)
				if !ok {
					t.Fatalf("content part type = %T, want messages.FilePart", input.ContentParts[0])
				}
				if !bytes.Equal(part.Bytes, []byte{0, 1, 2, 3}) || part.Name != "stdin" {
					t.Errorf("file part = %#v, want stdin bytes and name", part)
				}
			},
		},
		{
			name:      "multiple files preserve order",
			filePaths: []string{firstPath, secondPath},
			check: func(t *testing.T, input agentloop.ExecuteInput) {
				t.Helper()
				if input.Message != "" || len(input.ContentParts) != 2 {
					t.Fatalf("input = %#v, want two file parts", input)
				}
				for i, want := range []struct {
					name string
					data string
				}{
					{name: "first.txt", data: "first file"},
					{name: "second.txt", data: "second file"},
				} {
					part, ok := input.ContentParts[i].(messages.FilePart)
					if !ok {
						t.Fatalf("content part %d type = %T, want messages.FilePart", i, input.ContentParts[i])
					}
					if part.Name != want.name || string(part.Bytes) != want.data {
						t.Errorf("content part %d = %#v, want name %q and data %q", i, part, want.name, want.data)
					}
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, err := BuildExecuteInput(test.stdin, test.argPrompt, test.filePaths)
			if err != nil {
				t.Fatalf("BuildExecuteInput() error = %v", err)
			}
			test.check(t, input)
		})
	}
}

func TestBuildAgentConfigFromFlags_MapsAllFlags(t *testing.T) {
	initialHistory := []messages.Message{messages.NewTextMessage(messages.RoleUser, "earlier")}
	globalFlags := &flags.GlobalFlags{
		VerboseMode:   2,
		ConfigDirPath: filepath.Join(t.TempDir(), "config"),
		WorkDirPath:   filepath.Join(t.TempDir(), "workspace"),
		AllowPathList: []string{filepath.Join(t.TempDir(), "allowed")},
		LogToStdout:   true,
	}
	askFlags := &flags.AskFlags{
		SystemPrompt:          "system prompt",
		NoSystemInformation:   true,
		ContinueLastSession:   true,
		SessionID:             "flag-session",
		Model:                 "model-name",
		Provider:              "provider-name",
		APIKey:                "api-key",
		BaseURL:               "https://example.test",
		Stream:                true,
		OutputJSON:            true,
		OutputReasoningTokens: true,
		OutputModality:        "audio",
		ModelConfig:           `{"temperature":0.2}`,
		RecordCapturePath:     "record.json",
		ReplayCapturePath:     "replay.json",
	}

	want := &session.Request{
		SystemPrompt:          askFlags.SystemPrompt,
		SessionID:             askFlags.SessionID,
		ContinueLastSession:   askFlags.ContinueLastSession,
		InitialHistory:        initialHistory,
		Model:                 askFlags.Model,
		Provider:              askFlags.Provider,
		APIKey:                askFlags.APIKey,
		BaseURL:               askFlags.BaseURL,
		OutputModality:        askFlags.OutputModality,
		ModelConfig:           askFlags.ModelConfig,
		OutputReasoningTokens: askFlags.OutputReasoningTokens,
		RecordCapturePath:     askFlags.RecordCapturePath,
		ReplayCapturePath:     askFlags.ReplayCapturePath,
	}

	got := BuildAgentConfigFromFlags(globalFlags, askFlags, initialHistory, "call-session")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildAgentConfigFromFlags() = %#v, want %#v", got, want)
	}
}

func TestCLIHostDefaultWorkDirUsesLaunchDirectory(t *testing.T) {
	launchDir := t.TempDir()
	t.Chdir(launchDir)

	workDir, err := cliWorkDir(&flags.GlobalFlags{ConfigDirPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.Getwd(); err != nil {
		t.Fatalf("get launch directory: %v", err)
	} else if workDir != got {
		t.Fatalf("default WorkDir = %q, want launch directory %q", workDir, got)
	}
}

func TestBuildAgentConfigFromFlags_UsesCallSessionWithoutGlobalFlags(t *testing.T) {
	got := BuildAgentConfigFromFlags(nil, flags.NewAskFlags(), nil, "call-session")
	if got.SessionID != "call-session" {
		t.Errorf("SessionID = %q, want call-session", got.SessionID)
	}
}

func TestDefaultToolDefs_DelegatesToRegistry(t *testing.T) {
	input := []messages.ToolDefinition{{Name: "read_file", Description: "read a file"}}
	defs := DefaultToolDefs(input)
	if len(defs) != len(input) {
		t.Fatalf("definition count = %d, want input count %d", len(defs), len(input))
	}
	if len(defs) == 0 {
		t.Fatal("DefaultToolDefs() returned no definitions for the default registry")
	}
	defs[0].Name = "mutated"
	if input[0].Name == "mutated" {
		t.Fatal("DefaultToolDefs returned aliased definitions")
	}
	for _, def := range defs {
		if def.Name == "" || def.Description == "" {
			t.Errorf("definition = %#v, want name and description", def)
		}
	}
}
