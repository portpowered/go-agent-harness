package instructions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	configDir   = "/config"
	workDir     = "/work"
	primaryRoot = "/work/root"
	promptValue = "system prompt"
	scopeText   = "Filesystem scope: /work"
)

type loader struct{ workspace string }

func (loader) Stat(string) error                           { return nil }
func (loader) ReadFile(string) ([]byte, error)             { return nil, nil }
func (loader) SkillsSummary() (string, error)              { return "", nil }
func newLoader(workspace string) session.InstructionLoader { return loader{workspace: workspace} }

type recordingService struct {
	request session.InstructionRequest
	err     error
}

func (s *recordingService) Resolve(_ context.Context, request session.InstructionRequest) (session.InstructionResult, error) {
	s.request = request
	return session.InstructionResult{Instructions: "resolved:" + request.Prompt}, s.err
}

func (s *recordingService) Compose(composition session.InstructionComposition) string {
	return composition.Instructions
}

func resolver(calls *[]string, err error) func(string) (string, error) {
	return func(dir string) (string, error) {
		*calls = append(*calls, dir)
		return primaryRoot, err
	}
}

func TestNormalizeWorkspaceSelection(t *testing.T) {
	var calls []string
	got, err := Normalize(sessionturn.InstructionsRequest{Prompt: promptValue, ConfigDir: configDir, ResolveWorkspace: resolver(&calls, nil), Loader: newLoader})
	if err != nil || got.WorkspaceDir != primaryRoot || len(calls) != 1 || calls[0] != configDir || got.FilesystemScopeSet {
		t.Fatalf("config fallback = %#v, calls=%v, %v", got, calls, err)
	}
	if loaded, ok := got.Loader.(loader); !ok || loaded.workspace != primaryRoot {
		t.Fatalf("loader workspace = %#v", got.Loader)
	}
	calls = nil
	scoped, err := Normalize(sessionturn.InstructionsRequest{WorkDir: workDir, ConfigDir: configDir, Scope: &sessionturn.FilesystemScope{Description: scopeText}, ResolveWorkspace: resolver(&calls, nil)})
	if err != nil || scoped.WorkspaceDir != workDir || len(calls) != 0 || !scoped.FilesystemScopeSet || scoped.FilesystemScopeDescription != scopeText || scoped.Loader != nil {
		t.Fatalf("scoped = %#v, %v", scoped, err)
	}
	empty, err := Normalize(sessionturn.InstructionsRequest{Scope: &sessionturn.FilesystemScope{}})
	if err != nil || empty.WorkspaceDir != "" || !empty.FilesystemScopeSet {
		t.Fatalf("empty scoped = %#v, %v", empty, err)
	}
	unvalidated, err := Normalize(sessionturn.InstructionsRequest{WorkDir: workDir})
	if err != nil || unvalidated.WorkspaceDir != workDir {
		t.Fatalf("no resolver = %#v, %v", unvalidated, err)
	}
}

func TestNormalizeWrapsWorkspaceFailure(t *testing.T) {
	var calls []string
	missing := errors.New("workspace does not exist")
	_, err := Normalize(sessionturn.InstructionsRequest{WorkDir: workDir, ResolveWorkspace: resolver(&calls, missing)})
	if !errors.Is(err, missing) || !strings.HasPrefix(err.Error(), "resolve filesystem scope: ") {
		t.Fatalf("workspace failure = %v", err)
	}
	if _, err := Resolve(context.Background(), &recordingService{}, sessionturn.InstructionsRequest{WorkDir: workDir, ResolveWorkspace: resolver(&calls, missing)}); !errors.Is(err, missing) {
		t.Fatalf("resolve workspace failure = %v", err)
	}
}

func TestResolveDelegatesToSessionService(t *testing.T) {
	service := &recordingService{}
	got, err := Resolve(context.Background(), service, sessionturn.InstructionsRequest{Prompt: promptValue, WorkDir: workDir})
	if err != nil || got != "resolved:"+promptValue || service.request.WorkspaceDir != workDir {
		t.Fatalf("resolve = %q, %v, request=%#v", got, err, service.request)
	}
	failure := errors.New("prompt file unreadable")
	if _, err := Resolve(context.Background(), &recordingService{err: failure}, sessionturn.InstructionsRequest{}); !errors.Is(err, failure) {
		t.Fatalf("service failure = %v", err)
	}
	if _, err := Resolve(context.Background(), nil, sessionturn.InstructionsRequest{}); !errors.Is(err, errNoResolver) {
		t.Fatalf("nil service = %v", err)
	}
}
