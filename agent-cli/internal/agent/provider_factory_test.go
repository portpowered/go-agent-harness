package agent

import (
	"errors"
	"testing"

	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

// stubProvider implements providers.Provider for testing.
type stubProvider struct {
	providers.Provider
	name string
}

func TestProviderFactory_Build_RegisteredName(t *testing.T) {
	factory := NewProviderFactory()
	factory.Register("test-provider", func(ctx ProviderBuildContext) (ProviderBuildResult, error) {
		return ProviderBuildResult{Provider: &stubProvider{name: "test"}}, nil
	})

	result, err := factory.Build("test-provider", ProviderBuildContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sp, ok := result.Provider.(*stubProvider)
	if !ok {
		t.Fatal("expected *stubProvider")
	}
	if sp.name != "test" {
		t.Errorf("expected name %q, got %q", "test", sp.name)
	}
}

func TestProviderFactory_Build_DefaultBuilder(t *testing.T) {
	factory := NewProviderFactory()
	factory.RegisterDefault(func(ctx ProviderBuildContext) (ProviderBuildResult, error) {
		return ProviderBuildResult{Provider: &stubProvider{name: "default"}}, nil
	})

	result, err := factory.Build("unknown", ProviderBuildContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sp, ok := result.Provider.(*stubProvider)
	if !ok {
		t.Fatal("expected *stubProvider")
	}
	if sp.name != "default" {
		t.Errorf("expected name %q, got %q", "default", sp.name)
	}
}

func TestProviderFactory_Build_UnknownProviderError(t *testing.T) {
	factory := NewProviderFactory()
	factory.Register("alpha", func(ctx ProviderBuildContext) (ProviderBuildResult, error) {
		return ProviderBuildResult{}, nil
	})

	_, err := factory.Build("beta", ProviderBuildContext{})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !contains(err.Error(), "unknown provider") {
		t.Errorf("error should mention 'unknown provider', got: %v", err)
	}
	if !contains(err.Error(), "alpha") {
		t.Errorf("error should list available provider 'alpha', got: %v", err)
	}
}

func TestProviderFactory_Build_BuilderErrorPropagated(t *testing.T) {
	factory := NewProviderFactory()
	buildErr := errors.New("config missing")
	factory.Register("broken", func(ctx ProviderBuildContext) (ProviderBuildResult, error) {
		return ProviderBuildResult{}, buildErr
	})

	_, err := factory.Build("broken", ProviderBuildContext{})
	if !errors.Is(err, buildErr) {
		t.Errorf("expected build error to be propagated, got: %v", err)
	}
}

func TestProviderFactory_Build_ExplicitOverridesDefault(t *testing.T) {
	factory := NewProviderFactory()
	factory.RegisterDefault(func(ctx ProviderBuildContext) (ProviderBuildResult, error) {
		return ProviderBuildResult{Provider: &stubProvider{name: "default"}}, nil
	})
	factory.Register("specific", func(ctx ProviderBuildContext) (ProviderBuildResult, error) {
		return ProviderBuildResult{Provider: &stubProvider{name: "specific"}}, nil
	})

	result, err := factory.Build("specific", ProviderBuildContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sp := result.Provider.(*stubProvider)
	if sp.name != "specific" {
		t.Errorf("expected 'specific', got %q", sp.name)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
