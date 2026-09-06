package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/spf13/cobra"
)

type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type configAddLocalCase struct {
	name       string
	baseURL    func(string) string
	statuses   map[string]int
	seed       string
	model      string
	wantStdout string
	wantStderr string
	wantError  string
	wantPaths  []string
}

func runConfigAddLocalCase(t *testing.T, tc configAddLocalCase) {
	t.Helper()
	configDir := t.TempDir()
	server := newProbeServer(t, tc.statuses)
	defer server.Close()
	baseURL := configAddLocalBaseURL(tc, server.URL)
	seedConfigAddLocalCase(t, configDir, tc.seed)
	args := configAddLocalArgs(tc, configDir, baseURL)
	got := executeGeneratedCLI(context.Background(), configDir, args...)
	if tc.wantError != "" {
		assertConfigAddLocalError(t, got.err, tc.wantError)
		return
	}
	if got.err != nil {
		t.Fatalf("execute config add-local: %v", got.err)
	}
	assertConfigAddLocalOutput(t, tc, configDir, server, got)
}

func configAddLocalBaseURL(tc configAddLocalCase, serverURL string) string {
	if tc.baseURL == nil {
		return ""
	}
	return tc.baseURL(serverURL)
}

func seedConfigAddLocalCase(t *testing.T, configDir, seed string) {
	t.Helper()
	if seed == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(seed), 0600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}

func configAddLocalArgs(tc configAddLocalCase, configDir, baseURL string) []string {
	args := []string{"--config-dir", configDir, "config", "add-local"}
	if tc.baseURL != nil {
		args = append(args, "--base-url", baseURL)
	}
	if tc.model != "" {
		args = append(args, "--model", tc.model)
	}
	if tc.name == "unsupported provider flag" {
		args = append(args, "--provider", "local")
	}
	return args
}

func assertConfigAddLocalError(t *testing.T, got error, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	if got.Error() != want {
		t.Fatalf("error = %q, want exact Cobra validation error %q", got, want)
	}
}

func assertConfigAddLocalOutput(t *testing.T, tc configAddLocalCase, configDir string, server *probeServer, got cliResult) {
	t.Helper()
	normalizedStdout := normalizeCLIOutput(got.stdout, configDir, server.URL)
	normalizedStderr := normalizeCLIOutput(got.stderr, configDir, server.URL)
	if normalizedStdout != tc.wantStdout {
		t.Fatalf("stdout = %q, want %q", normalizedStdout, tc.wantStdout)
	}
	if normalizedStderr != tc.wantStderr {
		t.Fatalf("stderr = %q, want %q", normalizedStderr, tc.wantStderr)
	}
	if !reflect.DeepEqual(server.paths, tc.wantPaths) {
		t.Fatalf("probe paths = %v, want %v", server.paths, tc.wantPaths)
	}
	data, err := os.ReadFile(filepath.Join(configDir, config.ConfigFileName))
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	assertConfigAddLocalPersistedData(t, tc, configDir, server.URL, data)
}

func assertConfigAddLocalPersistedData(t *testing.T, tc configAddLocalCase, configDir, serverURL string, data []byte) {
	t.Helper()
	if tc.name == "default reachable config" || tc.seed != "" {
		golden := "config_default.yaml"
		if tc.seed != "" {
			golden = "config_non_default.yaml"
		}
		assertConfigGolden(t, golden, normalizeCLIOutput(string(data), configDir, serverURL))
		return
	}
	if !strings.Contains(string(data), "model: offline-model") {
		t.Fatalf("warning path did not persist requested model: %s", data)
	}
}

func TestToolCommandListToolsPropagatesWriterError(t *testing.T) {
	cmd := NewToolCommand(flags.NewGlobalFlags())
	wantErr := errors.New("write failed")

	err := cmd.listTools(errWriter{err: wantErr}, []messages.ToolDefinition{{Name: "test"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("listTools error = %v, want %v", err, wantErr)
	}
}

func TestConfigAddLocalCommandRunPropagatesSummaryWriteError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()

	cmd := &cobra.Command{}
	cmd.SetOut(errWriter{err: errors.New("summary write failed")})
	cmd.SetErr(errWriter{err: errors.New("stderr write failed")})

	configCmd := NewConfigAddLocalCommand(globalFlags)
	configCmd.baseURL = server.URL
	configCmd.model = "test-model"

	err := configCmd.run(cmd)
	if err == nil {
		t.Fatal("expected summary write error, got nil")
	}
	if !strings.Contains(err.Error(), "write config summary") {
		t.Fatalf("error = %v, want write config summary context", err)
	}
}
