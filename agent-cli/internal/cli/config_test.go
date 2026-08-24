package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/spf13/cobra"
)

var updateGoldens = flag.Bool("update", false, "update CLI golden files")

type cliResult struct {
	stdout string
	stderr string
	err    error
}

func newGeneratedCLIRoot(configDir string) *cobra.Command {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	askFlags := flags.NewAskFlags()
	loopFlags := flags.NewLoopFlags()
	chatFlags := flags.NewChatFlags()

	router := NewRouter(
		globalFlags,
		NewRootCommand(globalFlags),
		NewAskCommand(nil, askFlags, loopFlags, globalFlags),
		NewChatCommand(nil, askFlags, loopFlags, chatFlags, globalFlags),
		NewToolCommand(globalFlags),
		NewInteractionCommand(),
		NewInteractionReplayCommand(),
		NewProbeCommand(),
		NewProbeRunCommand(),
		NewSessionCommand(askFlags, globalFlags, nil),
		NewSessionShowCommand(globalFlags),
		NewSessionListCommand(globalFlags),
		NewSessionDeleteCommand(globalFlags),
		NewConfigCommand(),
		NewConfigAddLocalCommand(globalFlags),
	)
	return NewAgentCLI(router).Generate()
}

func executeGeneratedCLI(ctx context.Context, configDir string, args ...string) cliResult {
	var stdout, stderr bytes.Buffer
	root := newGeneratedCLIRoot(configDir)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return cliResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func TestConfigAddLocalS2FlagMatrix(t *testing.T) {
	configSummaryPath := filepath.Join("<config-dir>", config.ConfigFileName)
	tests := []struct {
		name       string
		baseURL    func(string) string
		statuses   map[string]int
		seed       string
		model      string
		wantStdout string
		wantStderr string
		wantError  string
		wantPaths  []string
	}{
		{
			name:    "default reachable config",
			baseURL: func(url string) string { return url },
			statuses: map[string]int{
				"/models": http.StatusOK,
			},
			model: "llama3",
			wantStdout: "Server reachable at <local-server>/models\n" +
				"Local provider added to " + configSummaryPath + "\n" +
				"  provider: local\n  base_url: <local-server>\n  model: llama3\n",
			wantPaths: []string{"/models"},
		},
		{
			name:    "non-default /v1 config",
			baseURL: func(url string) string { return url + "/v1" },
			statuses: map[string]int{
				"/v1/models": http.StatusOK,
			},
			seed:  nonDefaultConfig,
			model: "custom-local-model",
			wantStdout: "Server reachable at <local-server>/v1/models\n" +
				"Local provider added to " + configSummaryPath + "\n" +
				"  provider: local\n  base_url: <local-server>/v1\n  model: custom-local-model\n",
			wantPaths: []string{"/v1/models"},
		},
		{
			name:    "unreachable server warns and still persists",
			baseURL: func(url string) string { return url },
			statuses: map[string]int{
				"/models":    http.StatusServiceUnavailable,
				"/v1/models": http.StatusServiceUnavailable,
			},
			model:      "offline-model",
			wantStdout: "Local provider added to " + configSummaryPath + "\n  provider: local\n  base_url: <local-server>\n  model: offline-model\n",
			wantStderr: "Warning: could not reach server at <local-server> (server may not be running yet)\n",
			wantPaths:  []string{"/models", "/v1/models"},
		},
		{
			name:      "missing base url",
			model:     "llama3",
			wantError: `required flag(s) "base-url" not set`,
		},
		{
			name:      "missing model",
			baseURL:   func(url string) string { return url },
			wantError: `required flag(s) "model" not set`,
		},
		{
			name:      "missing required pair",
			wantError: `required flag(s) "base-url", "model" not set`,
		},
		{
			name:      "unsupported provider flag",
			baseURL:   func(url string) string { return url },
			model:     "llama3",
			wantError: "unknown flag: --provider",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			server := newProbeServer(t, tc.statuses)
			defer server.Close()

			baseURL := ""
			if tc.baseURL != nil {
				baseURL = tc.baseURL(server.URL)
			}
			if tc.seed != "" {
				if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(tc.seed), 0600); err != nil {
					t.Fatalf("seed config: %v", err)
				}
			}

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

			got := executeGeneratedCLI(context.Background(), configDir, args...)
			if tc.wantError != "" {
				if got.err == nil {
					t.Fatalf("expected error %q, got nil", tc.wantError)
				}
				if got.err.Error() != tc.wantError {
					t.Fatalf("error = %q, want exact Cobra validation error %q", got.err, tc.wantError)
				}
				return
			}
			if got.err != nil {
				t.Fatalf("execute config add-local: %v", got.err)
			}

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
			if tc.name == "default reachable config" || tc.seed != "" {
				golden := "config_default.yaml"
				if tc.seed != "" {
					golden = "config_non_default.yaml"
				}
				assertConfigGolden(t, golden, normalizeCLIOutput(string(data), configDir, server.URL))
			} else if !strings.Contains(string(data), "model: offline-model") {
				t.Fatalf("warning path did not persist requested model: %s", data)
			}
		})
	}
}

func TestConfigAddLocalUsesIsolatedDefaultHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	server := newProbeServer(t, map[string]int{"/models": http.StatusOK})
	defer server.Close()

	got := executeGeneratedCLI(context.Background(), "", "config", "add-local", "--base-url", server.URL, "--model", "home-model")
	if got.err != nil {
		t.Fatalf("execute config with default home: %v", got.err)
	}
	configPath := filepath.Join(home, config.ConfigDirName, config.ConfigFileName)
	rel, err := filepath.Rel(home, configPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("config path escaped isolated home: %q", configPath)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config was not written below isolated home: %v", err)
	}
}

