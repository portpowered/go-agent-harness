package livehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeProvidersWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestStageLiveOpeningImagesRewritesAndCleansCapabilityPaths(t *testing.T) {
	root := t.TempDir()
	imageBytes := []byte("staged-image")
	baseDefinitions := []messages.ToolDefinition{{
		Name: runtimeTools.ReadImageToolID,
		Parameters: []messages.ToolParameter{{
			Name:        "path",
			Type:        "string",
			Description: "Path to the local image to attach",
			Required:    true,
		}},
	}}
	request := serviceSession.Request{
		ConfigDir:  root,
		ImagePaths: []string{filepath.Join(root, "source.png")},
	}
	liveRequest := runtimeSession.LiveRequest{
		OpeningContentParts: []messages.ContentPart{
			messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
		},
		Capabilities: &runtimeSession.LiveCapabilities{
			Definitions: baseDefinitions,
			RefreshDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
				return baseDefinitions, nil
			},
		},
	}

	cleanup, err := stageLiveOpeningImages(request, &liveRequest)
	if err != nil {
		t.Fatalf("stageLiveOpeningImages: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup staged images: %v", err)
		}
	})

	path := stagedReadImagePath(t, liveRequest.Capabilities.Definitions)
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, filepath.Clean(root)+string(os.PathSeparator)) {
		t.Fatalf("staged path = %q, want an absolute path under %q", path, root)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged image: %v", err)
	}
	if string(got) != string(imageBytes) {
		t.Fatalf("staged image = %q, want %q", got, imageBytes)
	}

	refreshed, err := liveRequest.Capabilities.RefreshDefinitions(context.Background())
	if err != nil {
		t.Fatalf("refresh staged definitions: %v", err)
	}
	refreshedPath := stagedReadImagePath(t, refreshed)
	if refreshedPath != path {
		t.Fatalf("refreshed staged path = %q, want %q", refreshedPath, path)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup staged images: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged image after cleanup: err=%v, want not-exist", err)
	}
}

func stagedReadImagePath(t *testing.T, definitions []messages.ToolDefinition) string {
	t.Helper()
	const marker = "Session-staged image path(s) (use one of these exact absolute paths):\n- "
	for _, definition := range definitions {
		if definition.Name != runtimeTools.ReadImageToolID {
			continue
		}
		for _, parameter := range definition.Parameters {
			if parameter.Name != "path" {
				continue
			}
			index := strings.Index(parameter.Description, marker)
			if index < 0 {
				t.Fatalf("read_image path description = %q, want staging marker", parameter.Description)
			}
			path := parameter.Description[index+len(marker):]
			if lineEnd := strings.IndexByte(path, '\n'); lineEnd >= 0 {
				path = path[:lineEnd]
			}
			return path
		}
	}
	t.Fatal("read_image definition or path parameter missing")
	return ""
}

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
