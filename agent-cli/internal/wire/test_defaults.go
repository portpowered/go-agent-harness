package wire

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/agent-cli/internal/flags"
)

var (
	mockConfigOnce sync.Once
	mockConfigDir  string
	mockConfigErr  error
)

func applyDeterministicMockDefaults(globalFlags *flags.GlobalFlags) error {
	if globalFlags.ConfigDirPath != "" {
		return nil
	}

	mockConfigOnce.Do(func() {
		mockConfigDir, mockConfigErr = createDeterministicMockConfig()
	})
	if mockConfigErr != nil {
		return mockConfigErr
	}

	globalFlags.ConfigDirPath = mockConfigDir
	return nil
}

func createDeterministicMockConfig() (string, error) {
	dir, err := os.MkdirTemp("", "agent-cli-mock-config-*")
	if err != nil {
		return "", fmt.Errorf("create mock config dir: %w", err)
	}

	configYAML := `model:
  provider: openrouter
  openrouter:
    model: test-mock-model
    api_key: test-key-not-real
`
	if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte(configYAML), 0644); err != nil {
		return "", fmt.Errorf("write mock config.yaml: %w", err)
	}

	modelsYAML := `models:
  - name: test-mock-model
    aliases: [z-ai/glm-4.7]
    providers: [openrouter]
    input_modalities: [text, image, audio, video]
    output_modalities: [text, image, audio, video, embedding]
    max_token_count: 128000
    supports_tool_use: true
`
	if err := os.WriteFile(filepath.Join(dir, config.ModelsFileName), []byte(modelsYAML), 0644); err != nil {
		return "", fmt.Errorf("write mock models.yaml: %w", err)
	}

	return dir, nil
}