func TestConfigAddLocalInvalidConfigHasCommandContext(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte("model: ["), 0600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	got := executeGeneratedCLI(context.Background(), configDir, "--config-dir", configDir, "config", "add-local", "--base-url", "http://127.0.0.1:1", "--model", "broken")
	if got.err == nil {
		t.Fatal("expected invalid config error")
	}
	if !strings.Contains(got.err.Error(), "load config") || !strings.Contains(got.err.Error(), "config.yaml") {
		t.Fatalf("error = %q, want load context and config path", got.err)
	}
}

func TestConfigRenderingS3Goldens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seed   string
		model  string
		golden string
	}{
		{name: "default", model: "llama3", golden: "config_default.yaml"},
		{name: "non-default", seed: nonDefaultConfig, model: "custom-local-model", golden: "config_non_default.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			if tc.seed != "" {
				if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(tc.seed), 0600); err != nil {
					t.Fatalf("seed config: %v", err)
				}
			}
			server := newProbeServer(t, map[string]int{"/v1/models": http.StatusOK})
			defer server.Close()
			baseURL := server.URL
			if tc.seed != "" {
				baseURL += "/v1"
			}
			got := executeGeneratedCLI(context.Background(), configDir, "--config-dir", configDir, "config", "add-local", "--base-url", baseURL, "--model", tc.model)
			if got.err != nil {
				t.Fatalf("execute golden command: %v", got.err)
			}
			data, err := os.ReadFile(filepath.Join(configDir, config.ConfigFileName))
			if err != nil {
				t.Fatalf("read golden config: %v", err)
			}
			assertConfigGolden(t, tc.golden, normalizeCLIOutput(string(data), configDir, server.URL))
		})
	}
}

func TestConfigRenderingRedactsEnvironmentAPIKey(t *testing.T) {
	const sentinel = "s2s-config-redaction-sentinel-20260816"
	for _, envName := range []string{
		"AGENT_MODEL__OPENAI__API_KEY",
		"AGENT_MODEL__CLAUDE__API_KEY",
		"AGENT_MODEL__OPENROUTER__API_KEY",
		"AGENT_MODEL__LOCAL__API_KEY",
		"AGENT_MODEL__FAL__API_KEY",
		"AGENT_MODEL__GROK__API_KEY",
	} {
		t.Setenv(envName, sentinel)
	}
	configDir := t.TempDir()
	server := newProbeServer(t, map[string]int{"/models": http.StatusOK})
	defer server.Close()

	got := executeGeneratedCLI(context.Background(), configDir, "--config-dir", configDir, "config", "add-local", "--base-url", server.URL, "--model", "redaction-model")
	if got.err != nil {
		t.Fatalf("execute config redaction case: %v", got.err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, config.ConfigFileName))
	if err != nil {
		t.Fatalf("read rendered config: %v", err)
	}
	rendered := got.stdout + got.stderr + string(data)
	if strings.Contains(rendered, sentinel) {
		t.Fatalf("rendered config must omit environment secret %q", sentinel)
	}
	if !strings.Contains(rendered, redactedAPIKey) {
		t.Fatalf("rendered config must omit the environment secret and include <redacted>")
	}
}

type failOnWrite struct {
	failAt int
	writes int
	err    error
}

func (w *failOnWrite) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func TestConfigAddLocalSummaryWriteFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failAt int
	}{
		{name: "provider summary", failAt: 3},
		{name: "base URL summary", failAt: 4},
		{name: "model summary", failAt: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newProbeServer(t, map[string]int{"/models": http.StatusOK})
			defer server.Close()

			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = t.TempDir()
			cmd := &cobra.Command{}
			cmd.SetOut(&failOnWrite{failAt: tc.failAt, err: errors.New("summary write failed")})
			cmd.SetErr(&bytes.Buffer{})

			configCmd := NewConfigAddLocalCommand(globalFlags)
			configCmd.baseURL = server.URL
			configCmd.model = "test-model"
			err := configCmd.run(cmd)
			if err == nil || !strings.Contains(err.Error(), "write config summary") {
				t.Fatalf("run error = %v, want write config summary context", err)
			}
		})
	}
}

const nonDefaultConfig = `model:
  provider: openrouter
  openrouter:
    model: seeded-model
    base_url: https://example.test/v1
  continuation_nudge_enabled: true
  continuation_nudge_message: continue now
  repetition_penalty: 1.25
tools:
  exec:
    enable_deny_patterns: false
    custom_deny_patterns:
      - seeded-pattern
  list:
    - id: exec
      enabled: false
    - id: sleep
      enabled: true
`

type probeServer struct {
	*httptest.Server
	paths []string
}

func newProbeServer(t *testing.T, statuses map[string]int) *probeServer {
	t.Helper()
	server := &probeServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.paths = append(server.paths, r.URL.Path)
		status := statuses[r.URL.Path]
		if status == 0 {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
	}))
	return server
}

func normalizeCLIOutput(value, configDir, serverURL string) string {
	value = strings.ReplaceAll(value, serverURL, "<local-server>")
	return strings.ReplaceAll(value, filepath.Clean(configDir), "<config-dir>")
}

func assertConfigGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0600); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update only when intentionally regenerating): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
