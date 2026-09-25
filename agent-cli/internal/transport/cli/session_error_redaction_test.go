package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionRunFailureRedactsProviderCredentialFromStderr pins the CLI
// error-rendering contract: when a provider failure echoes the session's API
// key, whether it came from --api-key or the config file, the error cobra
// renders to stderr carries the redaction marker instead of the key.
func TestSessionRunFailureRedactsProviderCredentialFromStderr(t *testing.T) {
	for _, test := range []struct {
		name      string
		configKey string
		flagKey   string
		secret    string
	}{
		{name: "flag credential", configKey: "sk-config-unused-000000", flagKey: "sk-flag-secret-1234567890", secret: "sk-flag-secret-1234567890"},
		{name: "config credential", configKey: "sk-config-secret-0987654321", secret: "sk-config-secret-0987654321"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			configYAML := "model:\n  provider: openai\n  openai:\n    model: gpt-realtime\n    api_key: " + test.configKey + "\n"
			if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
				t.Fatalf("write session config: %v", err)
			}
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = configDir
			inferencer := credentialEchoInferencer{secret: test.secret}
			command := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, inferencer, nil).Generate()
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			args := []string{"--prompt", "hello", "--provider", config.ProviderOpenAI, "--model", "gpt-realtime"}
			if test.flagKey != "" {
				args = append(args, "--api-key", test.flagKey)
			}
			command.SetArgs(args)

			err := command.ExecuteContext(context.Background())
			if err == nil {
				t.Fatal("session returned nil error, want provider failure")
			}
			for _, rendered := range []string{err.Error(), stderr.String(), stdout.String()} {
				if strings.Contains(rendered, test.secret) {
					t.Fatalf("rendered session failure leaks the credential: %q", rendered)
				}
			}
			if !strings.Contains(stderr.String(), "Incorrect API key provided: [REDACTED]") {
				t.Fatalf("stderr = %q, want the redacted provider failure", stderr.String())
			}
		})
	}
}

// credentialEchoInferencer fails provider connection the way a provider
// rejecting a key does: by echoing the key in its error.
type credentialEchoInferencer struct{ secret string }

func (i credentialEchoInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, fmt.Errorf("realtime connect: 401 Unauthorized: Incorrect API key provided: %s", i.secret)
}
