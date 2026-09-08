package agent

import (
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	session "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
)

func validationRunData(catalog ModelCatalog) *RunData {
	return &RunData{modelCatalog: catalog}
}

func validationExecutor(t *testing.T, catalog ModelCatalog, relaxed bool) *Executor {
	t.Helper()
	storage := session.NewStorage(t.TempDir())
	return NewExecutor(nil, nil, stubInferencer{}, relaxed).WithResolution(RuntimeResolution{
		Resolved:       true,
		Provider:       ProviderConfig{Provider: "test", Model: "test-model"},
		ModelCatalog:   catalog,
		Storage:        storage,
		WorkspaceDir:   storage.WorkspaceDir(),
		PromptResolved: true,
	})
}

func assertValidationError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("validation error = %v, want %q", err, want)
	}
}

func TestValidateOutputModalityUsesResolvedCatalog(t *testing.T) {
	imageCatalog := ModelCatalog{Models: []ModelInfo{{Name: "test-model", OutputModalities: []string{"image"}}}}
	for _, test := range []struct {
		name      string
		modality  string
		catalog   ModelCatalog
		relaxed   bool
		wantError string
	}{
		{name: "empty", modality: "", catalog: imageCatalog},
		{name: "text", modality: "text", catalog: imageCatalog},
		{name: "supported", modality: "image", catalog: imageCatalog},
		{name: "unknown catalog", modality: "audio", catalog: ModelCatalog{}},
		{name: "relaxed", modality: "audio", catalog: ModelCatalog{Models: []ModelInfo{{Name: "test-model"}}}, relaxed: true},
		{name: "unsupported", modality: "audio", catalog: imageCatalog, wantError: `model "test-model" does not support audio output`},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec := validationExecutor(t, test.catalog, test.relaxed)
			err := exec.validateOutputModality(&Config{OutputModality: test.modality}, validationRunData(test.catalog))
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("validateOutputModality() error = %v, want nil", err)
				}
				return
			}
			assertValidationError(t, err, test.wantError)
		})
	}
}

func TestValidateInputMimeTypesUsesResolvedCatalog(t *testing.T) {
	input := agentloop.ExecuteInput{ContentParts: []messages.ContentPart{
		messages.ImagePart{MediaType: "image/webp"},
	}}
	for _, test := range []struct {
		name      string
		catalog   ModelCatalog
		relaxed   bool
		wantError string
	}{
		{name: "empty catalog", catalog: ModelCatalog{}},
		{name: "empty support list", catalog: ModelCatalog{Models: []ModelInfo{{Name: "test-model"}}}},
		{name: "supported", catalog: ModelCatalog{Models: []ModelInfo{{Name: "test-model", SupportedInputMimeTypes: []string{"image/webp"}}}}},
		{name: "relaxed", relaxed: true, catalog: ModelCatalog{Models: []ModelInfo{{Name: "test-model", SupportedInputMimeTypes: []string{"image/png"}}}}},
		{name: "unsupported", catalog: ModelCatalog{Models: []ModelInfo{{Name: "test-model", SupportedInputMimeTypes: []string{"image/png"}}}}, wantError: `model "test-model" does not support input type "image/webp". supported types: image/png Tip: Convert with: convert input.webp output.png`},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec := validationExecutor(t, test.catalog, test.relaxed)
			err := exec.validateInputMimeTypes(&Config{}, validationRunData(test.catalog), input)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("validateInputMimeTypes() error = %v, want nil", err)
				}
				return
			}
			assertValidationError(t, err, test.wantError)
		})
	}
}

func TestValidateInputMimeTypesSkipsEmptyInput(t *testing.T) {
	exec := validationExecutor(t, ModelCatalog{Models: []ModelInfo{{Name: "test-model", SupportedInputMimeTypes: []string{"image/png"}}}}, false)
	if err := exec.validateInputMimeTypes(&Config{}, validationRunData(exec.resolvedCatalog), agentloop.ExecuteInput{}); err != nil {
		t.Fatalf("empty input validation error = %v, want nil", err)
	}
}

// Keep this assertion close to the validation tests so future changes do not
// reintroduce the old config-tree lookup as a hidden fallback.
func TestValidationModelDoesNotInferFromConfig(t *testing.T) {
	exec := NewExecutor(nil, nil, stubInferencer{})
	name, info := exec.validationModel(&Config{Model: "other"}, &RunData{modelCatalog: ModelCatalog{Models: []ModelInfo{{Name: "other"}}}})
	if name != "" || info != nil {
		t.Fatalf("unresolved validation model = (%q, %+v), want no inferred model", name, info)
	}
	if strings.Contains(name, "config") {
		t.Fatal("validation model unexpectedly mentions host config")
	}
}
