package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
)

func TestPathResolverExpandsLeadingTildeForms(t *testing.T) {
	currentHome := filepath.Join(t.TempDir(), "current-home")
	namedHome := filepath.Join(t.TempDir(), "named-home")
	resolver := &pathResolver{
		currentHome: func() (string, error) { return currentHome, nil },
		lookupUser: func(name string) (string, error) {
			if name != "alice" {
				return "", fmt.Errorf("unexpected user %q", name)
			}
			return namedHome, nil
		},
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "current home", input: "~", want: currentHome},
		{name: "current home child", input: "~/config/settings.yaml", want: filepath.Join(currentHome, "config", "settings.yaml")},
		{name: "named home", input: "~alice", want: namedHome},
		{name: "named home child", input: "~alice/config/settings.yaml", want: filepath.Join(namedHome, "config", "settings.yaml")},
		{name: "empty value", input: "", want: ""},
		{name: "relative path", input: "config/settings.yaml", want: "config/settings.yaml"},
		{name: "absolute path", input: filepath.Join(string(os.PathSeparator), "tmp", "settings.yaml"), want: filepath.Join(string(os.PathSeparator), "tmp", "settings.yaml")},
		{name: "stream sentinel", input: "-", want: "-"},
		{name: "endpoint", input: "https://example.test/~alice", want: "https://example.test/~alice"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolver.Resolve(tc.input)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if strings.HasPrefix(tc.input, "~") && tc.input != "" && !filepath.IsAbs(got) {
				t.Fatalf("expanded path %q is not absolute", got)
			}
		})
	}
}

func TestPathResolverReportsLookupFailuresWithoutFallback(t *testing.T) {
	lookupErr := errors.New("home lookup unavailable")
	tests := []struct {
		name          string
		input         string
		resolver      *pathResolver
		wantSubstring string
	}{
		{
			name:  "current home lookup",
			input: "~/config.yaml",
			resolver: &pathResolver{currentHome: func() (string, error) {
				return "", lookupErr
			}},
			wantSubstring: "current home lookup failed",
		},
		{
			name:  "named user lookup",
			input: "~alice/config.yaml",
			resolver: &pathResolver{
				lookupUser: func(string) (string, error) { return "", lookupErr },
			},
			wantSubstring: `lookup home for user "alice" failed`,
		},
		{
			name:          "empty current home",
			input:         "~/config.yaml",
			resolver:      &pathResolver{currentHome: func() (string, error) { return "", nil }},
			wantSubstring: "home directory for current user is empty",
		},
		{
			name:          "malformed NUL",
			input:         "~\x00/config.yaml",
			resolver:      &pathResolver{},
			wantSubstring: "malformed leading-tilde path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.resolver.Resolve(tc.input)
			if got != "" {
				t.Fatalf("Resolve(%q) returned path %q on failure", tc.input, got)
			}
			if err == nil {
				t.Fatalf("Resolve(%q) returned nil error", tc.input)
			}
			var resolutionErr *PathResolutionError
			if !errors.As(err, &resolutionErr) {
				t.Fatalf("Resolve(%q) error %T = %v, want PathResolutionError", tc.input, err, err)
			}
			if resolutionErr.Path != tc.input {
				t.Fatalf("resolution error path = %q, want %q", resolutionErr.Path, tc.input)
			}
			if !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Fatalf("error = %q, want %q", err, tc.wantSubstring)
			}
			if tc.input != "~\x00/config.yaml" && !strings.Contains(err.Error(), tc.input) {
				t.Fatalf("error = %q, want input %q", err, tc.input)
			}
		})
	}
}

func TestRouterResolveConfigDirUsesOneAbsoluteEffectivePath(t *testing.T) {
	currentHome := filepath.Join(t.TempDir(), "home")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = "~/workspace"
	router := &Router{
		Flags: globalFlags,
		pathResolver: &pathResolver{
			currentHome: func() (string, error) { return currentHome, nil },
		},
	}

	if err := router.resolveConfigDir(); err != nil {
		t.Fatalf("resolveConfigDir(): %v", err)
	}
	want := filepath.Join(currentHome, "workspace")
	if globalFlags.ConfigDirPath != want {
		t.Fatalf("effective config dir = %q, want %q", globalFlags.ConfigDirPath, want)
	}
	if !filepath.IsAbs(globalFlags.ConfigDirPath) {
		t.Fatalf("effective config dir %q is not absolute", globalFlags.ConfigDirPath)
	}
}

func TestConfigAddLocalLeadingTildeWritesBelowHome(t *testing.T) {
	home := t.TempDir()
	workingDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("chdir to test working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

	server := newProbeServer(t, map[string]int{"/models": 200})
	defer server.Close()
	got := executeGeneratedCLI(context.Background(), "", "-C", "~/x", "config", "add-local", "--base-url", server.URL, "--model", "home-model")
	if got.err != nil {
		t.Fatalf("execute config add-local: %v", got.err)
	}

	configPath := filepath.Join(home, "x", config.ConfigFileName)
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected config at expanded path %q: %v", configPath, err)
	}
	if _, err := os.Stat(filepath.Join(workingDir, "~")); !os.IsNotExist(err) {
		t.Fatalf("literal working-directory tilde tree exists or could not be checked: %v", err)
	}
	if !strings.Contains(got.stdout, configPath) {
		t.Fatalf("stdout = %q, want expanded config path %q", got.stdout, configPath)
	}
}

func TestConfigDirResolutionFailurePrecedesConfigSideEffects(t *testing.T) {
	workingDir := t.TempDir()
	targetHome := t.TempDir()
	resolverErr := errors.New("injected home lookup failure")
	resolver := &pathResolver{
		currentHome: func() (string, error) { return "", resolverErr },
	}
	root := newGeneratedCLIRootWithPathResolver("", resolver)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"-C", "~/x", "config", "add-local", "--base-url", "http://127.0.0.1:1", "--model", "should-not-write"})

	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("chdir to test working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected config-dir resolution error")
	} else if !strings.Contains(err.Error(), `resolve --config-dir`) || !strings.Contains(err.Error(), `"~/x"`) || !strings.Contains(err.Error(), resolverErr.Error()) {
		t.Fatalf("error = %q, want config-dir input and lookup failure", err)
	}
	if _, err := os.Stat(filepath.Join(workingDir, "~")); !os.IsNotExist(err) {
		t.Fatalf("literal working-directory tilde tree exists or could not be checked: %v", err)
	}
	if entries, err := os.ReadDir(targetHome); err != nil {
		t.Fatalf("read target home: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("target home changed before resolution: %v", entries)
	}
	if strings.Contains(stdout.String(), "Local provider added") || strings.Contains(stdout.String(), "Server reachable") {
		t.Fatalf("stdout = %q, want no command-specific output", stdout.String())
	}
}
