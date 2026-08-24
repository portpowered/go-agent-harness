package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func validationRunData(models *config.ModelsConfig) *RunData {
	return &RunData{Models: models}
}

type validationErrorContract interface {
	Error() string
}

func assertValidationError(t *testing.T, err error, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatalf("validation error = nil, want %q", wantMessage)
	}
	// These validators currently return bare fmt.Errorf values: there is no
	// exported sentinel or custom type for errors.Is/errors.As identity matching.
	// Preserve the available typed error contract and exact message until that
	// production API can expose a stable classification without changing this lane.
	var typed validationErrorContract
	if !errors.As(err, &typed) {
		t.Fatalf("validation error %T does not satisfy the error contract", err)
	}
	if typed.Error() != wantMessage {
		t.Fatalf("validation error = %q, want exact message %q", typed.Error(), wantMessage)
	}
}

func TestValidateOutputModality_S4ErrorTableAndControls(t *testing.T) {
	tests := []struct {
		name      string
		makeExec  func() *Executor
		cfg       *Config
		runData   *RunData
		wantError string
	}{
		{
			name:      "relaxed validation",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil, true) },
			cfg:       &Config{OutputModality: "audio"},
			runData:   validationRunData(nil),
			wantError: "",
		},
		{
			name:      "empty modality",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{},
			runData:   validationRunData(nil),
			wantError: "",
		},
		{
			name:      "text modality",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{OutputModality: "text"},
			runData:   validationRunData(nil),
			wantError: "",
		},
		{
			name:      "test inferencer without config directory",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, stubInferencer{}) },
			cfg:       &Config{OutputModality: "audio"},
			runData:   validationRunData(nil),
			wantError: "",
		},
		{
			name: "config load failure is allowed",
			makeExec: func() *Executor {
				return NewExecutor(nil, nil, nil)
			},
			cfg:       &Config{ConfigDir: validationConfigFile(t), OutputModality: "audio"},
			runData:   validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{Name: "gpt-4o"}}}),
			wantError: "",
		},
		{
			name: "provider without openai config is allowed",
			makeExec: func() *Executor {
				return NewExecutor(nil, nil, nil)
			},
			cfg:       &Config{ConfigDir: validationConfigDir(t, "model:\n  provider: fal\n  fal:\n    model: fal-model\n    api_key: test-key\n"), OutputModality: "audio"},
			runData:   validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{Name: "fal-model"}}}),
			wantError: "",
		},
		{
			name:      "models unavailable",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key")), OutputModality: "audio"},
			runData:   validationRunData(nil),
			wantError: "",
		},
		{
			name:      "unknown model is delegated",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key")), OutputModality: "audio"},
			runData:   validationRunData(&config.ModelsConfig{}),
			wantError: "",
		},
		{
			name:     "unsupported output modality",
			makeExec: func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:      &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key")), OutputModality: "audio"},
			runData: validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{
				Name:             "gpt-4o",
				OutputModalities: []string{"text"},
			}}}),
			wantError: `model "gpt-4o" does not support audio output`,
		},
		{
			name:     "supported output modality",
			makeExec: func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:      &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key")), OutputModality: "audio"},
			runData: validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{
				Name:             "gpt-4o",
				OutputModalities: []string{"audio"},
			}}}),
			wantError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.makeExec().validateOutputModality(tt.cfg, tt.runData)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateOutputModality() error = %v, want nil", err)
				}
				return
			}
			assertValidationError(t, err, tt.wantError)
		})
	}
}

func TestValidateInputMimeTypes_S4ErrorTableAndControls(t *testing.T) {
	inputWithImage := agentloop.ExecuteInput{ContentParts: []messages.ContentPart{
		messages.ImagePart{MediaType: "image/webp"},
	}}

	tests := []struct {
		name      string
		makeExec  func() *Executor
		cfg       *Config
		runData   *RunData
		input     agentloop.ExecuteInput
		wantError string
	}{
		{
			name:      "relaxed validation",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil, true) },
			cfg:       &Config{},
			runData:   validationRunData(nil),
			input:     inputWithImage,
			wantError: "",
		},
		{
			name:      "empty content list",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{},
			runData:   validationRunData(nil),
			input:     agentloop.ExecuteInput{},
			wantError: "",
		},
		{
			name:      "test inferencer without config directory",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, stubInferencer{}) },
			cfg:       &Config{},
			runData:   validationRunData(nil),
			input:     inputWithImage,
			wantError: "",
		},
		{
			name:      "config load failure is allowed",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{ConfigDir: validationConfigFile(t)},
			runData:   validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{Name: "gpt-4o"}}}),
			input:     inputWithImage,
			wantError: "",
		},
		{
			name:      "provider without openai config is allowed",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{ConfigDir: validationConfigDir(t, "model:\n  provider: fal\n  fal:\n    model: fal-model\n    api_key: test-key\n")},
			runData:   validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{Name: "fal-model"}}}),
			input:     inputWithImage,
			wantError: "",
		},
		{
			name:      "models unavailable",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key"))},
			runData:   validationRunData(nil),
			input:     inputWithImage,
			wantError: "",
		},
		{
			name:      "unknown model is delegated",
			makeExec:  func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:       &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key"))},
			runData:   validationRunData(&config.ModelsConfig{}),
			input:     inputWithImage,
			wantError: "",
		},
		{
			name:     "empty supported mime list accepts input",
			makeExec: func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:      &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key"))},
			runData: validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{
				Name:                    "gpt-4o",
				SupportedInputMimeTypes: nil,
			}}}),
			input:     inputWithImage,
			wantError: "",
		},
		{
			name:     "unsupported input mime",
			makeExec: func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:      &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key"))},
			runData: validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{
				Name:                    "gpt-4o",
				SupportedInputMimeTypes: []string{"image/png"},
			}}}),
			input:     inputWithImage,
			wantError: `model "gpt-4o" does not support input type "image/webp". supported types: image/png Tip: Convert with: convert input.webp output.png`,
		},
		{
			name:     "supported input mime",
			makeExec: func() *Executor { return NewExecutor(nil, nil, nil) },
			cfg:      &Config{ConfigDir: validationConfigDir(t, validOpenRouterConfig("test-key"))},
			runData: validationRunData(&config.ModelsConfig{Models: []config.ModelInfo{{
				Name:                    "gpt-4o",
				SupportedInputMimeTypes: []string{"image/webp"},
			}}}),
			input:     inputWithImage,
			wantError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.makeExec().validateInputMimeTypes(tt.cfg, tt.runData, tt.input)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateInputMimeTypes() error = %v, want nil", err)
				}
				return
			}
			assertValidationError(t, err, tt.wantError)
		})
	}
}

func validationConfigDir(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	writeExecutorConfig(t, dir, contents)
	return dir
}

func validationConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config-parent-is-a-file")
	if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
