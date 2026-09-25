package livehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeProvidersWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

type imageAdmissionCase struct {
	name      string
	provider  string
	model     string
	modelsYML string
	mediaType string
	wantErr   string
}

// TestBuildRequestAdmitsSessionImagesOnlyForImageCapableModels pins the
// session image capability contract: both an opening --image and a
// tool-provided read_image are refused, with the model named, when the
// selected provider/model cannot accept image input; configured model metadata
// may reject the model or narrow its MIME set.
func TestBuildRequestAdmitsSessionImagesOnlyForImageCapableModels(t *testing.T) {
	for _, test := range []imageAdmissionCase{
		{name: "catalog image model", provider: config.ProviderOpenAI, model: "gpt-realtime-2.1", mediaType: "image/png"},
		{
			name: "non-image provider", provider: config.ProviderGrok, model: "grok-voice", mediaType: "image/png",
			wantErr: `model "grok-voice" does not support image input capability`,
		},
		{
			name: "configured modalities without image", provider: config.ProviderOpenAI, model: "gpt-realtime-2.1", mediaType: "image/png",
			modelsYML: "models:\n  - name: gpt-realtime-2.1\n    providers: [openai]\n    input_modalities: [text, audio]\n",
			wantErr:   `model "gpt-realtime-2.1" does not support image input capability`,
		},
		{
			name: "configured MIME narrowing", provider: config.ProviderOpenAI, model: "gpt-realtime-2.1", mediaType: "image/png",
			modelsYML: "models:\n  - name: gpt-realtime-2.1\n    providers: [openai]\n    input_modalities: [text, image]\n    supportedInputMimeTypes: [image/jpeg]\n",
			wantErr:   `session image "photo.png" has unsupported MIME type "image/png" (supported: image/jpeg)`,
		},
	} {
		t.Run(test.name, func(t *testing.T) { assertImageAdmissionCase(t, test) })
	}
}

func assertImageAdmissionCase(t *testing.T, test imageAdmissionCase) {
	t.Helper()
	configDir := t.TempDir()
	if test.modelsYML != "" {
		if err := os.WriteFile(filepath.Join(configDir, config.ModelsFileName), []byte(test.modelsYML), 0o600); err != nil {
			t.Fatalf("write models.yaml: %v", err)
		}
	}
	opened := 0
	var toolOpen ImageOpener
	deps := RequestDependencies{
		InstructionService: runtimeSessionWire.NewInstructionService(),
		ModelCatalog:       runtimeProvidersWire.NewModelCatalog(),
		OpenImages: func(paths []string) ([]messages.ContentPart, error) {
			opened++
			parts := make([]messages.ContentPart, len(paths))
			for index := range paths {
				parts[index] = messages.ImagePart{Bytes: []byte("image"), MediaType: test.mediaType}
			}
			return parts, nil
		},
		Capabilities: func(context.Context, *config.Config) (*runtimeSession.LiveCapabilities, error) {
			return &runtimeSession.LiveCapabilities{}, nil
		},
		BindImagePreparer: func(executor messages.ToolExecutor, bound ImageOpener) messages.ToolExecutor {
			toolOpen = bound
			return executor
		},
	}
	request := serviceSession.Request{
		Provider: test.provider, Model: test.model, APIKey: "test-key", ProviderProvided: true, ModelProvided: true,
		ConfigDir: configDir, LoadedConfig: &config.Config{}, SystemPrompt: "none",
	}

	// A session without --image still binds read_image; the tool path must
	// enforce the same capability decision.
	if _, err := BuildRequest(context.Background(), request, nil, deps); err != nil || toolOpen == nil {
		t.Fatalf("BuildRequest without images: err=%v, read_image bound=%t", err, toolOpen != nil)
	}
	_, toolErr := toolOpen([]string{"photo.png"})
	assertImageAdmission(t, "read_image", toolErr, test.wantErr)

	opened = 0
	request.ImagePaths = []string{"photo.png"}
	got, err := BuildRequest(context.Background(), request, nil, deps)
	assertImageAdmission(t, "--image", err, test.wantErr)
	if test.wantErr == "" && len(got.OpeningContentParts) != 1 {
		t.Fatalf("opening parts = %d, want 1", len(got.OpeningContentParts))
	}
	var capabilityErr *ImageCapabilityError
	if errors.As(err, &capabilityErr) && opened != 0 {
		t.Fatalf("image opener ran %d time(s) for a model without image input", opened)
	}
}

func assertImageAdmission(t *testing.T, path string, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("%s admission: %v", path, err)
		}
		return
	}
	if err == nil || err.Error() != want {
		t.Fatalf("%s admission error = %v, want %q", path, err, want)
	}
}
