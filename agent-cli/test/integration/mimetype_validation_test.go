package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

// writeTestConfig creates a config.yaml and models.yaml in configDir for MIME type validation tests.
// The model is configured with the given supportedInputMimeTypes.
func writeTestConfig(t *testing.T, configDir string, modelName string, supportedMimeTypes []string) {
	t.Helper()

	configYAML := `model:
  provider: openrouter
  openrouter:
    model: ` + modelName + `
    api_key: fake-key-for-testing
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	// Build models.yaml with the test model and specified MIME types.
	mimeList := ""
	if len(supportedMimeTypes) > 0 {
		var items []string
		for _, m := range supportedMimeTypes {
			items = append(items, "      - "+m)
		}
		mimeList = "\n    supportedInputMimeTypes:\n" + strings.Join(items, "\n")
	}

	modelsYAML := `models:
  - name: ` + modelName + `
    providers: [openrouter]
    input_modalities: [text, image]
    output_modalities: [text]
    max_token_count: 128000
    supports_tool_use: true` + mimeList + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "models.yaml"), []byte(modelsYAML), 0644); err != nil {
		t.Fatalf("write models.yaml: %v", err)
	}
}

// TestMimeValidation_UnsupportedWebP verifies that providing a WebP file to a model
// that only supports PNG produces a clear error with the model name, rejected MIME type,
// supported types list, and a conversion hint — before any inference call is made.
func TestMimeValidation_UnsupportedWebP(t *testing.T) {
	tmpDir := t.TempDir()

	writeTestConfig(t, tmpDir, "test-png-only-model", []string{"image/png"})

	// Create a minimal WebP file (RIFF header + WEBP signature).
	webpPath := filepath.Join(tmpDir, "photo.webp")
	webpData := []byte("RIFF\x00\x00\x00\x00WEBP") // RIFF header + WEBP signature at bytes 8-11
	if err := os.WriteFile(webpPath, webpData, 0644); err != nil {
		t.Fatalf("write webp file: %v", err)
	}

	rec := &recordingInferencer{response: "should not be called"}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeAgentCLIWithInferencerOverride(exec, rec)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "--config-dir", tmpDir, "describe this image", webpPath})

	ctx := context.Background()
	err = rootCmd.ExecuteContext(ctx)

	// The command should fail with a MIME validation error.
	if err == nil {
		t.Fatal("expected error for unsupported WebP file, got nil")
	}

	errMsg := err.Error()

	// Assert error contains model name.
	if !strings.Contains(errMsg, "test-png-only-model") {
		t.Errorf("error should contain model name; got: %s", errMsg)
	}

	// Assert error contains the rejected MIME type.
	if !strings.Contains(errMsg, "image/webp") {
		t.Errorf("error should contain rejected MIME type 'image/webp'; got: %s", errMsg)
	}

	// Assert error contains the supported types list.
	if !strings.Contains(errMsg, "image/png") {
		t.Errorf("error should contain supported type 'image/png'; got: %s", errMsg)
	}

	// Assert error contains the conversion hint.
	if !strings.Contains(errMsg, "convert input.webp output.png") {
		t.Errorf("error should contain conversion hint; got: %s", errMsg)
	}

	// Assert no inference call was made (validation happened before inference).
	if len(rec.recorded) > 0 {
		t.Error("inferencer should not have been called; MIME validation should reject before inference")
	}
}
