package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInteractiveToolConfigDefaultsAreLoadedAndPersisted(t *testing.T) {
	dir := t.TempDir()
	storage := NewConfigStorage(filepath.Join(dir, ConfigFileName))

	cfg, err := storage.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	want := DefaultInteractiveToolConfig()
	if cfg.Tools.Interactive != want {
		t.Fatalf("interactive config = %+v, want %+v", cfg.Tools.Interactive, want)
	}
	data, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	for _, value := range []string{"fast_read_timeout: 5s", "long_running_timeout: 20s", "acknowledgement_threshold: 2s"} {
		if !strings.Contains(string(data), value) {
			t.Fatalf("generated config missing %q:\n%s", value, data)
		}
	}
}

func TestInteractiveToolConfigOverridesLoadFromYAMLAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(`tools:
  interactive:
    fast_read_timeout: 7500ms
    long_running_timeout: 12s
    acknowledgement_threshold: 1500ms
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("AGENT_TOOLS__INTERACTIVE__FAST_READ_TIMEOUT", "3s")

	cfg, err := NewConfigStorage(path).Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	got := cfg.Tools.Interactive
	if got.FastReadTimeout != 3*time.Second || got.LongRunningTimeout != 12*time.Second || got.AcknowledgementThreshold != 1500*time.Millisecond {
		t.Fatalf("interactive config = %+v, want environment/YAML overrides", got)
	}
}

func TestInteractiveToolConfigRejectsInvalidValues(t *testing.T) {
	defaults := DefaultInteractiveToolConfig()
	cases := []struct {
		name   string
		mutate func(*InteractiveToolConfig)
		want   string
	}{
		{name: "zero fast read", mutate: func(c *InteractiveToolConfig) { c.FastReadTimeout = 0 }, want: "fast_read_timeout"},
		{name: "negative fast read", mutate: func(c *InteractiveToolConfig) { c.FastReadTimeout = -time.Second }, want: "fast_read_timeout"},
		{name: "ten second fast read", mutate: func(c *InteractiveToolConfig) { c.FastReadTimeout = 10 * time.Second }, want: "less than 10s"},
		{name: "zero long running", mutate: func(c *InteractiveToolConfig) { c.LongRunningTimeout = 0 }, want: "long_running_timeout"},
		{name: "thirty second long running", mutate: func(c *InteractiveToolConfig) { c.LongRunningTimeout = 30 * time.Second }, want: "less than 30s"},
		{name: "zero acknowledgement", mutate: func(c *InteractiveToolConfig) { c.AcknowledgementThreshold = 0 }, want: "acknowledgement_threshold"},
		{name: "late acknowledgement", mutate: func(c *InteractiveToolConfig) { c.AcknowledgementThreshold = 2500 * time.Millisecond }, want: "no greater than 2s"},
		{name: "no time to acknowledge", mutate: func(c *InteractiveToolConfig) { c.LongRunningTimeout = 2 * time.Second }, want: "must exceed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := defaults
			tc.mutate(&value)
			err := value.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestConfigStorageRejectsExplicitZeroInteractiveDurationBeforeUnmarshal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("tools:\n  interactive:\n    fast_read_timeout: 0s\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := NewConfigStorage(path).Load()
	if err == nil || !strings.Contains(err.Error(), "fast_read_timeout") || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("Load() error = %v, want actionable fast-read validation", err)
	}
}
